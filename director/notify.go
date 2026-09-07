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
	// ErrStaleHost means the address is old enough that nobody is likely to be
	// sitting at it.
	ErrStaleHost = errors.New("this director's recorded host is stale")
	// ErrWokenRecently means a wake happened inside the floor.
	ErrWokenRecently = errors.New("this director was woken recently")
	// ErrNothingToWakeFor means the engagement is fine and the director has
	// nothing to do about it.
	ErrNothingToWakeFor = errors.New("nothing about this engagement needs the director")
	// ErrNotAttached means no conversation is sitting at this director: nothing
	// recorded a host, or the claim is old enough that whoever made it has
	// gone. There is nobody to surprise.
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
// # The guards, in the order they are cheap
//
// A host that declares no wake, or that recorded no address, stops here for
// nothing. A stale claim stops before a harness call, because typing into a
// conversation somebody walked away from half an hour ago is at best noise and
// at worst somebody else's screen — the address belongs to whoever attached
// last, and an earlier conversation falls back to its own turn. The floor stops
// a fleet reporting in unison from typing a dozen lines into a live
// conversation. Only then is the engagement observed, because that costs a
// harness call, and only news the director can act on is worth a turn.
func (d *Director) notify(ctx context.Context, engagementID string) error {
	host := d.State.Host
	if !host.Hosting.Wake {
		return fmt.Errorf("%w: %s", ErrNoWake, host.Describe())
	}
	if host.Ref == "" {
		return fmt.Errorf("%w: no address was recorded for %s", ErrNoWake, host.Harness)
	}
	now := d.now()
	if d.State.AttachedAt.IsZero() || now.Sub(d.State.AttachedAt) > AttachGrace {
		return fmt.Errorf("%w: last attached %s ago, and the grace is %s",
			ErrStaleHost, now.Sub(d.State.AttachedAt).Round(time.Second), AttachGrace)
	}

	engagement, ok := d.State.Engagements[engagementID]
	if !ok {
		return fmt.Errorf("%w: no engagement %q", ErrNotFound, engagementID)
	}
	task, err := d.Workflow.Task(engagement.Task)
	if err != nil {
		return err
	}
	if floor := wakeFloor(task); !host.LastWokenAt.IsZero() && now.Sub(host.LastWokenAt) < floor {
		return fmt.Errorf("%w: %s ago, and the floor for task %q is %s",
			ErrWokenRecently, now.Sub(host.LastWokenAt).Round(time.Second), task.Name, floor)
	}

	if !worthWaking(ctx, d, engagement, task) {
		return fmt.Errorf("%w: %s is %s", ErrNothingToWakeFor, engagementID, engagement.Health)
	}

	adapter, err := d.lookup(host.Harness)
	if err != nil {
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

// worthWaking decides whether this is news the director has to act on.
//
// Health.NeedsDirector is the standing definition of that and is the main test.
// The one thing it cannot see is an agent that has just reported its terminal
// progress: health only calls that complete once the process is gone, so the
// single most useful message an engagement ever sends — I am finished — would
// otherwise never ring anybody. A terminal report is the agent stating it is
// done, which is exactly what the director is waiting to hear.
func worthWaking(ctx context.Context, d *Director, engagement *Engagement, task Task) bool {
	if task.Terminal != "" && engagement.Progress == task.Terminal {
		return true
	}
	// Observing costs a harness call, so it happens last and only for an
	// engagement that has not already answered the question.
	d.observe(ctx, engagement)
	return engagement.Health.NeedsDirector()
}

// wakeFloor is the shortest gap between two wakes.
//
// The task's own heartbeat is the natural unit: it is what the workflow said
// this agent's ordinary rate of speech is, so waking more often than that is
// waking faster than the work produces news. A task with no heartbeat has
// nothing to measure against, and the stall threshold — which is what silence
// is judged by everywhere else — stands in.
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
// # No floor
//
// notify holds a floor because a fleet reporting in unison would otherwise
// type a dozen lines into the conversation. Removals are not a fleet: they are
// a person at a keyboard, at human rate, and a floor here would silently drop
// exactly the message whose loss this exists to prevent. The wake is still
// recorded, because it is a line typed into the conversation and the floor for
// everything else should count it.
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

	now := d.now()
	if !host.Known() {
		return fmt.Errorf("%w: no host was recorded", ErrNotAttached)
	}
	if d.State.AttachedAt.IsZero() || now.Sub(d.State.AttachedAt) > AttachGrace {
		return fmt.Errorf("%w: last attached %s ago, and the grace is %s",
			ErrNotAttached, now.Sub(d.State.AttachedAt).Round(time.Second), AttachGrace)
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

	// Recorded whether or not the send worked, on the same terms as notify: an
	// attempt is an attempt, and a broken address that is retried is how a
	// conversation fills up with identical lines.
	sendErr := adapter.Send(ctx, harness.SendRequest{Ref: host.Ref, Text: removalText(removed)})
	if err := d.mutate(func(state *State) error {
		state.Host.LastWokenAt = now
		return nil
	}); err != nil {
		return err
	}
	return sendErr
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
