package director

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rwmyers/agent-director/harness"
)

// disposingAdapter is a harness that has a slot to give back, and records every
// slot it was asked to reclaim.
//
// stopErr and disposeErr exist because the two failures mean different things:
// a stop that did not take says the agent may still be in there, while a
// dispose that did not take says only that the slot is still on somebody's
// screen.
type disposingAdapter struct {
	*fakeAdapter
	declares   bool
	stopErr    error
	disposeErr error
	// seat is the conversation this adapter reports the current process as
	// running in, standing in for a herdr pane. Empty means "not inside".
	seat string

	disposed []string
}

func (a *disposingAdapter) Stop(context.Context, harness.StopRequest) error {
	return a.stopErr
}

func (a *disposingAdapter) Disposes() bool { return a.declares }

func (a *disposingAdapter) Dispose(_ context.Context, req harness.DisposeRequest) error {
	if a.disposeErr != nil {
		return a.disposeErr
	}
	a.disposed = append(a.disposed, req.Ref)
	return nil
}

func (a *disposingAdapter) Locate() (string, bool) {
	if a.seat == "" {
		return "", false
	}
	return a.seat, true
}

// plainAdapter is a harness with no slot at all — Claude Code's shape. It must
// not implement Disposer, because the point of the test using it is that
// removal there behaves exactly as it did before disposal existed.
type plainAdapter struct {
	*fakeAdapter
}

func TestRemoveReclaimsTheHarnessSlot(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)

	withDisposing := func(t *testing.T, lifecycle harness.Lifecycle, prepare func(*disposingAdapter)) (*Director, *disposingAdapter, *Engagement) {
		t.Helper()
		base := &fakeAdapter{name: "fake", observation: harness.Observation{
			Found: true, Lifecycle: lifecycle, LastActivityAt: now,
		}}
		adapter := &disposingAdapter{fakeAdapter: base, declares: true}
		if prepare != nil {
			prepare(adapter)
		}
		d := newTestDirector(t, base, now)
		d.Lookup = func(string) (harness.Adapter, error) { return adapter, nil }

		engagement, err := d.Spawn(context.Background(), SpawnOptions{
			Task: "investigate", Title: "the one to forget", Brief: "look"})
		if err != nil {
			t.Fatalf("Spawn() = %v, want no error", err)
		}
		return d, adapter, engagement
	}

	t.Run("a finished engagement gives its slot back", func(t *testing.T) {
		t.Parallel()
		d, adapter, engagement := withDisposing(t, harness.LifecycleDone, nil)

		result, err := d.Remove(context.Background(), engagement.ID, RemoveOptions{})
		if err != nil {
			t.Fatalf("Remove() = %v, want no error", err)
		}
		if len(adapter.disposed) != 1 || adapter.disposed[0] != engagement.Ref {
			t.Fatalf("adapter reclaimed %v, want [%s]", adapter.disposed, engagement.Ref)
		}
		if result.Disposal != DisposalClosed {
			t.Errorf("Disposal = %q, want %q", result.Disposal, DisposalClosed)
		}
	})

	t.Run("a harness that declares no slot removes exactly as before", func(t *testing.T) {
		t.Parallel()
		// The whole compatibility requirement in one assertion: an adapter
		// written before any of this must see no new call and produce no new
		// output, not even a "nothing to reclaim".
		base := &fakeAdapter{name: "fake", observation: harness.Observation{
			Found: true, Lifecycle: harness.LifecycleDone, LastActivityAt: now,
		}}
		adapter := &plainAdapter{fakeAdapter: base}
		d := newTestDirector(t, base, now)
		d.Lookup = func(string) (harness.Adapter, error) { return adapter, nil }
		engagement, err := d.Spawn(context.Background(), SpawnOptions{
			Task: "investigate", Title: "no slot here", Brief: "look"})
		if err != nil {
			t.Fatalf("Spawn() = %v, want no error", err)
		}

		result, err := d.Remove(context.Background(), engagement.ID, RemoveOptions{})
		if err != nil {
			t.Fatalf("Remove() = %v, want no error", err)
		}
		if result.Disposal != DisposalNone || result.DisposalReason != "" {
			t.Errorf("Disposal = %q (%q), want nothing reported at all", result.Disposal, result.DisposalReason)
		}
		if _, ok := d.State.Engagements[engagement.ID]; ok {
			t.Error("Remove() left the engagement behind")
		}
	})

	t.Run("a harness that declares the capability off is never asked", func(t *testing.T) {
		t.Parallel()
		d, adapter, engagement := withDisposing(t, harness.LifecycleDone, func(a *disposingAdapter) {
			a.declares = false
		})

		result, err := d.Remove(context.Background(), engagement.ID, RemoveOptions{})
		if err != nil {
			t.Fatalf("Remove() = %v, want no error", err)
		}
		if len(adapter.disposed) != 0 {
			t.Errorf("adapter reclaimed %v having declared it has nothing to reclaim", adapter.disposed)
		}
		if result.Disposal != DisposalNone {
			t.Errorf("Disposal = %q, want nothing reported", result.Disposal)
		}
	})

	t.Run("--force never closes the slot", func(t *testing.T) {
		t.Parallel()
		// --force exists to forget an engagement while deliberately leaving its
		// agent running. The slot is where it is running, so closing it would
		// destroy the exact thing the flag preserves.
		d, adapter, engagement := withDisposing(t, harness.LifecycleWorking, nil)

		result, err := d.Remove(context.Background(), engagement.ID, RemoveOptions{Force: true})
		if err != nil {
			t.Fatalf("Remove() = %v, want no error", err)
		}
		if len(adapter.disposed) != 0 {
			t.Fatalf("--force closed %v, want the agent left where it is running", adapter.disposed)
		}
		if result.Disposal != DisposalKept {
			t.Errorf("Disposal = %q, want %q", result.Disposal, DisposalKept)
		}
		if !strings.Contains(result.DisposalReason, "--force") {
			t.Errorf("DisposalReason = %q, want it to name --force", result.DisposalReason)
		}
	})

	t.Run("--force on a finished engagement still leaves the slot alone", func(t *testing.T) {
		t.Parallel()
		// The flag is a statement about not touching the agent, and it is not
		// conditional on the engagement having turned out to be live.
		d, adapter, engagement := withDisposing(t, harness.LifecycleDone, nil)

		if _, err := d.Remove(context.Background(), engagement.ID, RemoveOptions{Force: true}); err != nil {
			t.Fatalf("Remove() = %v, want no error", err)
		}
		if len(adapter.disposed) != 0 {
			t.Errorf("--force closed %v, want nothing closed under --force ever", adapter.disposed)
		}
	})

	t.Run("--stop closes the slot once the stop has succeeded", func(t *testing.T) {
		t.Parallel()
		d, adapter, engagement := withDisposing(t, harness.LifecycleWorking, nil)

		result, err := d.Remove(context.Background(), engagement.ID, RemoveOptions{Stop: true})
		if err != nil {
			t.Fatalf("Remove() = %v, want no error", err)
		}
		if !result.Stopped {
			t.Fatalf("Stopped = false, want the stop to have succeeded")
		}
		if len(adapter.disposed) != 1 {
			t.Errorf("adapter reclaimed %v, want the slot given back after a successful stop", adapter.disposed)
		}
		if result.Disposal != DisposalClosed {
			t.Errorf("Disposal = %q, want %q", result.Disposal, DisposalClosed)
		}
	})

	t.Run("--stop leaves the slot alone when the stop failed", func(t *testing.T) {
		t.Parallel()
		// The agent may still be alive in there. A stop that did not take is
		// the one case where the engagement looked stoppable and is not.
		d, adapter, engagement := withDisposing(t, harness.LifecycleWorking, func(a *disposingAdapter) {
			a.stopErr = errors.New("the harness would not end it")
		})

		result, err := d.Remove(context.Background(), engagement.ID, RemoveOptions{Stop: true})
		if err != nil {
			t.Fatalf("Remove() = %v, want no error", err)
		}
		if !result.Orphaned {
			t.Fatalf("Orphaned = false, want the failed stop reported")
		}
		if len(adapter.disposed) != 0 {
			t.Fatalf("a failed stop closed %v, want the slot left alone", adapter.disposed)
		}
		if result.Disposal != DisposalKept || !strings.Contains(result.DisposalReason, "stop") {
			t.Errorf("Disposal = %q (%q), want it kept and the failed stop named", result.Disposal, result.DisposalReason)
		}
	})

	t.Run("a lifecycle the harness could not confirm leaves the slot alone", func(t *testing.T) {
		t.Parallel()
		// Unknown means the harness answered and does not know, which is not
		// evidence that the conversation has finished.
		d, adapter, engagement := withDisposing(t, harness.LifecycleUnknown, nil)

		result, err := d.Remove(context.Background(), engagement.ID, RemoveOptions{})
		if err != nil {
			t.Fatalf("Remove() = %v, want no error", err)
		}
		if len(adapter.disposed) != 0 {
			t.Fatalf("an unconfirmed engagement closed %v, want the slot left alone", adapter.disposed)
		}
		if result.Disposal != DisposalKept {
			t.Errorf("Disposal = %q, want %q", result.Disposal, DisposalKept)
		}
	})

	t.Run("a slot that will not close does not fail the removal", func(t *testing.T) {
		t.Parallel()
		// The row has already gone and nothing restores it. Failing here would
		// report a removal that happened as one that did not, and the obvious
		// response — run it again — then answers "nothing matches".
		d, _, engagement := withDisposing(t, harness.LifecycleDone, func(a *disposingAdapter) {
			a.disposeErr = errors.New("herdr server is not running")
		})

		result, err := d.Remove(context.Background(), engagement.ID, RemoveOptions{})
		if err != nil {
			t.Fatalf("Remove() = %v, want the removal to succeed", err)
		}
		if result.Disposal != DisposalFailed {
			t.Errorf("Disposal = %q, want %q", result.Disposal, DisposalFailed)
		}
		if !strings.Contains(result.DisposalReason, "herdr server is not running") {
			t.Errorf("DisposalReason = %q, want it to carry what the harness said", result.DisposalReason)
		}
		if _, ok := d.State.Engagements[engagement.ID]; ok {
			t.Error("a failed slot close left the row behind, want the removal to have happened")
		}
		reloaded, err := LoadState(d.State.Path)
		if err != nil {
			t.Fatalf("LoadState() = %v, want no error", err)
		}
		if _, ok := reloaded.Engagements[engagement.ID]; ok {
			t.Error("a failed slot close left the row in the state file")
		}
	})

	t.Run("the director's own recorded seat is never closed", func(t *testing.T) {
		t.Parallel()
		// A director very often runs inside the harness it dispatches to. A
		// removal that reached its own seat would kill the session issuing the
		// command, taking the fleet's only record with it.
		d, adapter, engagement := withDisposing(t, harness.LifecycleDone, nil)
		// Through mutate, because the removal reloads the state file under its
		// lock: an in-memory host would not survive to be consulted.
		recordHost(t, d, Host{Harness: engagement.Harness, Ref: engagement.Ref, Source: HostDetected})

		result, err := d.Remove(context.Background(), engagement.ID, RemoveOptions{})
		if err != nil {
			t.Fatalf("Remove() = %v, want no error", err)
		}
		if len(adapter.disposed) != 0 {
			t.Fatalf("a removal closed the director's own seat %v", adapter.disposed)
		}
		if result.Disposal != DisposalKept || !strings.Contains(result.DisposalReason, "director itself") {
			t.Errorf("Disposal = %q (%q), want it kept and the reason to say why", result.Disposal, result.DisposalReason)
		}
	})

	t.Run("the seat the harness reports right now is never closed", func(t *testing.T) {
		t.Parallel()
		// The recorded host is written when a director attaches, so a command
		// run from a conversation that never attached would have nothing to
		// compare against. Asking the adapter now is the second guard.
		d, adapter, engagement := withDisposing(t, harness.LifecycleDone, nil)
		// The seat is set after the spawn, because it is the engagement's own
		// ref that has to come back from the harness. Nothing is recorded as
		// this director's host, so only the live answer can carry the guard.
		adapter.seat = engagement.Ref

		result, err := d.Remove(context.Background(), engagement.ID, RemoveOptions{})
		if err != nil {
			t.Fatalf("Remove() = %v, want no error", err)
		}
		if len(adapter.disposed) != 0 {
			t.Fatalf("a removal closed the seat the harness says this process occupies: %v", adapter.disposed)
		}
		if result.Disposal != DisposalKept {
			t.Errorf("Disposal = %q, want %q", result.Disposal, DisposalKept)
		}
	})

	t.Run("somebody else's seat in the same harness is still closed", func(t *testing.T) {
		t.Parallel()
		// The guard has to be about this conversation and not about the
		// harness, or a director in a pane could never reclaim any pane.
		d, adapter, engagement := withDisposing(t, harness.LifecycleDone, nil)
		adapter.seat = "some-other-pane"
		recordHost(t, d, Host{Harness: engagement.Harness, Ref: "some-other-pane", Source: HostDetected})

		if _, err := d.Remove(context.Background(), engagement.ID, RemoveOptions{}); err != nil {
			t.Fatalf("Remove() = %v, want no error", err)
		}
		if len(adapter.disposed) != 1 {
			t.Errorf("adapter reclaimed %v, want the engagement's own slot given back", adapter.disposed)
		}
	})

	t.Run("a spawn that never got a slot reclaims nothing", func(t *testing.T) {
		t.Parallel()
		d, adapter, engagement := withDisposing(t, harness.LifecycleDone, nil)
		if err := d.mutate(func(state *State) error {
			state.Engagements[engagement.ID].Ref = ""
			return nil
		}); err != nil {
			t.Fatalf("mutate() = %v, want no error", err)
		}

		result, err := d.Remove(context.Background(), engagement.ID, RemoveOptions{})
		if err != nil {
			t.Fatalf("Remove() = %v, want no error", err)
		}
		if len(adapter.disposed) != 0 {
			t.Errorf("adapter reclaimed %v for an engagement that never had a slot", adapter.disposed)
		}
		if result.Disposal != DisposalNone {
			t.Errorf("Disposal = %q, want nothing reported", result.Disposal)
		}
	})
}

func TestRetireDoesNotCloseHarnessSlots(t *testing.T) {
	t.Parallel()
	// Retiring is routinely done from a new conversation to clear up other
	// directors, whose slots are not the invoker's to destroy. remove opts into
	// reclaiming; nothing else that deletes state inherits it.
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	base := &fakeAdapter{name: "fake", observation: harness.Observation{
		Found: true, Lifecycle: harness.LifecycleDone, LastActivityAt: now,
	}}
	adapter := &disposingAdapter{fakeAdapter: base, declares: true}
	d := newTestDirector(t, base, now)
	d.Lookup = func(string) (harness.Adapter, error) { return adapter, nil }

	if _, err := d.Spawn(context.Background(), SpawnOptions{
		Task: "investigate", Title: "the retired one", Brief: "look"}); err != nil {
		t.Fatalf("Spawn() = %v, want no error", err)
	}

	if _, err := d.Retire(context.Background(), RetireOptions{}); err != nil {
		t.Fatalf("Retire() = %v, want no error", err)
	}
	if len(adapter.disposed) != 0 {
		t.Errorf("Retire() closed %v, want retire to leave every slot alone", adapter.disposed)
	}
}

// recordHost persists where this director is sitting, the way attaching does.
// Setting State.Host in memory is not enough: a removal reloads the state file
// under its lock before anything downstream reads it back.
func recordHost(t *testing.T, d *Director, host Host) {
	t.Helper()
	if err := d.mutate(func(state *State) error {
		state.Host = host
		return nil
	}); err != nil {
		t.Fatalf("recording the host = %v, want no error", err)
	}
}
