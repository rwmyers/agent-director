package director

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rwmyers/agent-director/harness"
)

// stoppingAdapter records what it was asked to stop, so a test can tell an
// engagement that was ended from one that was merely forgotten about.
type stoppingAdapter struct {
	*fakeAdapter
	stopped []string
}

func (s *stoppingAdapter) Stop(_ context.Context, req harness.StopRequest) error {
	s.stopped = append(s.stopped, req.Ref)
	return nil
}

func TestRetire(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)

	withEngagement := func(t *testing.T, lifecycle harness.Lifecycle) (*Director, *stoppingAdapter, *Engagement) {
		t.Helper()
		base := &fakeAdapter{name: "fake", observation: harness.Observation{
			Found: true, Lifecycle: lifecycle, LastActivityAt: now,
		}}
		adapter := &stoppingAdapter{fakeAdapter: base}
		d := newTestDirector(t, base, now)
		d.Lookup = func(string) (harness.Adapter, error) { return adapter, nil }

		engagement, err := d.Spawn(context.Background(), SpawnOptions{Task: "investigate", Brief: "look"})
		if err != nil {
			t.Fatalf("Spawn() = %v, want no error", err)
		}
		return d, adapter, engagement
	}

	t.Run("an idle director is removed", func(t *testing.T) {
		t.Parallel()
		d := newTestDirector(t, &fakeAdapter{name: "fake"}, now)

		result, err := d.Retire(context.Background(), RetireOptions{})
		if err != nil {
			t.Fatalf("Retire() = %v, want no error", err)
		}
		if _, err := os.Stat(result.Path); !os.IsNotExist(err) {
			t.Error("Retire() left the state file behind")
		}
	})

	t.Run("a director with running work refuses, and says how to proceed", func(t *testing.T) {
		t.Parallel()
		// The refusal is the point. This record is the only thing mapping an
		// engagement back to its harness, so removing it does not stop the
		// fleet — it makes the fleet unreachable, silently.
		d, _, engagement := withEngagement(t, harness.LifecycleWorking)

		_, err := d.Retire(context.Background(), RetireOptions{})
		if err == nil {
			t.Fatal("Retire() = nil error with a running engagement, want a refusal")
		}
		if !strings.Contains(err.Error(), engagement.ID) {
			t.Errorf("Retire() error = %q, want it to name what is still running", err)
		}
		for _, flag := range []string{"--stop", "--force"} {
			if !strings.Contains(err.Error(), flag) {
				t.Errorf("Retire() error = %q, want it to offer %s", err, flag)
			}
		}
		if _, err := os.Stat(d.State.Path); err != nil {
			t.Error("Retire() removed the state file despite refusing")
		}
	})

	t.Run("--stop ends the work before removing the record", func(t *testing.T) {
		t.Parallel()
		d, adapter, engagement := withEngagement(t, harness.LifecycleWorking)

		result, err := d.Retire(context.Background(), RetireOptions{Stop: true})
		if err != nil {
			t.Fatalf("Retire() = %v, want no error", err)
		}
		if len(adapter.stopped) != 1 {
			t.Fatalf("adapter was asked to stop %d engagements, want 1", len(adapter.stopped))
		}
		if len(result.Stopped) != 1 || result.Stopped[0] != engagement.ID {
			t.Errorf("Stopped = %v, want [%s]", result.Stopped, engagement.ID)
		}
		if len(result.Orphaned) != 0 {
			t.Errorf("Orphaned = %v, want nothing orphaned when everything stopped", result.Orphaned)
		}
	})

	t.Run("--force removes the record and reports what it stranded", func(t *testing.T) {
		t.Parallel()
		// Orphaning is a legitimate choice, but it must be reported: those
		// processes are still running and nothing can reach them afterwards.
		d, adapter, engagement := withEngagement(t, harness.LifecycleWorking)

		result, err := d.Retire(context.Background(), RetireOptions{Force: true})
		if err != nil {
			t.Fatalf("Retire() = %v, want no error", err)
		}
		if len(adapter.stopped) != 0 {
			t.Errorf("--force stopped %v, want it to leave them running", adapter.stopped)
		}
		if len(result.Orphaned) != 1 || result.Orphaned[0] != engagement.ID {
			t.Errorf("Orphaned = %v, want [%s]", result.Orphaned, engagement.ID)
		}
	})

	t.Run("a finished engagement does not block retirement", func(t *testing.T) {
		t.Parallel()
		// Nothing is running, so there is nothing to strand.
		d, _, _ := withEngagement(t, harness.LifecycleDone)

		if _, err := d.Retire(context.Background(), RetireOptions{}); err != nil {
			t.Errorf("Retire() = %v, want no error for a fleet that has finished", err)
		}
	})

	t.Run("an unknown director reports what does exist", func(t *testing.T) {
		t.Parallel()
		d := newTestDirector(t, &fakeAdapter{name: "fake"}, now)

		_, err := RetireByID(context.Background(), d.Roots, "dir_nosuchthing", RetireOptions{}, nil)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("Retire() = %v, want ErrNotFound", err)
		}
		if !strings.Contains(err.Error(), d.State.DirectorID) {
			t.Errorf("Retire() error = %q, want it to list the director that does exist", err)
		}
	})

	t.Run("retiring one director leaves the others alone", func(t *testing.T) {
		t.Parallel()
		d := newTestDirector(t, &fakeAdapter{name: "fake"}, now)
		other, err := Init(d.Roots, "test", "keeper", func() time.Time { return now })
		if err != nil {
			t.Fatalf("Init() = %v, want no error", err)
		}

		if _, err := d.Retire(context.Background(), RetireOptions{}); err != nil {
			t.Fatalf("Retire() = %v, want no error", err)
		}
		remaining, err := ListDirectors(d.Roots.Primary)
		if err != nil {
			t.Fatalf("ListDirectors() = %v, want no error", err)
		}
		if len(remaining) != 1 || remaining[0].DirectorID != other.DirectorID {
			t.Errorf("remaining = %+v, want only %s", remaining, other.DirectorID)
		}
	})
}
