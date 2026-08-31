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
}

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
func (d *Director) Remove(ctx context.Context, fragment string, opts RemoveOptions) (*RemoveResult, error) {
	engagement, err := d.ResolveEngagement(fragment)
	if err != nil {
		return nil, err
	}
	// Observe rather than trust the stored row: lifecycle is never persisted,
	// and refusing on a cached one would either block a removal that is fine or
	// permit the orphaning this exists to prevent.
	d.observe(ctx, engagement)

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
	return result, nil
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
