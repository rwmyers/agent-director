package director

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rwmyers/agent-director/harness"
)

// locatingAdapter is a fake that also answers where this process is sitting,
// which is the one question the plain fake cannot answer and the whole basis
// for telling a director's own removal apart from somebody else's.
type locatingAdapter struct {
	*fakeAdapter
	// here is the conversation this process is in, empty for none.
	here string
}

func (a *locatingAdapter) Locate() (string, bool) {
	return a.here, a.here != ""
}

func (a *locatingAdapter) Hosts() harness.Hosting {
	return harness.Hosting{Background: true, Wake: true}
}

// removable builds a wakeable director holding one finished engagement, and
// says where the process running the removal is sitting.
//
// The engagement is reported as done so that Remove does not refuse it: this
// file is about who gets told, not about the live-agent guard.
func removable(t *testing.T, now time.Time, runningIn string) (*Director, *locatingAdapter, *Engagement) {
	t.Helper()
	base := &fakeAdapter{
		name: "fake",
		observation: harness.Observation{
			Found: true, Lifecycle: harness.LifecycleDone, LastActivityAt: now,
		},
	}
	d := newTestDirector(t, base, now)
	adapter := &locatingAdapter{fakeAdapter: base, here: runningIn}
	d.Lookup = func(name string) (harness.Adapter, error) {
		if name != base.name {
			return nil, errors.New("no such harness: " + name)
		}
		return adapter, nil
	}
	engagement := spawnOne(t, d)

	setHost(t, d, Host{
		Harness: "fake",
		Ref:     "w6:p1",
		Hosting: harness.Hosting{Background: true, Wake: true},
		Source:  HostDetected,
	})
	if err := d.mutate(func(state *State) error {
		state.AttachedAt = now
		return nil
	}); err != nil {
		t.Fatalf("mutate() = %v, want no error", err)
	}
	return d, adapter, engagement
}

// removeOne drops an engagement and returns the result, failing on anything else.
func removeOne(t *testing.T, d *Director, id string) *RemoveResult {
	t.Helper()
	result, err := d.Remove(context.Background(), id, RemoveOptions{})
	if err != nil {
		t.Fatalf("Remove(%s) = %v, want no error", id, err)
	}
	return result
}

func TestRemovalRingsAnAttachedDirector(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	// A shell: somewhere that is not the director's own conversation.
	d, adapter, engagement := removable(t, now, "")
	result := removeOne(t, d, engagement.ID)

	if err := d.NotifyRemoved(context.Background(), []*RemoveResult{result}); err != nil {
		t.Fatalf("NotifyRemoved() = %v, want a ring", err)
	}

	sent := wakes(adapter.fakeAdapter)
	if len(sent) != 1 {
		t.Fatalf("wakes = %d, want exactly one", len(sent))
	}
	// What the director needs to act rather than merely be told: which row
	// went, that it was not its own doing, and where the truth now is.
	for _, want := range []string{engagement.ID, engagement.Title, "Somebody other than you", "director status"} {
		if !strings.Contains(sent[0].Text, want) {
			t.Errorf("ring = %q, want it to contain %q", sent[0].Text, want)
		}
	}
	if d.State.Host.LastWokenAt.IsZero() {
		t.Error("the ring was not recorded, so the floor for ordinary wakes would not count it")
	}
}

func TestADirectorIsNotRungAboutItsOwnRemoval(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	// Running in the very pane the director recorded as its own seat.
	d, adapter, engagement := removable(t, now, "w6:p1")
	result := removeOne(t, d, engagement.ID)

	err := d.NotifyRemoved(context.Background(), []*RemoveResult{result})
	if !errors.Is(err, ErrOwnRemoval) {
		t.Errorf("NotifyRemoved() = %v, want ErrOwnRemoval", err)
	}
	if sent := wakes(adapter.fakeAdapter); len(sent) != 0 {
		t.Errorf("wakes = %d (%v), want none: a director must not be told what it just did", len(sent), sent)
	}
}

func TestAnotherConversationInTheSameHarnessIsNotTheDirector(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	// Same harness, different pane. Matching on the harness alone would have
	// silenced this, which is the whole reason the address is compared.
	d, adapter, engagement := removable(t, now, "w9:p4")
	result := removeOne(t, d, engagement.ID)

	if err := d.NotifyRemoved(context.Background(), []*RemoveResult{result}); err != nil {
		t.Fatalf("NotifyRemoved() = %v, want a ring", err)
	}
	if sent := wakes(adapter.fakeAdapter); len(sent) != 1 {
		t.Errorf("wakes = %d, want exactly one", len(sent))
	}
}

func TestRemovalDoesNotRingAHostThatCannotBeWoken(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	d, adapter, engagement := removable(t, now, "")
	// What Claude Code declares: it can background a command and be re-entered
	// when it exits, and nothing can push a turn into it.
	setHost(t, d, Host{
		Harness: "fake",
		Ref:     "session-1",
		Hosting: harness.Hosting{Background: true, Wake: false},
		Source:  HostDetected,
	})
	result := removeOne(t, d, engagement.ID)

	err := d.NotifyRemoved(context.Background(), []*RemoveResult{result})
	if !errors.Is(err, ErrNoWake) {
		t.Fatalf("NotifyRemoved() = %v, want ErrNoWake", err)
	}
	// The refusal has to be sayable to the person at the console, so it names
	// the host rather than only the fact.
	if !strings.Contains(err.Error(), "fake") {
		t.Errorf("NotifyRemoved() = %q, want it to name the host", err)
	}
	if sent := adapter.sends; len(sent) != 0 {
		t.Errorf("sends = %d (%v), want none", len(sent), sent)
	}
}

func TestRemovalIsSilentWhenNobodyIsAttached(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)

	t.Run("no host was ever recorded", func(t *testing.T) {
		t.Parallel()
		d, adapter, engagement := removable(t, now, "")
		setHost(t, d, Host{Source: HostUnknown})
		result := removeOne(t, d, engagement.ID)

		if err := d.NotifyRemoved(context.Background(), []*RemoveResult{result}); !errors.Is(err, ErrNotAttached) {
			t.Errorf("NotifyRemoved() = %v, want ErrNotAttached", err)
		}
		if sent := adapter.sends; len(sent) != 0 {
			t.Errorf("sends = %d, want none", len(sent))
		}
	})

	t.Run("the claim is older than the grace", func(t *testing.T) {
		t.Parallel()
		d, adapter, engagement := removable(t, now, "")
		if err := d.mutate(func(state *State) error {
			state.AttachedAt = now.Add(-AttachGrace - time.Minute)
			return nil
		}); err != nil {
			t.Fatalf("mutate() = %v, want no error", err)
		}
		result := removeOne(t, d, engagement.ID)

		// Whoever attached has walked away. Typing into that address is at
		// best noise and at worst somebody else's screen.
		if err := d.NotifyRemoved(context.Background(), []*RemoveResult{result}); !errors.Is(err, ErrNotAttached) {
			t.Errorf("NotifyRemoved() = %v, want ErrNotAttached", err)
		}
		if sent := wakes(adapter.fakeAdapter); len(sent) != 0 {
			t.Errorf("wakes = %d, want none", len(sent))
		}
	})
}

func TestABatchOfRemovalsIsOneRing(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	d, adapter, first := removable(t, now, "")

	var results []*RemoveResult
	results = append(results, removeOne(t, d, first.ID))
	for i := 0; i < 4; i++ {
		results = append(results, removeOne(t, d, spawnOne(t, d).ID))
	}

	if err := d.NotifyRemoved(context.Background(), results); err != nil {
		t.Fatalf("NotifyRemoved() = %v, want a ring", err)
	}
	sent := wakes(adapter.fakeAdapter)
	if len(sent) != 1 {
		t.Fatalf("wakes = %d, want exactly one for one invocation", len(sent))
	}
	if !strings.Contains(sent[0].Text, "5 engagements") {
		t.Errorf("ring = %q, want it to count all five", sent[0].Text)
	}
	// Named in full up to the cap, counted after it: clearing a long batch
	// must not paste the whole list into somebody's conversation.
	if !strings.Contains(sent[0].Text, "and 2 more") {
		t.Errorf("ring = %q, want the rest carried as a count", sent[0].Text)
	}
	for _, result := range results[namedInRemoval:] {
		if strings.Contains(sent[0].Text, result.EngagementID) {
			t.Errorf("ring = %q, want it to stop naming after %d", sent[0].Text, namedInRemoval)
		}
	}
}

func TestRemovingNothingRingsNobody(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	d, adapter, _ := removable(t, now, "")

	if err := d.NotifyRemoved(context.Background(), nil); !errors.Is(err, ErrNothingToWakeFor) {
		t.Errorf("NotifyRemoved(nil) = %v, want ErrNothingToWakeFor", err)
	}
	if sent := adapter.sends; len(sent) != 0 {
		t.Errorf("sends = %d, want none", len(sent))
	}
}
