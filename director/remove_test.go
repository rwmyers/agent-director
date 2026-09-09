package director

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rwmyers/agent-director/harness"
)

func TestResolveEngagement(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)

	adapter := &fakeAdapter{name: "fake", observation: harness.Observation{
		Found: true, Lifecycle: harness.LifecycleDone, LastActivityAt: now,
	}}
	d := newTestDirector(t, adapter, now)

	migration, err := d.Spawn(context.Background(), SpawnOptions{
		Task: "investigate", Title: "migrate the billing schema", Brief: "look"})
	if err != nil {
		t.Fatalf("Spawn() = %v, want no error", err)
	}
	docs, err := d.Spawn(context.Background(), SpawnOptions{
		Task: "investigate", Title: "document the billing API", Brief: "look"})
	if err != nil {
		t.Fatalf("Spawn() = %v, want no error", err)
	}

	// The short handle a director's own status table prints: four hex
	// characters out of the identifier, which is all a person reads back.
	shortHandle := strings.TrimPrefix(migration.ID, engagementPrefix)[:4]

	tests := []struct {
		name     string
		fragment string
		want     string
		// wantErr is a substring the refusal must contain. Empty means the
		// resolution is expected to succeed.
		wantErr []string
	}{
		{
			name:     "a full identifier",
			fragment: migration.ID,
			want:     migration.ID,
		},
		{
			name:     "a short handle from the status table",
			fragment: shortHandle,
			want:     migration.ID,
		},
		{
			name:     "a title substring",
			fragment: "migrate",
			want:     migration.ID,
		},
		{
			name:     "a title substring in the wrong case",
			fragment: "DOCUMENT THE",
			want:     docs.ID,
		},
		{
			// Guessing between two would act on somebody else's engagement,
			// and nothing in the output would say which one it picked.
			name:     "a fragment matching two engagements is refused, naming both",
			fragment: "billing",
			wantErr:  []string{migration.ID, docs.ID, "matches 2"},
		},
		{
			name:     "a fragment matching nothing says what is here",
			fragment: "eng_nosuchthing",
			wantErr:  []string{migration.ID, docs.ID},
		},
		{
			name:     "an empty fragment names nothing",
			fragment: "",
			wantErr:  []string{"no engagement named"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := d.ResolveEngagement(test.fragment)

			if len(test.wantErr) > 0 {
				if err == nil {
					t.Fatalf("ResolveEngagement(%q) = %s, want a refusal", test.fragment, got.ID)
				}
				for _, want := range test.wantErr {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("ResolveEngagement(%q) error = %q, want it to mention %q", test.fragment, err, want)
					}
				}
				return
			}

			if err != nil {
				t.Fatalf("ResolveEngagement(%q) = %v, want no error", test.fragment, err)
			}
			if got.ID != test.want {
				t.Errorf("ResolveEngagement(%q) = %s, want %s", test.fragment, got.ID, test.want)
			}
		})
	}
}

func TestRemove(t *testing.T) {
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

		engagement, err := d.Spawn(context.Background(), SpawnOptions{
			Task: "investigate", Title: "the one to forget", Brief: "look"})
		if err != nil {
			t.Fatalf("Spawn() = %v, want no error", err)
		}
		return d, adapter, engagement
	}

	t.Run("a finished engagement is forgotten, and stops being listed", func(t *testing.T) {
		t.Parallel()
		d, _, engagement := withEngagement(t, harness.LifecycleDone)

		result, err := d.Remove(context.Background(), engagement.ID, RemoveOptions{})
		if err != nil {
			t.Fatalf("Remove() = %v, want no error", err)
		}
		if result.Title != engagement.Title {
			t.Errorf("Title = %q, want %q — the row is gone, so the result has to carry it", result.Title, engagement.Title)
		}
		fleet, err := d.Status(context.Background())
		if err != nil {
			t.Fatalf("Status() = %v, want no error", err)
		}
		if len(fleet) != 0 {
			t.Errorf("Status() = %d engagements, want 0", len(fleet))
		}

		// Removal has to survive the process, not just the in-memory copy.
		reloaded, err := LoadState(d.State.Path)
		if err != nil {
			t.Fatalf("LoadState() = %v, want no error", err)
		}
		if _, ok := reloaded.Engagements[engagement.ID]; ok {
			t.Error("Remove() left the engagement in the state file")
		}
	})

	t.Run("a live engagement refuses, and says how to proceed", func(t *testing.T) {
		t.Parallel()
		// Same reasoning as retire, one row at a time: this record is the only
		// thing mapping the engagement to its harness, so forgetting a running
		// one does not stop it — it makes it unreachable, silently.
		d, _, engagement := withEngagement(t, harness.LifecycleWorking)

		_, err := d.Remove(context.Background(), engagement.ID, RemoveOptions{})
		if err == nil {
			t.Fatal("Remove() = nil error for a live engagement, want a refusal")
		}
		if !strings.Contains(err.Error(), engagement.ID) {
			t.Errorf("Remove() error = %q, want it to name what is still running", err)
		}
		for _, flag := range []string{"--stop", "--force"} {
			if !strings.Contains(err.Error(), flag) {
				t.Errorf("Remove() error = %q, want it to offer %s", err, flag)
			}
		}
		if _, ok := d.State.Engagements[engagement.ID]; !ok {
			t.Error("Remove() forgot the engagement despite refusing")
		}
	})

	t.Run("--stop ends the work before forgetting it", func(t *testing.T) {
		t.Parallel()
		d, adapter, engagement := withEngagement(t, harness.LifecycleWorking)

		result, err := d.Remove(context.Background(), engagement.ID, RemoveOptions{Stop: true})
		if err != nil {
			t.Fatalf("Remove() = %v, want no error", err)
		}
		if len(adapter.stopped) != 1 {
			t.Fatalf("adapter was asked to stop %d engagements, want 1", len(adapter.stopped))
		}
		if !result.Stopped || result.Orphaned {
			t.Errorf("Stopped = %v, Orphaned = %v, want it stopped and not orphaned", result.Stopped, result.Orphaned)
		}
	})

	t.Run("--force forgets it and reports what it stranded", func(t *testing.T) {
		t.Parallel()
		d, adapter, engagement := withEngagement(t, harness.LifecycleWorking)

		result, err := d.Remove(context.Background(), engagement.ID, RemoveOptions{Force: true})
		if err != nil {
			t.Fatalf("Remove() = %v, want no error", err)
		}
		if len(adapter.stopped) != 0 {
			t.Errorf("--force stopped %v, want it to leave the agent running", adapter.stopped)
		}
		if !result.Orphaned {
			t.Error("Orphaned = false, want the orphaning reported")
		}
		if _, ok := d.State.Engagements[engagement.ID]; ok {
			t.Error("--force left the engagement in the record")
		}
	})

	t.Run("a failed spawn with no harness record is removable without --force", func(t *testing.T) {
		t.Parallel()
		// The case this command exists for: a spawn that left a binding and no
		// conversation. There is nothing running, so there is nothing to strand.
		d, _, engagement := withEngagement(t, harness.LifecycleDone)
		if err := d.mutate(func(state *State) error {
			state.Engagements[engagement.ID].Ref = ""
			return nil
		}); err != nil {
			t.Fatalf("mutate() = %v, want no error", err)
		}

		if _, err := d.Remove(context.Background(), engagement.ID, RemoveOptions{}); err != nil {
			t.Errorf("Remove() = %v, want no error for an engagement that never started", err)
		}
	})

	t.Run("its unanswered questions go with it", func(t *testing.T) {
		t.Parallel()
		d, _, engagement := withEngagement(t, harness.LifecycleDone)
		ask, err := d.Ask(context.Background(), engagement.ID, d.State.Engagements[engagement.ID].Token, "which branch?")
		if err != nil {
			t.Fatalf("Ask() = %v, want no error", err)
		}

		result, err := d.Remove(context.Background(), engagement.ID, RemoveOptions{})
		if err != nil {
			t.Fatalf("Remove() = %v, want no error", err)
		}
		if len(result.Asks) != 1 || result.Asks[0] != ask.ID {
			t.Errorf("Asks = %v, want [%s]", result.Asks, ask.ID)
		}
		if _, ok := d.State.Asks[ask.ID]; ok {
			t.Error("Remove() left a question belonging to an engagement nobody can see")
		}
	})

	t.Run("removing one leaves the rest of the fleet alone", func(t *testing.T) {
		t.Parallel()
		d, _, engagement := withEngagement(t, harness.LifecycleDone)
		keeper, err := d.Spawn(context.Background(), SpawnOptions{
			Task: "investigate", Title: "the one to keep", Brief: "look"})
		if err != nil {
			t.Fatalf("Spawn() = %v, want no error", err)
		}

		if _, err := d.Remove(context.Background(), engagement.ID, RemoveOptions{}); err != nil {
			t.Fatalf("Remove() = %v, want no error", err)
		}
		if len(d.State.Engagements) != 1 {
			t.Fatalf("state holds %d engagements, want 1", len(d.State.Engagements))
		}
		if _, ok := d.State.Engagements[keeper.ID]; !ok {
			t.Errorf("Remove() removed %s as well, want only %s", keeper.ID, engagement.ID)
		}
	})

	t.Run("an unknown engagement is not found", func(t *testing.T) {
		t.Parallel()
		d, _, _ := withEngagement(t, harness.LifecycleDone)

		_, err := d.Remove(context.Background(), "eng_nosuchthing", RemoveOptions{})
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("Remove() = %v, want ErrNotFound", err)
		}
	})

	t.Run("an adapter error on observation fails removal unless forced", func(t *testing.T) {
		t.Parallel()
		d, adapter, engagement := withEngagement(t, harness.LifecycleDone)
		expectedErr := errors.New("command could not be executed because the command was executed from within a sandbox")
		adapter.getErr = expectedErr

		// Without force: fails with adapter error
		if _, err := d.Remove(context.Background(), engagement.ID, RemoveOptions{}); !errors.Is(err, expectedErr) {
			t.Errorf("Remove() err = %v, want %v", err, expectedErr)
		}
		if _, ok := d.State.Engagements[engagement.ID]; !ok {
			t.Error("Remove() deleted the engagement despite observation error")
		}

		// With force: succeeds and removes
		if _, err := d.Remove(context.Background(), engagement.ID, RemoveOptions{Force: true}); err != nil {
			t.Errorf("Remove(Force: true) err = %v, want no error", err)
		}
		if _, ok := d.State.Engagements[engagement.ID]; ok {
			t.Error("Remove(Force: true) failed to remove engagement from state")
		}
	})
}
