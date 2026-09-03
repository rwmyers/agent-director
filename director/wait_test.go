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

// canBackground gives a test director a host that permits a blocking wait,
// which is what every case below is actually about. Without one Wait refuses
// before it looks at anything else, which is its own test further down.
func canBackground(t *testing.T, d *Director) *Director {
	t.Helper()
	d.State.Host = Host{
		Harness: "panes",
		Ref:     "w6:p1",
		Hosting: harness.Hosting{Background: true},
		Source:  HostDetected,
	}
	// Written rather than only held, because every state mutation re-reads the
	// file — as it must, since an agent's report can land between two of this
	// director's own commands.
	if err := d.State.Save(); err != nil {
		t.Fatalf("Save() = %v, want no error", err)
	}
	return d
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
		d := canBackground(t, newTestDirector(t, adapter, now))
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
		d := canBackground(t, newTestDirector(t, adapter, now))
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
		d := canBackground(t, newTestDirector(t, adapter, now))
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
		d := canBackground(t, newTestDirector(t, adapter, now))
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
		d := canBackground(t, newTestDirector(t, adapter, now))

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
		d := canBackground(t, newTestDirector(t, adapter, now))

		_, err := d.Wait(context.Background(), WaitOptions{Engagements: []string{"eng_nope"}})
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("Wait() = %v, want ErrNotFound — waiting on an id that does not exist looks identical to patience", err)
		}
	})
}

func TestWaitRefusesWhereItCannotBeBackgrounded(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

	t.Run("a host that cannot background one is refused before anything blocks", func(t *testing.T) {
		t.Parallel()
		// Nothing here has a host, which is the default and the safe answer.
		// The refusal must be immediate: the whole point is that blocking on
		// this host is not slow, it is gone.
		d := newTestDirector(t, &fakeAdapter{name: "fake"}, now)

		_, err := d.Wait(context.Background(), WaitOptions{Timeout: time.Hour})
		if !errors.Is(err, ErrHostCannotWait) {
			t.Fatalf("Wait() = %v, want ErrHostCannotWait", err)
		}
		// A director reading this needs somewhere to go, and status is the one
		// thing that works on every host.
		if !strings.Contains(err.Error(), "director status") {
			t.Errorf("Wait() = %v, want it to point at director status", err)
		}
	})

	t.Run("a host that declares only wake still cannot be blocked on", func(t *testing.T) {
		t.Parallel()
		// The two bits are independent: being reachable says nothing about
		// whether this conversation can put a command in the background.
		d := newTestDirector(t, &fakeAdapter{name: "fake"}, now)
		d.State.Host = Host{Harness: "panes", Hosting: harness.Hosting{Wake: true}, Source: HostDetected}

		if _, err := d.Wait(context.Background(), WaitOptions{}); !errors.Is(err, ErrHostCannotWait) {
			t.Fatalf("Wait() = %v, want ErrHostCannotWait", err)
		}
	})

	t.Run("host in director.conf takes effect without re-attaching", func(t *testing.T) {
		t.Parallel()
		// The advice a refusal prints says to write `host` into director.conf.
		// A director acts on that key in the conversation that just read the
		// refusal, so it has to be read where the decision is made rather than
		// where the conversation attached — otherwise the remedy silently does
		// nothing and the same refusal comes back.
		d := newTestDirector(t, &fakeAdapter{name: "fake"}, now)
		withHosts(d, &hostAdapter{
			fakeAdapter: fakeAdapter{name: "sessions"},
			hosting:     harness.Hosting{Background: true},
		})
		// Attached before the key existed: nothing recognised its own
		// environment, so nothing was recorded.
		if err := d.claim(); err != nil {
			t.Fatalf("claim() = %v, want no error", err)
		}
		if _, err := d.Wait(context.Background(), WaitOptions{}); !errors.Is(err, ErrHostCannotWait) {
			t.Fatalf("Wait() before the key = %v, want ErrHostCannotWait", err)
		}

		// The key, as somebody has just written it. No second attach.
		d.Config.Host = "sessions"

		_, err := d.Wait(context.Background(), WaitOptions{
			Interval: 5 * time.Millisecond, Timeout: 30 * time.Millisecond,
		})
		if !errors.Is(err, ErrWaitTimeout) {
			t.Fatalf("Wait() after the key = %v, want it to have waited and timed out", err)
		}
		if d.State.Host.Known() {
			t.Errorf("recorded host = %+v, want it untouched: the key is read, not attached", d.State.Host)
		}
	})

	t.Run("a recorded host still answers when nothing detects now", func(t *testing.T) {
		t.Parallel()
		// Re-locating is ambient and can come up empty where the attach did
		// not. Losing a capability the conversation already had would be a
		// regression dressed as a correction.
		d := newTestDirector(t, &fakeAdapter{name: "fake"}, now)
		withHosts(d)
		d.State.Host = Host{Harness: "panes", Hosting: harness.Hosting{Background: true}, Source: HostDetected}

		_, err := d.Wait(context.Background(), WaitOptions{
			Interval: 5 * time.Millisecond, Timeout: 30 * time.Millisecond,
		})
		if !errors.Is(err, ErrWaitTimeout) {
			t.Fatalf("Wait() = %v, want it to have waited and timed out", err)
		}
	})

	t.Run("--force overrides the refusal", func(t *testing.T) {
		t.Parallel()
		// Detection is ambient and can be wrong. Somebody who knows better must
		// not be stuck behind an adapter's declaration.
		adapter := &fakeAdapter{name: "fake"}
		d := newTestDirector(t, adapter, now)

		_, err := d.Wait(context.Background(), WaitOptions{
			Force: true, Interval: 5 * time.Millisecond, Timeout: 30 * time.Millisecond,
		})
		if !errors.Is(err, ErrWaitTimeout) {
			t.Fatalf("Wait(--force) = %v, want it to have waited and timed out", err)
		}
	})
}
