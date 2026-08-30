package director

import (
	"context"
	"time"

	"github.com/rwmyers/agent-director/harness"
)

// Transition is one change in an engagement's state, as a consumer sees it.
//
// This is the shape a monitoring UI, a status bar or a shell script consumes.
// It is a flat record on purpose: something tailing a pipe should be able to
// act on a line without holding any prior state of its own.
type Transition struct {
	At         time.Time         `json:"at"`
	Engagement string            `json:"engagement"`
	Title      string            `json:"title"`
	Task       string            `json:"task"`
	Health     Health            `json:"health"`
	Lifecycle  harness.Lifecycle `json:"lifecycle"`
	Progress   string            `json:"progress,omitempty"`
	// Was is the previous health, so a consumer can tell an engagement that has
	// just gone wrong from one that has been wrong for an hour.
	Was Health `json:"was,omitempty"`
	// Message is whatever the agent last said, when it changed.
	Message string `json:"message,omitempty"`
	// New marks an engagement seen for the first time on this feed.
	New bool `json:"new,omitempty"`
}

// WatchOptions configures the transition feed.
type WatchOptions struct {
	// Interval is how often the harnesses are polled.
	Interval time.Duration
	// IncludeInitial emits the current state of every engagement before
	// watching for changes, so a consumer that starts late is not looking at a
	// blank screen until something happens to move.
	IncludeInitial bool
}

// DefaultWatchInterval balances a responsive feed against how often the
// harnesses are asked. Every poll is a directory scan and a stat per
// engagement, which is cheap but not free.
const DefaultWatchInterval = 5 * time.Second

// Watch emits a transition whenever an engagement's health, lifecycle or
// progress changes, until the context is cancelled.
//
// It emits only on change. A consumer that received a line every interval
// regardless would have to dedupe, and a status bar that redraws every five
// seconds whether or not anything happened is the failure mode this is meant to
// avoid — one that is invisible from a director's point of view, because a
// director never runs this command.
//
// Polling rather than pushing, because neither harness offers a usable event
// stream today: Claude Code's push channel is per-event hook processes, and
// herdr's socket is frequently not running. An adapter that grows a real event
// stream can make this push-driven later without changing what a consumer sees.
func (d *Director) Watch(ctx context.Context, opts WatchOptions) (<-chan Transition, error) {
	interval := opts.Interval
	if interval <= 0 {
		interval = DefaultWatchInterval
	}

	out := make(chan Transition)
	go func() {
		defer close(out)

		type seen struct {
			health    Health
			lifecycle harness.Lifecycle
			progress  string
		}
		previous := map[string]seen{}
		first := true

		emit := func(transition Transition) bool {
			select {
			case out <- transition:
				return true
			case <-ctx.Done():
				return false
			}
		}

		for {
			// Reload from disk each pass: an agent's report or another
			// director's action lands in the file, not in this process, and a
			// feed that only saw its own writes would miss almost everything.
			if fresh, err := LoadState(d.State.Path); err == nil {
				d.State = fresh
			}

			engagements, err := d.Status(ctx)
			if err == nil {
				for _, engagement := range engagements {
					current := seen{engagement.Health, engagement.Lifecycle, engagement.Progress}
					was, known := previous[engagement.ID]
					if known && was == current {
						continue
					}
					if first && !opts.IncludeInitial {
						previous[engagement.ID] = current
						continue
					}
					previous[engagement.ID] = current

					transition := Transition{
						At:         d.now(),
						Engagement: engagement.ID,
						Title:      engagement.Title,
						Task:       engagement.Task,
						Health:     engagement.Health,
						Lifecycle:  engagement.Lifecycle,
						Progress:   engagement.Progress,
						Message:    engagement.LastMessage,
						New:        !known,
					}
					if known {
						transition.Was = was.health
					}
					if !emit(transition) {
						return
					}
				}
			}
			first = false

			select {
			case <-ctx.Done():
				return
			case <-time.After(interval):
			}
		}
	}()
	return out, nil
}
