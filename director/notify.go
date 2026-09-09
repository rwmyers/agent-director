package director

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rwmyers/agent-director/harness"
)

// The reasons a wake did not happen. They are ordinary results, not failures:
// every one of them leaves the news in the state file, where it was going
// anyway, and the director finds it on its next turn.
var (
	// ErrNoWake means this director's host cannot be reached, or no address for
	// it was recorded.
	ErrNoWake = errors.New("this director's host cannot be woken")
	// ErrStaleHost means the recorded address no longer names a live
	// conversation in its harness: the seat it points at has been closed,
	// reassigned, or is no longer recognised.
	ErrStaleHost = errors.New("this director's recorded host is stale")
	// ErrWokenRecently means a wake happened inside the floor.
	ErrWokenRecently = errors.New("this director was woken recently")
	// ErrNothingToWakeFor means the engagement is fine and the director has
	// nothing to do about it.
	ErrNothingToWakeFor = errors.New("nothing about this engagement needs the director")
	// ErrNotAttached means nothing ever recorded a host for this director, so
	// no conversation has ever claimed a seat at it. There is nobody to
	// surprise.
	//
	// It used to cover a second case — a claim older than AttachGrace — and no
	// longer does. That test asked how long ago somebody attached, which says
	// nothing about whether they are still there: a director waiting on
	// long-running engagements does not attach again while it waits. Whether
	// the recorded seat is still real is ErrStaleHost's question now, and it is
	// asked of the harness rather than of a clock.
	ErrNotAttached = errors.New("no conversation is currently attached to this director")
	// ErrOwnRemoval means the news is the recipient's own action.
	ErrOwnRemoval = errors.New("this director removed the engagement itself")
)

// notify rings the director, if it is able to be rung and there is news.
//
// # It is an optimisation and never the record
//
// Everything a wake would say is already in the state file by the time this
// runs. The wake only changes WHEN the director reads it: with one, the news
// arrives now; without one, on the director's next turn, which is exactly
// today's behaviour. So every branch below returns an error that the caller
// discards, and a dropped wake must be indistinguishable from the world before
// this existed. That is not a nicety — herdr's Send tolerates a stalled prompt
// into a busy pane and reports success, so a wake that silently went nowhere is
// the ordinary case rather than the exotic one, and anything that treated a
// successful Send as delivery would be wrong most of the time.
//
// # No daemon
//
// The process that rings the director is the agent's own `director report`. It
// is already a director binary, already holds the root, already has the state
// open, and is running at the moment the news exists. Nothing is left behind
// when it exits.
//
// # The guards, in order
//
// A host that declares no wake, or that recorded no address, stops here for
// nothing. Then the engagement is observed, because only news the director can
// act on is worth a turn. Only then does the floor apply, and only to news that
// is not exempt — see exemptFromFloor. Last of all, once a ring is genuinely
// about to happen, the recorded address is resolved against its harness — see
// addressResolves — because that is a second harness call and it is only worth
// paying for a wake that would otherwise be sent.
//
// The floor used to come first, on the reasoning that it was the cheaper check
// and so belonged before the harness call. That put a rate limit in front of
// the question it was supposed to be rate-limiting: it dropped wakes before
// anything had decided whether this was news worth waking for, and the news it
// dropped was completions and blocked questions, which are the two things a
// director must not miss. Deciding first and metering second costs one
// observation per report that lands inside a floor — see exemptFromFloor for
// what that observation buys.
//
// # What is deliberately not a guard
//
// How long ago the director attached. AttachedAt is written by attach and by
// nothing else, so it measures the age of a claim and not the presence of a
// person: a director waiting on engagements that take hours does not re-attach
// while it waits, and gating the ring on that clock made the failure feed
// itself — an unrung director stays idle, and staying idle is what made it look
// stale. Worse, the engagements most worth ringing about are the long ones,
// which are precisely the ones whose director has been waiting longest.
//
// The hazard the clock was standing in for is real: typing into a pane somebody
// else may now own. It is asked directly instead.
func (d *Director) notify(ctx context.Context, engagementID string) error {
	host := d.State.Host
	if !host.Hosting.Wake {
		return fmt.Errorf("%w: %s", ErrNoWake, host.Describe())
	}
	if host.Ref == "" {
		return fmt.Errorf("%w: no address was recorded for %s", ErrNoWake, host.Harness)
	}
	now := d.now()

	engagement, ok := d.State.Engagements[engagementID]
	if !ok {
		return fmt.Errorf("%w: no engagement %q", ErrNotFound, engagementID)
	}
	task, err := d.Workflow.Task(engagement.Task)
	if err != nil {
		return err
	}

	worth, err := worthWaking(ctx, d, engagement, task)
	if err != nil {
		return err
	}
	if !worth {
		return fmt.Errorf("%w: %s is %s", ErrNothingToWakeFor, engagementID, engagement.Health)
	}

	if !exemptFromFloor(engagement, task) {
		if floor := wakeFloor(task); !host.LastWokenAt.IsZero() && now.Sub(host.LastWokenAt) < floor {
			return fmt.Errorf("%w: %s ago, and the floor for task %q is %s",
				ErrWokenRecently, now.Sub(host.LastWokenAt).Round(time.Second), task.Name, floor)
		}
	}

	adapter, err := d.lookup(host.Harness)
	if err != nil {
		return err
	}
	if err := d.addressResolves(ctx, adapter, host); err != nil {
		return err
	}

	// Recorded whether or not the send worked. A wake that failed is still an
	// attempt, and retrying it on the next report is how a broken address turns
	// into a conversation full of identical lines.
	sendErr := adapter.Send(ctx, harness.SendRequest{Ref: host.Ref, Text: wakeText(engagement)})
	if err := d.mutate(func(state *State) error {
		state.Host.LastWokenAt = now
		return nil
	}); err != nil {
		return err
	}
	return sendErr
}

// addressResolves asks the harness whether the address recorded for this
// director still names a conversation it recognises and something is attached
// to.
//
// This is the guard that replaced a clock. The hazard is narrow and worth
// stating exactly: a wake types text into whatever the recorded ref names, and
// if that seat has been closed and re-used the text lands in somebody else's
// conversation — or, worse for herdr, in the shell a closed agent left behind,
// which would run it. Elapsed time is a poor proxy for that. A pane does not
// become somebody else's because half an hour passed; it becomes somebody
// else's when the harness reassigns it, and the harness is the only thing that
// knows.
//
// So it asks. Get is the same call every observation already makes, and its
// answers map onto the question directly:
//
//   - Not found is the harness saying it has no such conversation. The seat is
//     gone — a closed pane, a deleted transcript — and there is nothing there
//     to type into.
//   - A lifecycle that is not live means the conversation the address named has
//     ended. The address may still parse, but nothing is holding it, which is
//     exactly the state in which a harness hands it to somebody else.
//   - An error is the harness declining to answer. That is not evidence the
//     seat is fine, and a Send through the same unreachable harness would not
//     land anyway, so it stops here and says which failure it was.
//
// LifecycleUnknown passes, because unknown is the harness saying it does not
// know rather than saying no, and reading a refusal into silence is the mistake
// the zero values in this codebase are all chosen to avoid. The cost of letting
// it through is a wake into a conversation that may not be listening, which is
// the ordinary case this whole path is already built to tolerate.
//
// # What it still cannot tell
//
// A harness that re-uses an identifier — a closed pane whose id is handed to a
// new one — answers found and live for what is now a different conversation.
// Nothing recorded about the host distinguishes the two, so this cannot either.
// It is not a regression: the clock could not tell them apart within its grace
// and did not try. Closing it would mean recording something at attach that the
// harness echoes back, which is a change to what a host is.
func (d *Director) addressResolves(ctx context.Context, adapter harness.Adapter, host Host) error {
	observation, err := adapter.Get(ctx, host.Ref)
	if err != nil {
		return fmt.Errorf("%w: %s could not be asked whether %s is still its conversation: %w",
			ErrStaleHost, host.Harness, host.Ref, err)
	}
	if !observation.Found {
		return fmt.Errorf("%w: %s no longer recognises %s, so it is not this director's seat any more",
			ErrStaleHost, host.Harness, host.Ref)
	}
	if !observation.Lifecycle.Live() {
		return fmt.Errorf("%w: %s reports %s is %s, so nothing is sitting at that address",
			ErrStaleHost, host.Harness, host.Ref, observation.Lifecycle)
	}
	return nil
}

// worthWaking decides whether this is news the director has to act on.
//
// Health.NeedsDirector is the standing definition of that and is the main test.
// The one thing it cannot see is an agent that has just reported its terminal
// progress: health only calls that complete once the process is gone, so the
// single most useful message an engagement ever sends — I am finished — would
// otherwise never ring anybody. A terminal report is the agent stating it is
// done, which is exactly what the director is waiting to hear.
//
// # A failed observation is not news, and is not nothing either
//
// An observation that fails is not a stale health left standing: observe
// replaces the verdict with HealthUnknown before it returns the error, and
// unknown does not need the director — so the answer here is no, do not ring.
// Ringing on it would be ringing to say the harness is unreachable, which is a
// change to what unknown means and belongs in health, not in the doorway to a
// wake.
//
// The error still travels rather than being dropped. Swallowing it would leave
// notify reporting ErrNothingToWakeFor — the engagement is fine — when the
// truth is that nobody could look at it, and conflating those two is exactly
// what propagating observation errors was for. Nothing observable changes:
// notify's errors are all reasons a wake did not happen, and every caller
// discards them.
func worthWaking(ctx context.Context, d *Director, engagement *Engagement, task Task) (bool, error) {
	if task.Terminal != "" && engagement.Progress == task.Terminal {
		return true, nil
	}
	// Observing costs a harness call, so it happens last and only for an
	// engagement that has not already answered the question.
	if err := d.observe(ctx, engagement); err != nil {
		return false, err
	}
	return engagement.Health.NeedsDirector(), nil
}

// exemptFromFloor reports whether this news is too important to meter.
//
// The floor exists for a fleet's ordinary chatter — several engagements going
// quiet or stalling at once, none of which is urgent and all of which a
// director will see on its next turn. Two kinds of news are not that.
//
// A terminal report is the agent saying it has finished. It is the single most
// useful message an engagement ever sends, it is said exactly once, and the
// director is waiting on it in order to start the next thing.
//
// A blocked engagement is worse. The agent is *stopped* — on its own question,
// or on something the harness can see it waiting for — and it will never report
// again. The ring it does not get is not a ring that arrives late; it is the
// only signal there was, and the engagement sits there until somebody looks by
// hand.
//
// Neither can arrive in a flood: an engagement finishes once and blocks on one
// question at a time, so exempting them does not reopen the hazard the floor
// was built for. What is still metered is stalls, abandonments, and completions
// of tasks that declare no finish line — the news that repeats.
//
// # It costs an observation
//
// Health is only known after observe, so asking this at all means the harness
// call has already been paid for even when the floor then drops the wake. That
// is a real cost: inside a floor, every report now observes where it used to
// return early, which is at most one Get per engagement per ring — the same
// call `director status` makes for each row, once, per ring.
//
// It is not avoidable while the answer stays honest. Terminal progress and a
// pending ask are both free to check, but the third way to be blocked is the
// harness reporting the agent stopped waiting on a human, and nothing but the
// harness knows that. Metering before the observation would buy the call back
// by silently narrowing "blocked" to "blocked in a way we could see for free",
// which is the class of engagement least able to complain about being missed.
func exemptFromFloor(engagement *Engagement, task Task) bool {
	if task.Terminal != "" && engagement.Progress == task.Terminal {
		return true
	}
	return engagement.Health == HealthBlocked
}

// wakeFloor is the shortest gap between two wakes.
//
// The task's own heartbeat is the natural unit: it is what the workflow said
// this agent's ordinary rate of speech is, so waking more often than that is
// waking faster than the work produces news. A task with no heartbeat has
// nothing to measure against, and the stall threshold — which is what silence
// is judged by everywhere else — stands in.
//
// It is per-director rather than per-engagement, because the noise it exists to
// stop is a fleet speaking at once and not one engagement speaking twice. That
// is also why it has to be applied to a decision rather than in place of one:
// any engagement's ring arms it for every other engagement, so whatever it
// drops, it drops from somebody who was not the one making the noise. What it
// does not apply to is exemptFromFloor's business.
func wakeFloor(task Task) time.Duration {
	if task.ReportOn.Every > 0 {
		return task.ReportOn.Every
	}
	return task.StallThreshold()
}

// wakeText is what lands in the director's conversation.
//
// It says which engagement and what happened, and then sends the director to
// status rather than trying to be the report itself. The state file is the
// record; this line only says to go and read it, so a wake that is garbled,
// truncated by a harness, or delivered twice costs nothing.
func wakeText(engagement *Engagement) string {
	what := engagement.Progress
	if engagement.Health == HealthBlocked {
		what = "blocked"
	}
	if what == "" {
		what = string(engagement.Health)
	}
	return fmt.Sprintf("Your engagement %s (%s) is %s. Run `director status --unhealthy` and deal with it.",
		engagement.ID, engagement.Title, what)
}

// NotifyRemoved tells an attached director that somebody else has taken
// engagements out of its record.
//
// # Why this exists at all
//
// Every other wake in this file is an engagement reporting news about itself,
// and the director's picture of the fleet stays true whether the wake lands or
// not. A removal is the opposite: the record changed underneath a conversation
// that is holding its own copy of it. A director that is not told does not
// merely learn late — it goes on believing in a row that has gone, and the
// first sign is a "not found" from `director read` on an engagement it can
// still see in its own scrollback.
//
// So unlike notify, this is not purely an optimisation over the record. It is
// still not the record — the state file remains the truth and a lost ring
// costs only staleness — but the staleness it prevents is the kind that makes
// the director act wrongly rather than late.
//
// # One ring for the whole invocation
//
// It takes the batch rather than one result because `director remove a b c` is
// one human action. Ringing three times would be three lines typed into a live
// conversation for a single decision, which is the failure this is under
// instructions not to reproduce.
//
// # No floor, and it does not arm one either
//
// notify holds a floor because a fleet reporting in unison would otherwise
// type a dozen lines into the conversation. Removals are not a fleet: they are
// a person at a keyboard, at human rate, and a floor here would silently drop
// exactly the message whose loss this exists to prevent.
//
// This used to record the wake anyway, on the reasoning that it is a line typed
// into the conversation and the floor for everything else should count it. That
// reads the floor as a budget on lines, and it is not one — it is the task's own
// heartbeat, the rate at which that engagement's work produces news. A removal
// has no task and no heartbeat, so there is no interval it could arm that would
// mean anything; the one it armed was whichever task happened to report next.
//
// The asymmetry is the rest of it. An action that exempts itself from a limit
// but still charges it spends a budget it does not pay into, and the traffic
// only ever flows one way: a person clearing three finished rows at the console
// could silence a heartbeat's worth of stalls across the whole fleet, while no
// number of stalls can ever silence a removal. Removals are human-rate by
// construction — the same fact that earns the exemption — so the cost of not
// arming is two lines back to back in the worst case, which is what a person
// doing two things gets everywhere else.
//
// LastWokenAt keeps its meaning under this: when an engagement last rang this
// director, which is exactly what the floor is measured against.
//
// # What is deliberately not done
//
// Nothing is written down for a director that cannot be woken. There is no
// inbox in the state file to write it to, and the removed rows are gone from
// the one place a director looks. Saying so to the person at the console is
// therefore the honest end of the road — see the caller.
func (d *Director) NotifyRemoved(ctx context.Context, removed []*RemoveResult) error {
	if len(removed) == 0 {
		return fmt.Errorf("%w: nothing was removed", ErrNothingToWakeFor)
	}
	host := d.State.Host

	// First, and before anything that could be reported to a person. A
	// director runs `director remove` itself, routinely, and telling a
	// conversation what it just did is pure noise — as is telling the person
	// that the noise could not be delivered.
	if d.selfInitiated() {
		return fmt.Errorf("%w: %s", ErrOwnRemoval, host.Describe())
	}

	if !host.Known() {
		return fmt.Errorf("%w: no host was recorded", ErrNotAttached)
	}

	// Attached, and cannot be reached. Separated from the two above because
	// this is the only one worth saying out loud: somebody is sitting there,
	// holding a picture that is now wrong, and nothing will correct it until
	// they look.
	if !host.Hosting.Wake {
		return fmt.Errorf("%w: %s", ErrNoWake, host.Describe())
	}
	if host.Ref == "" {
		return fmt.Errorf("%w: no address was recorded for %s", ErrNoWake, host.Harness)
	}

	adapter, err := d.lookup(host.Harness)
	if err != nil {
		return err
	}

	// Same question notify asks, for the same reason and at the same point: the
	// resolve costs a harness call, and by here a line is about to be typed
	// into whatever this address names. An address that no longer resolves is
	// silence rather than something to report — the seat is empty, so nobody is
	// sitting there holding a picture that has gone wrong.
	if err := d.addressResolves(ctx, adapter, host); err != nil {
		return err
	}

	// Not recorded: see "No floor, and it does not arm one either" above. There
	// is no retry to guard against either — one `director remove` is one call,
	// and the CLI reports a failed ring to the person rather than trying again.
	return adapter.Send(ctx, harness.SendRequest{Ref: host.Ref, Text: removalText(removed)})
}

// selfInitiated reports whether the command running now is running inside the
// conversation the director itself occupies.
//
// It compares where this process is, worked out fresh, against the address the
// director recorded when it attached. Both must name the same harness and the
// same non-empty conversation. An empty address is never a match: a host that
// recorded no address — Claude Code with no session id — would otherwise make
// every other Claude Code conversation on the machine look like this one.
//
// A false yes costs a ring that should have happened; a false no costs a
// director being told about its own action. Requiring an exact address on both
// sides is what keeps either from happening by accident rather than by a
// harness genuinely being unable to tell two conversations apart.
func (d *Director) selfInitiated() bool {
	host := d.State.Host
	if !host.Known() || host.Ref == "" {
		return false
	}
	here := d.locateHost()
	return here.Known() && here.Harness == host.Harness && here.Ref == host.Ref
}

// namedInRemoval is how many engagements a ring spells out before it stops.
//
// Enough that the ordinary case — one or two rows cleared — is named in full,
// and few enough that clearing a finished batch of twenty does not paste
// twenty identifiers into somebody's conversation. The count carries the rest,
// and status carries the truth.
const namedInRemoval = 3

// removalText is what lands in the director's conversation.
//
// It has to do three things its cousin wakeText does not. It must say the
// removal came from outside, because the director also removes engagements and
// would otherwise read this as an echo of its own command. It must name what
// went, because the rows are already gone and status cannot show them. And it
// must say the fleet is not what the director last saw, because the failure
// being prevented is the director acting on a row from its own scrollback.
//
// As with wakeText it points at the record rather than being it, so a line
// that is truncated or delivered twice costs nothing.
func removalText(removed []*RemoveResult) string {
	named := make([]string, 0, namedInRemoval)
	for _, result := range removed {
		if len(named) == namedInRemoval {
			break
		}
		named = append(named, fmt.Sprintf("%s (%s)", result.EngagementID, result.Title))
	}
	list := strings.Join(named, ", ")
	if rest := len(removed) - len(named); rest > 0 {
		list = fmt.Sprintf("%s, and %d more", list, rest)
	}

	noun := "engagement"
	if len(removed) != 1 {
		noun = "engagements"
	}
	return fmt.Sprintf(
		"Somebody other than you removed %d %s from your record: %s. "+
			"They are gone from your fleet and can no longer be read, answered or stopped. "+
			"Run `director status` before acting on anything you remember holding.",
		len(removed), noun, list)
}
