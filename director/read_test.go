package director

import (
	"context"

	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rwmyers/agent-director/harness"
)

// readingAdapter is a fake that can also return output. Kept separate from
// fakeAdapter so the plain one genuinely cannot read, which is what proves the
// core refuses rather than emulating.
type readingAdapter struct {
	*fakeAdapter
	turns  []harness.Turn
	cursor string
	reads  []harness.ReadRequest
}

func (r *readingAdapter) Read(_ context.Context, req harness.ReadRequest) (harness.ReadResult, error) {
	r.reads = append(r.reads, req)
	return harness.ReadResult{
		Kind: harness.ReadTurns, Cursor: r.cursor, Total: len(r.turns),
		Complete: true, Turns: r.turns,
	}, nil
}

func newReadingDirector(t *testing.T, turns []harness.Turn, cursor string) (*Director, *readingAdapter, *Engagement) {
	t.Helper()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	base := &fakeAdapter{name: "fake", observation: harness.Observation{
		Found: true, Lifecycle: harness.LifecycleWorking, LastActivityAt: now,
	}}
	reader := &readingAdapter{fakeAdapter: base, turns: turns, cursor: cursor}

	d := newTestDirector(t, base, now)
	d.Lookup = func(string) (harness.Adapter, error) { return reader, nil }

	engagement, err := d.Spawn(context.Background(), SpawnOptions{Task: "investigate", Brief: "look"})
	if err != nil {
		t.Fatalf("Spawn() = %v, want no error", err)
	}
	return d, reader, engagement
}

func TestRead(t *testing.T) {
	t.Parallel()

	t.Run("a harness that cannot read says so rather than returning nothing", func(t *testing.T) {
		t.Parallel()
		// An empty result reads as "the agent said nothing", which is a
		// different and much more dangerous claim than "I cannot see what it
		// said" — the first invites a director to conclude the work failed.
		now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
		adapter := &fakeAdapter{name: "fake", observation: harness.Observation{Found: true}}
		d := newTestDirector(t, adapter, now)
		engagement, err := d.Spawn(context.Background(), SpawnOptions{Task: "investigate", Brief: "look"})
		if err != nil {
			t.Fatalf("Spawn() = %v, want no error", err)
		}

		_, err = d.Read(context.Background(), engagement.ID, ReadOptions{})
		if err == nil {
			t.Fatal("Read() = nil error against a harness with no reader, want a refusal")
		}
		if !strings.Contains(err.Error(), "cannot return") {
			t.Errorf("Read() error = %q, want it to say the harness cannot read", err)
		}
	})

	t.Run("the cursor advances so the next read is a delta", func(t *testing.T) {
		t.Parallel()
		turns := []harness.Turn{{Seq: "1", Role: "assistant", Text: "found it"}}
		d, reader, engagement := newReadingDirector(t, turns, "42")

		if _, err := d.Read(context.Background(), engagement.ID, ReadOptions{SinceLastRead: true}); err != nil {
			t.Fatalf("Read() = %v, want no error", err)
		}
		if got := d.State.Engagements[engagement.ID].Cursor; got != "42" {
			t.Errorf("stored cursor = %q, want %q", got, "42")
		}

		if _, err := d.Read(context.Background(), engagement.ID, ReadOptions{SinceLastRead: true}); err != nil {
			t.Fatalf("Read() = %v, want no error", err)
		}
		if len(reader.reads) != 2 {
			t.Fatalf("adapter saw %d reads, want 2", len(reader.reads))
		}
		if reader.reads[1].Cursor != "42" {
			t.Errorf("second read asked from cursor %q, want %q — otherwise the whole conversation is re-read every time",
				reader.reads[1].Cursor, "42")
		}
	})

	t.Run("two directors reading one engagement do not consume each other's position", func(t *testing.T) {
		t.Parallel()
		// A shared cursor means the second director silently reads nothing,
		// having "already read" what the first consumed — and has no way to
		// notice it happened.
		turns := []harness.Turn{{Seq: "1", Role: "assistant", Text: "found it"}}
		d, _, engagement := newReadingDirector(t, turns, "42")

		if _, err := d.Read(context.Background(), engagement.ID, ReadOptions{SinceLastRead: true}); err != nil {
			t.Fatalf("Read() = %v, want no error", err)
		}

		second, err := Init(d.Roots, "test", "second", func() time.Time { return time.Now() })
		if err != nil {
			t.Fatalf("Init() = %v, want no error", err)
		}
		other, err := Open(d.Roots, second.DirectorID, nil)
		if err != nil {
			t.Fatalf("Open() = %v, want no error", err)
		}
		if len(other.State.Engagements) != 0 {
			t.Fatal("the second director inherited engagements it does not own")
		}
		// Cursors live in each director's own state, so the first director's
		// advance is invisible here — which is the property being asserted.
		if got := d.State.Engagements[engagement.ID].Cursor; got != "42" {
			t.Errorf("the first director's cursor = %q, want it untouched at %q", got, "42")
		}
	})
}

func TestBudget(t *testing.T) {
	t.Parallel()

	turns := func(n int, size int) []harness.Turn {
		out := make([]harness.Turn, n)
		for i := range out {
			out[i] = harness.Turn{Seq: fmt.Sprint(i), Text: strings.Repeat("x", size)}
		}
		return out
	}

	t.Run("output that fits is returned whole", func(t *testing.T) {
		t.Parallel()
		kept, omitted := budget(turns(3, 10), 1000)
		if len(kept) != 3 || omitted != 0 {
			t.Errorf("budget() = %d kept / %d omitted, want 3/0", len(kept), omitted)
		}
	})

	t.Run("the oldest turns are dropped, not the newest", func(t *testing.T) {
		t.Parallel()
		// A director asking what happened wants the end of the story. Dropping
		// the newest would hand back the opening and withhold the conclusion,
		// which is the part that changes what it does next.
		all := turns(10, 100)
		for i := range all {
			all[i].Seq = fmt.Sprint(i)
		}
		kept, omitted := budget(all, 250)
		if omitted == 0 {
			t.Fatal("budget() omitted nothing, want it to trim")
		}
		if kept[len(kept)-1].Seq != "9" {
			t.Errorf("last kept turn = %q, want the newest %q", kept[len(kept)-1].Seq, "9")
		}
		if len(kept)+omitted != 10 {
			t.Errorf("kept %d + omitted %d != 10; the count must account for everything", len(kept), omitted)
		}
	})

	t.Run("a single oversized turn is still returned rather than nothing", func(t *testing.T) {
		t.Parallel()
		// Returning an empty result because one turn exceeded the budget would
		// look identical to the agent having said nothing.
		kept, _ := budget(turns(1, 10_000), 100)
		if len(kept) != 1 {
			t.Errorf("budget() = %d turns, want the one oversized turn kept", len(kept))
		}
	})
}

func TestResume(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

	t.Run("a harness that cannot resume is never quietly respawned", func(t *testing.T) {
		t.Parallel()
		// Spawning instead would create a second conversation with the same
		// title and brief, and the director would then be talking to whichever
		// one it happened to resolve — with nothing in the output to show it.
		adapter := &fakeAdapter{name: "fake", observation: harness.Observation{Found: true}}
		d := newTestDirector(t, adapter, now)
		engagement, err := d.Spawn(context.Background(), SpawnOptions{Task: "investigate", Brief: "look"})
		if err != nil {
			t.Fatalf("Spawn() = %v, want no error", err)
		}
		before := len(d.State.Engagements)

		if _, err := d.Resume(context.Background(), engagement.ID, ResumeOptions{}); err == nil {
			t.Fatal("Resume() = nil error, want a refusal")
		}
		if len(d.State.Engagements) != before {
			t.Error("Resume() created an engagement despite failing")
		}
	})

	t.Run("a fork becomes a new engagement that records where it came from", func(t *testing.T) {
		t.Parallel()
		adapter := &fakeAdapter{name: "fake", observation: harness.Observation{Found: true}}
		d := newTestDirector(t, adapter, now)
		d.Lookup = func(string) (harness.Adapter, error) { return &forkingAdapter{adapter}, nil }

		original, err := d.Spawn(context.Background(), SpawnOptions{Task: "investigate", Brief: "look"})
		if err != nil {
			t.Fatalf("Spawn() = %v, want no error", err)
		}

		forked, err := d.Resume(context.Background(), original.ID, ResumeOptions{Fork: true})
		if err != nil {
			t.Fatalf("Resume() = %v, want no error", err)
		}
		if forked.ID == original.ID {
			t.Error("a fork reused the original's id; two live conversations would share one record")
		}
		if forked.Detail["forked_from"] != original.ID {
			t.Errorf("forked_from = %q, want %q", forked.Detail["forked_from"], original.ID)
		}
		if forked.Token == original.Token {
			t.Error("a fork reused the original's token; either agent could then speak for the other")
		}
	})
}

type forkingAdapter struct{ *fakeAdapter }

func (f *forkingAdapter) Resume(_ context.Context, req harness.ResumeRequest) (harness.SpawnResult, error) {
	if req.Fork {
		return harness.SpawnResult{Ref: "forked-ref"}, nil
	}
	return harness.SpawnResult{Ref: req.Ref}, nil
}

func TestWatchEmitsOnlyChanges(t *testing.T) {
	t.Parallel()
	// A consumer tailing this must never have to dedupe. A status bar that
	// redraws every poll interval whether or not anything happened is the
	// failure this avoids — and it is invisible from a director's point of
	// view, because a director never runs watch.
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	adapter := &fakeAdapter{name: "fake", observation: harness.Observation{
		Found: true, Lifecycle: harness.LifecycleWorking, LastActivityAt: now,
	}}
	d := newTestDirector(t, adapter, now)
	if _, err := d.Spawn(context.Background(), SpawnOptions{Task: "investigate", Brief: "look"}); err != nil {
		t.Fatalf("Spawn() = %v, want no error", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	feed, err := d.Watch(ctx, WatchOptions{Interval: 5 * time.Millisecond, IncludeInitial: true})
	if err != nil {
		t.Fatalf("Watch() = %v, want no error", err)
	}

	first := receive(t, feed)
	if !first.New {
		t.Error("the first sighting of an engagement is not marked new")
	}

	// Nothing has changed, so nothing more may arrive.
	select {
	case extra := <-feed:
		t.Fatalf("Watch() emitted %+v with nothing changed; a consumer would have to dedupe", extra)
	case <-time.After(40 * time.Millisecond):
	}

	adapter.observation.Lifecycle = harness.LifecycleDone
	changed := receive(t, feed)
	if changed.Lifecycle != harness.LifecycleDone {
		t.Errorf("Lifecycle = %v, want %v", changed.Lifecycle, harness.LifecycleDone)
	}
	if changed.Was != first.Health {
		t.Errorf("Was = %q, want the previous health %q so a consumer can tell new problems from old ones",
			changed.Was, first.Health)
	}
}

func receive(t *testing.T, feed <-chan Transition) Transition {
	t.Helper()
	select {
	case transition, ok := <-feed:
		if !ok {
			t.Fatal("the feed closed unexpectedly")
		}
		return transition
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a transition")
		return Transition{}
	}
}

func TestWatchStopsOnCancel(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	adapter := &fakeAdapter{name: "fake", observation: harness.Observation{Found: true}}
	d := newTestDirector(t, adapter, now)

	ctx, cancel := context.WithCancel(context.Background())
	feed, err := d.Watch(ctx, WatchOptions{Interval: 5 * time.Millisecond})
	if err != nil {
		t.Fatalf("Watch() = %v, want no error", err)
	}
	cancel()

	select {
	case _, ok := <-feed:
		if ok {
			// Draining is fine; the channel must close eventually.
			for range feed {
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the feed did not close after the context was cancelled")
	}
}
