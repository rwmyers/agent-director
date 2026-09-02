package director

import (
	"context"
	"errors"
	"fmt"
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
