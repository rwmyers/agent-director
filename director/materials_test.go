package director

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rwmyers/agent-director/harness"
)

// materialsFixture is a spawned engagement on a live-looking harness, which is
// what every test here starts from.
func materialsFixture(t *testing.T) (*Director, *Engagement) {
	t.Helper()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	adapter := &fakeAdapter{name: "fake", observation: harness.Observation{
		Found: true, Lifecycle: harness.LifecycleWorking, LastActivityAt: now,
	}}
	d := newTestDirector(t, adapter, now)
	engagement, err := d.Spawn(context.Background(), SpawnOptions{Task: "investigate", Brief: "look"})
	if err != nil {
		t.Fatalf("Spawn() = %v, want no error", err)
	}
	return d, engagement
}

func report(t *testing.T, d *Director, engagement *Engagement, opts ReportOptions) *Engagement {
	t.Helper()
	updated, err := d.Report(context.Background(), engagement.ID, engagement.Token, opts)
	if err != nil {
		t.Fatalf("Report(%+v) = %v, want no error", opts, err)
	}
	return updated
}

func TestReportedMaterials(t *testing.T) {
	t.Parallel()

	t.Run("an engagement names where its work is and it is recorded", func(t *testing.T) {
		t.Parallel()
		d, engagement := materialsFixture(t)

		updated := report(t, d, engagement, ReportOptions{
			Progress:  "reading",
			Materials: []string{"feat/auth", "https://example.invalid/pr/7"},
		})
		want := []string{"feat/auth", "https://example.invalid/pr/7"}
		if !equalStrings(updated.Materials, want) {
			t.Errorf("Materials = %v, want %v", updated.Materials, want)
		}
	})

	t.Run("materials survive a reload, in the order they were given", func(t *testing.T) {
		t.Parallel()
		d, engagement := materialsFixture(t)
		report(t, d, engagement, ReportOptions{Materials: []string{"feat/auth", "internal/auth/token.go"}})

		reloaded, err := LoadState(d.State.Path)
		if err != nil {
			t.Fatalf("LoadState() = %v, want no error", err)
		}
		want := []string{"feat/auth", "internal/auth/token.go"}
		if got := reloaded.Engagements[engagement.ID].Materials; !equalStrings(got, want) {
			t.Errorf("Materials after reload = %v, want %v", got, want)
		}
	})

	t.Run("a report that does not mention materials leaves them alone", func(t *testing.T) {
		t.Parallel()
		// The footgun this field exists to avoid. `director note` replaces, so a
		// director that forgot to re-state the accumulated list silently lost
		// it. An ordinary progress report must not be able to do that.
		d, engagement := materialsFixture(t)
		report(t, d, engagement, ReportOptions{Materials: []string{"feat/auth"}})

		updated := report(t, d, engagement, ReportOptions{Progress: "delivered", Message: "still going"})
		if !equalStrings(updated.Materials, []string{"feat/auth"}) {
			t.Errorf("Materials = %v, want the previously reported set untouched", updated.Materials)
		}
	})

	t.Run("naming materials replaces the set rather than adding to it", func(t *testing.T) {
		t.Parallel()
		// Replace is the choice: the engagement knows its whole current set on
		// every call, and an append-only list can never retract a branch that
		// was abandoned or a path that moved.
		d, engagement := materialsFixture(t)
		report(t, d, engagement, ReportOptions{Materials: []string{"feat/auth", "wrong/path.go"}})

		updated := report(t, d, engagement, ReportOptions{Materials: []string{"feat/auth-v2"}})
		if !equalStrings(updated.Materials, []string{"feat/auth-v2"}) {
			t.Errorf("Materials = %v, want only what the latest report named", updated.Materials)
		}
	})

	t.Run("an explicitly empty list clears them", func(t *testing.T) {
		t.Parallel()
		d, engagement := materialsFixture(t)
		report(t, d, engagement, ReportOptions{Materials: []string{"feat/auth"}})

		updated := report(t, d, engagement, ReportOptions{Materials: []string{""}})
		if len(updated.Materials) != 0 {
			t.Errorf("Materials = %v, want them cleared", updated.Materials)
		}
	})

	t.Run("a material spanning several lines is refused", func(t *testing.T) {
		t.Parallel()
		// A material is a thing to go and look at. A paragraph would wreck the
		// one column it is rendered in, which is the problem this whole change
		// is about.
		d, engagement := materialsFixture(t)

		_, err := d.Report(context.Background(), engagement.ID, engagement.Token,
			ReportOptions{Materials: []string{"feat/auth\nand a whole paragraph"}})
		if err == nil {
			t.Fatal("Report() = nil error on a multi-line material, want a refusal")
		}
		if !strings.Contains(err.Error(), "one line") {
			t.Errorf("error = %q, want it to say what the rule is", err)
		}
	})

	t.Run("a refused material costs the whole report", func(t *testing.T) {
		t.Parallel()
		d, engagement := materialsFixture(t)

		_, _ = d.Report(context.Background(), engagement.ID, engagement.Token,
			ReportOptions{Progress: "reading", Materials: []string{"a\nb"}})

		observed, err := d.Get(context.Background(), engagement.ID)
		if err != nil {
			t.Fatalf("Get() = %v, want no error", err)
		}
		if observed.Progress != "" {
			t.Errorf("Progress = %q, want a rejected report to have written nothing", observed.Progress)
		}
	})

	t.Run("an engagement that reported nothing has no materials at all", func(t *testing.T) {
		t.Parallel()
		// Not a placeholder, not an empty string in a list. "Nothing yet" has to
		// be distinguishable from "something".
		d, engagement := materialsFixture(t)
		report(t, d, engagement, ReportOptions{Progress: "reading"})

		observed, err := d.Get(context.Background(), engagement.ID)
		if err != nil {
			t.Fatalf("Get() = %v, want no error", err)
		}
		if len(observed.Materials) != 0 {
			t.Errorf("Materials = %v, want none", observed.Materials)
		}
	})

	t.Run("a repeated material is recorded once", func(t *testing.T) {
		t.Parallel()
		d, engagement := materialsFixture(t)
		updated := report(t, d, engagement, ReportOptions{Materials: []string{"feat/auth", "feat/auth"}})
		if !equalStrings(updated.Materials, []string{"feat/auth"}) {
			t.Errorf("Materials = %v, want the repeat dropped", updated.Materials)
		}
	})
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
