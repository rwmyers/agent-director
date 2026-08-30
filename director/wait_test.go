package director

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rwmyers/agent-director/harness"
)

// waitResult carries what a Wait running in the background returned, so a test
// can assert both that it has not returned yet and what it returned when it
// finally did.
type waitResult struct {
	transition Transition
	err        error
}

func startWait(d *Director, opts WaitOptions) <-chan waitResult {
	done := make(chan waitResult, 1)
	go func() {
		transition, err := d.Wait(context.Background(), opts)
		done <- waitResult{transition, err}
	}()
	return done
}

func TestWait(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

	working := func() harness.Observation {
		return harness.Observation{
			Found: true, Lifecycle: harness.LifecycleWorking, LastActivityAt: now,
		}
	}

	t.Run("a matching health returns the transition and a health that was not asked for does not", func(t *testing.T) {
		t.Parallel()
		adapter := &fakeAdapter{name: "fake", observation: working()}
		d := newTestDirector(t, adapter, now)
		if _, err := d.Spawn(context.Background(), SpawnOptions{Task: "investigate", Brief: "look"}); err != nil {
			t.Fatalf("Spawn() = %v, want no error", err)
		}

		done := startWait(d, WaitOptions{Until: []Health{HealthAbandoned}, Interval: 5 * time.Millisecond})

		// The engagement is healthy, which is not what was asked for. Waking
		// here would defeat the point: a supervisor would spin on every
		// ordinary progress change.
		select {
		case got := <-done:
			t.Fatalf("Wait() returned %+v (err %v) on a health it was not waiting for", got.transition, got.err)
		case <-time.After(40 * time.Millisecond):
		}

		// The process ends short of terminal progress, so health becomes
		// abandoned — which is what was asked for.
		adapter.observation.Lifecycle = harness.LifecycleDone

		select {
		case got := <-done:
			if got.err != nil {
				t.Fatalf("Wait() = %v, want no error", got.err)
			}
			if got.transition.Health != HealthAbandoned {
				t.Errorf("Health = %q, want %q", got.transition.Health, HealthAbandoned)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Wait() did not return after a matching transition")
		}
	})

	t.Run("an engagement already in a matching health matches at once", func(t *testing.T) {
		t.Parallel()
		// The case a caller hits constantly: spawn, then wait. If the feed's
		// first pass were suppressed, an engagement that blocked before wait
		// started would never transition again and the wait would hang on
		// exactly the condition it was watching for.
		adapter := &fakeAdapter{name: "fake", observation: harness.Observation{
			Found: true, Lifecycle: harness.LifecycleBlocked, LastActivityAt: now,
		}}
		d := newTestDirector(t, adapter, now)
		if _, err := d.Spawn(context.Background(), SpawnOptions{Task: "investigate", Brief: "look"}); err != nil {
			t.Fatalf("Spawn() = %v, want no error", err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		transition, err := d.Wait(ctx, WaitOptions{Interval: 5 * time.Millisecond})
		if err != nil {
			t.Fatalf("Wait() = %v, want the already-blocked engagement", err)
		}
		if transition.Health != HealthBlocked {
			t.Errorf("Health = %q, want %q", transition.Health, HealthBlocked)
		}
	})

	t.Run("--engagement ignores every other engagement", func(t *testing.T) {
		t.Parallel()
		adapter := &fakeAdapter{name: "fake", observation: harness.Observation{
			Found: true, Lifecycle: harness.LifecycleBlocked, LastActivityAt: now,
		}}
		d := newTestDirector(t, adapter, now)
		if _, err := d.Spawn(context.Background(), SpawnOptions{Task: "investigate", Brief: "first"}); err != nil {
			t.Fatalf("Spawn() = %v, want no error", err)
		}
		second, err := d.Spawn(context.Background(), SpawnOptions{Task: "investigate", Brief: "second"})
		if err != nil {
			t.Fatalf("Spawn() = %v, want no error", err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		transition, err := d.Wait(ctx, WaitOptions{
			Engagements: []string{second.ID}, Interval: 5 * time.Millisecond,
		})
		if err != nil {
			t.Fatalf("Wait() = %v, want no error", err)
		}
		if transition.Engagement != second.ID {
			t.Errorf("Engagement = %q, want %q", transition.Engagement, second.ID)
		}
	})

	t.Run("a timeout is distinguishable from a match", func(t *testing.T) {
		t.Parallel()
		adapter := &fakeAdapter{name: "fake", observation: working()}
		d := newTestDirector(t, adapter, now)
		if _, err := d.Spawn(context.Background(), SpawnOptions{Task: "investigate", Brief: "look"}); err != nil {
			t.Fatalf("Spawn() = %v, want no error", err)
		}

		_, err := d.Wait(context.Background(), WaitOptions{
			Until: []Health{HealthBlocked}, Interval: 5 * time.Millisecond, Timeout: 50 * time.Millisecond,
		})
		if !errors.Is(err, ErrWaitTimeout) {
			t.Errorf("Wait() = %v, want ErrWaitTimeout so a caller can tell it apart from a failure", err)
		}
	})

	t.Run("an unknown health is refused rather than waited on forever", func(t *testing.T) {
		t.Parallel()
		adapter := &fakeAdapter{name: "fake", observation: working()}
		d := newTestDirector(t, adapter, now)

		_, err := d.Wait(context.Background(), WaitOptions{Until: []Health{"blocekd"}})
		if err == nil {
			t.Fatal("Wait() = nil error on a misspelled health, want a refusal")
		}
		if !strings.Contains(err.Error(), string(HealthBlocked)) {
			t.Errorf("Wait() error = %q, want it to name the valid values", err)
		}
	})

	t.Run("an unknown engagement is refused", func(t *testing.T) {
		t.Parallel()
		adapter := &fakeAdapter{name: "fake", observation: working()}
		d := newTestDirector(t, adapter, now)

		_, err := d.Wait(context.Background(), WaitOptions{Engagements: []string{"eng_nope"}})
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("Wait() = %v, want ErrNotFound — waiting on an id that does not exist looks identical to patience", err)
		}
	})
}
