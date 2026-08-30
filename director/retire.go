package director

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/rwmyers/agent-director/harness"
)

// RetireOptions controls how a director is removed.
type RetireOptions struct {
	// Stop ends every live engagement before removing the record.
	Stop bool
	// Force removes the record even with engagements still running, orphaning
	// them.
	Force bool
}

// RetireResult reports what happened.
type RetireResult struct {
	DirectorID string   `json:"director"`
	Name       string   `json:"name"`
	Stopped    []string `json:"stopped,omitempty"`
	Orphaned   []string `json:"orphaned,omitempty"`
	Path       string   `json:"path"`
}

// Retire removes this director and its record of what it was running.
//
// The record is the only thing that maps an engagement back to its harness. A
// live agent whose director is gone keeps running, keeps costing money, and can
// no longer be listed, read, answered or stopped by anything — the handle to it
// existed in exactly one file. So this refuses by default while anything is
// still alive, and says what and how to proceed.
//
// The refusal is the whole point. Removing a state file is trivial to do by
// hand and impossible to undo, and the consequence is invisible: the fleet does
// not stop, it just stops being reachable.
func (d *Director) Retire(ctx context.Context, opts RetireOptions) (*RetireResult, error) {
	result := &RetireResult{
		DirectorID: d.State.DirectorID,
		Name:       d.State.Name,
		Path:       d.State.Path,
	}

	engagements, err := d.Status(ctx)
	if err != nil {
		return nil, err
	}
	var live []*Engagement
	for _, engagement := range engagements {
		if engagement.Lifecycle.Live() {
			live = append(live, engagement)
		}
	}

	if len(live) > 0 && !opts.Stop && !opts.Force {
		return nil, runningWorkError(d.State.DirectorID, d.State.Name, live)
	}

	for _, engagement := range live {
		if !opts.Stop {
			result.Orphaned = append(result.Orphaned, engagement.ID)
			continue
		}
		if err := d.Stop(ctx, engagement.ID, harness.StopEnd); err != nil {
			// Best-effort past this point: a harness that has already lost the
			// conversation must not block a retirement explicitly asked for.
			result.Orphaned = append(result.Orphaned, engagement.ID)
			continue
		}
		result.Stopped = append(result.Stopped, engagement.ID)
	}

	if err := removeState(d.State.Path); err != nil {
		return nil, err
	}
	return result, nil
}

func runningWorkError(id, name string, live []*Engagement) error {
	var lines []string
	for _, engagement := range live {
		lines = append(lines, fmt.Sprintf("  %s  %s  %s", engagement.ID, engagement.Lifecycle, engagement.Title))
	}
	return fmt.Errorf(
		"director %s (%s) still has %d running engagement(s):\n%s\n\n"+
			"Retiring it now would leave them running with nothing able to reach them — this record is the only\n"+
			"thing that maps them back to their harness. Either stop them first:\n\n"+
			"    director retire %s --stop\n\n"+
			"or, if you know they should keep running without a director, orphan them deliberately:\n\n"+
			"    director retire %s --force",
		id, name, len(live), strings.Join(lines, "\n"), id, id)
}

func removeState(path string) error {
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("removing %s: %w", path, err)
	}
	// A lock left behind by a crashed writer would otherwise outlive the
	// director it was protecting.
	_ = os.Remove(path + ".lock")
	return nil
}

// RetireByID removes a director named by identifier.
//
// It goes through the ordinary open path so that a stale identifier gets the
// same explanation it would anywhere else. A director whose workflow has been
// deleted cannot be opened at all — and that is exactly the director somebody
// wants to remove — so its record can still be removed, but only with --force:
// without a workflow there is no way to observe its engagements, and quietly
// removing a record while unable to check whether anything is running is the
// one thing this command exists to prevent.
func RetireByID(ctx context.Context, roots Roots, id string, opts RetireOptions, clock Clock) (*RetireResult, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: no director named to retire", ErrNotFound)
	}

	d, openErr := Open(roots, id, clock)
	if openErr == nil {
		return d.Retire(ctx, opts)
	}

	path := statePath(roots.Primary, id)
	state, loadErr := LoadState(path)
	if loadErr != nil {
		// The record itself is unreadable or absent, so the open error is the
		// useful one — it explains a stale id and lists what does exist.
		return nil, openErr
	}

	if !opts.Force {
		return nil, fmt.Errorf(
			"director %s (%s) cannot be opened, so its engagements cannot be checked:\n  %v\n\n"+
				"It holds %d engagement record(s). If you are sure nothing of its is still running:\n\n"+
				"    director retire %s --force",
			state.DirectorID, state.Name, openErr, len(state.Engagements), id)
	}

	result := &RetireResult{DirectorID: state.DirectorID, Name: state.Name, Path: path}
	result.Orphaned = append(result.Orphaned, state.EngagementIDs()...)
	if err := removeState(path); err != nil {
		return nil, err
	}
	return result, nil
}
