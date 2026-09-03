package director

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rwmyers/agent-director/harness"
)

// TestOpenAsks covers the enumeration `status` prints instead of the questions
// themselves, and the lookup that fetches the text back.
func TestOpenAsks(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	adapter := &fakeAdapter{name: "fake", observation: harness.Observation{
		Found: true, Lifecycle: harness.LifecycleWorking, LastActivityAt: now,
	}}
	d := newTestDirector(t, adapter, now)

	first, err := d.Spawn(context.Background(), SpawnOptions{Task: "investigate", Brief: "one"})
	if err != nil {
		t.Fatalf("Spawn() = %v, want no error", err)
	}
	second, err := d.Spawn(context.Background(), SpawnOptions{Task: "investigate", Brief: "two"})
	if err != nil {
		t.Fatalf("Spawn() = %v, want no error", err)
	}

	early, err := d.Ask(context.Background(), first.ID, first.Token, "May I force-push?")
	if err != nil {
		t.Fatalf("Ask() = %v, want no error", err)
	}
	// A later clock, so "oldest first" is a claim about time rather than about
	// whichever identifier happened to sort first.
	d.Clock = func() time.Time { return now.Add(time.Minute) }
	late, err := d.Ask(context.Background(), second.ID, second.Token, "Which branch?")
	if err != nil {
		t.Fatalf("Ask() = %v, want no error", err)
	}

	t.Run("every unanswered question is listed, oldest first", func(t *testing.T) {
		open := d.OpenAsks()
		if len(open) != 2 {
			t.Fatalf("OpenAsks() = %d questions, want 2", len(open))
		}
		if open[0].ID != early.ID || open[1].ID != late.ID {
			t.Errorf("OpenAsks() order = %s, %s; want oldest first: %s, %s",
				open[0].ID, open[1].ID, early.ID, late.ID)
		}
	})

	t.Run("an engagement carries the identifiers of its own open questions", func(t *testing.T) {
		// This is what --json enumerates. It has to be identifiers rather than
		// the questions: status is run every turn, and a question reproduced on
		// every turn is the thing this replaced.
		observed, err := d.Get(context.Background(), first.ID)
		if err != nil {
			t.Fatalf("Get() = %v, want no error", err)
		}
		if !equalStrings(observed.OpenAsks, []string{early.ID}) {
			t.Errorf("OpenAsks = %v, want [%s]", observed.OpenAsks, early.ID)
		}
	})

	t.Run("naming a question returns its text", func(t *testing.T) {
		asks, err := d.Asks([]string{late.ID})
		if err != nil {
			t.Fatalf("Asks() = %v, want no error", err)
		}
		if len(asks) != 1 || asks[0].Question != "Which branch?" {
			t.Errorf("Asks() = %+v, want the question text", asks)
		}
	})

	t.Run("questions come back in the order they were named", func(t *testing.T) {
		asks, err := d.Asks([]string{late.ID, early.ID})
		if err != nil {
			t.Fatalf("Asks() = %v, want no error", err)
		}
		if len(asks) != 2 || asks[0].ID != late.ID || asks[1].ID != early.ID {
			t.Errorf("Asks() = %+v, want the order asked for", asks)
		}
	})

	t.Run("an unknown identifier does not cost the ones that were found", func(t *testing.T) {
		asks, err := d.Asks([]string{early.ID, "ask_nonesuch"})
		if err == nil {
			t.Fatal("Asks() = nil error on an unknown identifier, want it reported")
		}
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("Asks() error = %v, want it to wrap ErrNotFound so the exit code says so", err)
		}
		if !strings.Contains(err.Error(), "ask_nonesuch") {
			t.Errorf("Asks() error = %q, want it to name the identifier that missed", err)
		}
		if len(asks) != 1 || asks[0].ID != early.ID {
			t.Errorf("Asks() = %+v, want the question that was found returned anyway", asks)
		}
	})

	t.Run("an answered question stops being open", func(t *testing.T) {
		if _, err := d.Answer(early.ID, "No."); err != nil {
			t.Fatalf("Answer() = %v, want no error", err)
		}
		open := d.OpenAsks()
		if len(open) != 1 || open[0].ID != late.ID {
			t.Errorf("OpenAsks() = %+v, want only the unanswered one", open)
		}
		observed, err := d.Get(context.Background(), first.ID)
		if err != nil {
			t.Fatalf("Get() = %v, want no error", err)
		}
		if len(observed.OpenAsks) != 0 {
			t.Errorf("OpenAsks = %v, want none once it was answered", observed.OpenAsks)
		}
	})
}
