package director

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/rwmyers/agent-director/harness"
)

// RemoveOptions controls how one engagement is dropped from a director's
// record.
type RemoveOptions struct {
	// Stop ends the engagement before forgetting it.
	Stop bool
	// Force forgets it even while it is live, orphaning the agent.
	Force bool
}

// RemoveResult reports what was forgotten, so the caller can say it back to
// whoever asked for it. Removal is not reversible and the record is gone by the
// time this is read, so it carries everything the row used to show.
type RemoveResult struct {
	EngagementID string   `json:"engagement"`
	Title        string   `json:"title"`
	Task         string   `json:"task"`
	Harness      string   `json:"harness"`
	Health       Health   `json:"health"`
	Progress     string   `json:"progress,omitempty"`
	Stopped      bool     `json:"stopped,omitempty"`
	Orphaned     bool     `json:"orphaned,omitempty"`
	Asks         []string `json:"asks,omitempty"`

	// Disposal is what became of the slot the conversation occupied in its
	// harness, and DisposalReason says why for anything but a plain close.
	// Both are empty when the harness has no such slot, so a harness that has
	// never heard of disposal reports nothing rather than reporting a nothing.
	Disposal       Disposal `json:"disposal,omitempty"`
	DisposalReason string   `json:"disposal_reason,omitempty"`
}

// Disposal says what became of the slot an engagement's conversation occupied
// in its harness — the pane, window or tab the spawn allocated.
//
// It is reported beside what happened to the row because they are two different
// things that a removal does, and the whole reason this exists is that one of
// them used to happen silently and the other did not.
type Disposal string

const (
	// DisposalNone means there was no slot to reclaim: the harness declares
	// none, or the spawn never got one. It is the empty value, so a harness
	// that has not heard of disposal says nothing at all rather than saying
	// nothing happened.
	DisposalNone Disposal = ""
	// DisposalClosed means the slot was given back.
	DisposalClosed Disposal = "closed"
	// DisposalKept means it was deliberately left alone, because closing it
	// could have destroyed something still using it. The reason says which.
	DisposalKept Disposal = "kept"
	// DisposalFailed means closing it was attempted and did not work. The row
	// is gone regardless — see Remove for why that is the right way round.
	DisposalFailed Disposal = "failed"
)

// ResolveEngagement finds the one engagement a fragment names.
//
// A director reads its fleet as a table and refers back to a row by whatever is
// shortest — a truncated identifier, or a few words of the title. Every other
// command matches identifiers exactly, so without this the director has to
// expand the fragment itself, and the failure mode of getting that wrong is
// acting on somebody else's engagement.
//
// An exact identifier always wins outright. Otherwise a fragment matches on
// either the identifier or the title, case-insensitively, and matching more
// than one is an error rather than a choice: there is no ordering between two
// candidates that makes picking one of them safe.
func (d *Director) ResolveEngagement(fragment string) (*Engagement, error) {
	fragment = strings.TrimSpace(fragment)
	if fragment == "" {
		return nil, fmt.Errorf("%w: no engagement named", ErrNotFound)
	}
	if engagement, ok := d.State.Engagements[fragment]; ok {
		return engagement, nil
	}

	needle := strings.ToLower(fragment)
	var matches []*Engagement
	for _, id := range d.State.EngagementIDs() {
		engagement := d.State.Engagements[id]
		if strings.Contains(strings.ToLower(id), needle) ||
			strings.Contains(strings.ToLower(engagement.Title), needle) {
			matches = append(matches, engagement)
		}
	}

	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return nil, fmt.Errorf("%w: nothing here matches %q\n%s",
			ErrNotFound, fragment, describeFleet(d.State))
	default:
		return nil, ambiguousError(fragment, matches)
	}
}

func ambiguousError(fragment string, matches []*Engagement) error {
	var lines []string
	for _, engagement := range matches {
		lines = append(lines, fmt.Sprintf("  %s  %s", engagement.ID, engagement.Title))
	}
	return fmt.Errorf("%q matches %d engagements:\n%s\n\nName one of them exactly",
		fragment, len(matches), strings.Join(lines, "\n"))
}

func describeFleet(state *State) string {
	ids := state.EngagementIDs()
	if len(ids) == 0 {
		return "This director holds no engagements."
	}
	var out strings.Builder
	out.WriteString("This director holds:\n")
	for _, id := range ids {
		fmt.Fprintf(&out, "  %s  %s\n", id, state.Engagements[id].Title)
	}
	return strings.TrimRight(out.String(), "\n")
}

// Remove drops one engagement from this director's record.
//
// The record is the only thing mapping an engagement back to its harness — the
// same reason Retire refuses while work is live, at the granularity of one row.
// Forgetting a live engagement leaves an agent running, costing money, and
// unreachable by anything: it cannot be listed, read, answered or stopped once
// the only handle to it is gone. So this refuses by default and says how to
// proceed.
//
// What it removes is the director's memory of the work, not the work. The
// transcript, the log, the branch and everything the agent wrote are untouched;
// nothing outside the state file knows this happened.
//
// The one exception is the slot the conversation occupied in its harness — a
// herdr pane — and it is an exception because that slot is not the agent's
// work either. director caused it to be allocated and nothing else ever
// reclaims it, so removing a row without it strands a piece of somebody's
// interface for good. Harnesses that have no such slot are unaffected: see
// reclaimSlot, and harness.Disposer for what a harness has to declare.
func (d *Director) Remove(ctx context.Context, fragment string, opts RemoveOptions) (*RemoveResult, error) {
	engagement, err := d.ResolveEngagement(fragment)
	if err != nil {
		return nil, err
	}
	// Observe rather than trust the stored row: lifecycle is never persisted,
	// and refusing on a cached one would either block a removal that is fine or
	// permit the orphaning this exists to prevent.
	if err := d.observe(ctx, engagement); err != nil && !opts.Force {
		return nil, err
	}

	result := &RemoveResult{
		EngagementID: engagement.ID,
		Title:        engagement.Title,
		Task:         engagement.Task,
		Harness:      engagement.Harness,
		Health:       engagement.Health,
		Progress:     engagement.Progress,
	}

	if engagement.Lifecycle.Live() {
		switch {
		case opts.Stop:
			if err := d.Stop(ctx, engagement.ID, harness.StopEnd); err != nil {
				// Best-effort past this point, as retire is: a harness that has
				// already lost the conversation must not block a removal that
				// was explicitly asked for.
				result.Orphaned = true
			} else {
				result.Stopped = true
			}
		case opts.Force:
			result.Orphaned = true
		default:
			return nil, liveEngagementError(engagement)
		}
	}

	err = d.mutate(func(state *State) error {
		if _, ok := state.Engagements[engagement.ID]; !ok {
			return fmt.Errorf("%w: no engagement %q", ErrNotFound, engagement.ID)
		}
		delete(state.Engagements, engagement.ID)
		// Its questions go with it. An ask outliving the engagement that raised
		// it is answerable by nobody and belongs to a row that no longer exists.
		for askID, ask := range state.Asks {
			if ask.Engagement == engagement.ID {
				delete(state.Asks, askID)
				result.Asks = append(result.Asks, askID)
			}
		}
		sort.Strings(result.Asks)
		return nil
	})
	if err != nil {
		return nil, err
	}

	d.reclaimSlot(ctx, engagement, opts, result)
	return result, nil
}

// reclaimSlot gives back the slot this engagement's conversation was occupying,
// once the row is gone.
//
// # Why after the record, and why a failure here is not a failure
//
// The row is what was asked to be got rid of; the slot is cleanup director owes
// because it allocated it. Closing first and then failing to write the state
// file would leave the engagement listed with its conversation destroyed, which
// is the one ordering that loses something a person cannot get back.
//
// For the same reason a slot that will not close does not fail the removal. The
// row has gone and nothing restores it, so an error here would report a removal
// that did happen as one that did not, and the obvious response — run it again
// — then answers "nothing matches". Retrying cannot help, because the thing
// that would need retrying is not in the record any more. So the removal
// succeeds, DisposalFailed says what was left behind, and the caller is told
// which slot to close by hand. That is one thing to tidy rather than one thing
// to tidy plus a command whose exit code disagrees with what it did.
//
// This is reached from Remove and from nowhere else. Retire deletes a whole
// director's record and does not close slots: it is routinely run from a new
// conversation to clear up other directors, and those directors' slots are not
// the invoker's to destroy. The capability is opted into here, by the command
// that owns the panes it allocated, rather than inherited by anything that
// deletes state.
func (d *Director) reclaimSlot(ctx context.Context, engagement *Engagement, opts RemoveOptions, result *RemoveResult) {
	if engagement.Ref == "" {
		return // the spawn never got as far as a slot
	}
	adapter, err := d.lookupFor(engagement)
	if err != nil {
		// No adapter to ask. The same silence as one that declares nothing:
		// the record is already gone and there is nobody to tell.
		return
	}
	disposer, ok := harness.DisposerFor(adapter)
	if !ok {
		return // declares no slot, so removal behaves exactly as it always did
	}

	if why := keepSlot(d, engagement, opts, result); why != "" {
		result.Disposal = DisposalKept
		result.DisposalReason = why
		return
	}

	if err := disposer.Dispose(ctx, harness.DisposeRequest{Ref: engagement.Ref}); err != nil {
		result.Disposal = DisposalFailed
		result.DisposalReason = err.Error()
		return
	}
	result.Disposal = DisposalClosed
}

// keepSlot is every reason not to close a slot, and returns the one that
// applies or an empty string when there is none.
//
// This is the whole safety argument for the feature. Closing a slot out from
// under a living agent destroys work nothing can recover, so the question is
// not "does this look finished" but "has something actually confirmed there is
// nothing left in it". Anything short of that answer keeps the slot: an untidy
// session costs somebody a keystroke, and the other mistake costs an
// engagement.
func keepSlot(d *Director, engagement *Engagement, opts RemoveOptions, result *RemoveResult) string {
	// First, and unconditionally. A director very often runs inside the same
	// harness it dispatches to, and a removal that reached its own seat would
	// kill the session issuing the command — taking the director, its fleet's
	// only record of what it was doing, and the person's terminal with it.
	// Nothing below is allowed to overrule this.
	if d.occupiesSlot(engagement) {
		return "this is the conversation the director itself is running in"
	}
	// --force exists to forget an engagement while deliberately leaving its
	// agent running and unreachable. The slot is where it is running, so
	// closing it would destroy the exact thing the flag is for.
	if opts.Force {
		return "--force left the agent running, and its slot is where it is running"
	}
	// A stop that did not take. The agent may well still be alive in there.
	if result.Orphaned {
		return "the stop did not succeed, so the agent may still be running"
	}
	// A stop that did. This is confirmation, and it is fresher than any
	// observation: the harness was asked to end the conversation and said it
	// had.
	if result.Stopped {
		return ""
	}
	// Otherwise the only acceptable evidence is the lifecycle observed at the
	// top of Remove, moments ago: done, which is what makes an engagement
	// complete or abandoned. Unknown is not a confirmation of anything, and
	// live never reaches here without one of the flags above.
	if engagement.Lifecycle != harness.LifecycleDone {
		return fmt.Sprintf("%s did not confirm the conversation had finished (%s)",
			engagement.Harness, engagement.Lifecycle)
	}
	return ""
}

// occupiesSlot reports whether an engagement names the conversation this
// director is itself running in.
//
// Both sources are consulted because either can be the only one that knows. The
// recorded host is written when a director attaches and survives into commands
// run from anywhere; asking the adapter now catches a conversation that never
// attached, or one that has moved since. Neither is asked to be authoritative —
// either saying yes is enough, because the cost of a false yes is a slot left
// open and the cost of a false no is the session dying.
func (d *Director) occupiesSlot(engagement *Engagement) bool {
	if engagement.Ref == "" {
		return false
	}
	if host := d.State.Host; host.Known() && host.Harness == engagement.Harness && host.Ref == engagement.Ref {
		return true
	}
	adapter, err := d.lookup(engagement.Harness)
	if err != nil {
		return false
	}
	locator, ok := adapter.(harness.SelfLocator)
	if !ok {
		return false
	}
	ref, inside := locator.Locate()
	return inside && ref == engagement.Ref
}

func liveEngagementError(engagement *Engagement) error {
	return fmt.Errorf(
		"engagement %s (%s) is %s and still %s.\n\n"+
			"Forgetting it now would leave the agent running with nothing able to reach it — this record is the\n"+
			"only thing that maps it back to its harness, so it could not be listed, read, answered or stopped\n"+
			"afterwards. Either stop it first:\n\n"+
			"    director remove %s --stop\n\n"+
			"or, if it should keep running with no director tracking it, orphan it deliberately:\n\n"+
			"    director remove %s --force",
		engagement.ID, engagement.Title, engagement.Health, engagement.Lifecycle,
		engagement.ID, engagement.ID)
}
