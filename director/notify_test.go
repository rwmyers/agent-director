package director

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rwmyers/agent-director/harness"
)

// wakeable builds a director whose host is a harness that can be rung, and
// returns the adapter the wake would land on.
//
// The host harness and the engagement harness are the same fake here only for
// convenience: nothing in the core connects them, and an engagement in one
// harness waking a director sitting in another is the ordinary case.
func wakeable(t *testing.T, now time.Time) (*Director, *fakeAdapter) {
	t.Helper()
	adapter := &fakeAdapter{
		name: "fake",
		observation: harness.Observation{
			Found: true, Lifecycle: harness.LifecycleWorking, LastActivityAt: now,
		},
	}
	d := newTestDirector(t, adapter, now)
	d.State.Host = Host{
		Harness: "fake",
		Ref:     "w6:p1",
		Hosting: harness.Hosting{Background: true, Wake: true},
		Source:  HostDetected,
	}
	d.State.AttachedAt = now
	if err := d.State.Save(); err != nil {
		t.Fatalf("Save() = %v, want no error", err)
	}
	return d, adapter
}

// setHost writes a host to the state file as well as holding it, because every
// state mutation re-reads the file: an in-memory host would be thrown away by
// the next spawn or report.
func setHost(t *testing.T, d *Director, host Host) {
	t.Helper()
	if err := d.mutate(func(state *State) error {
		state.Host = host
		return nil
	}); err != nil {
		t.Fatalf("mutate() = %v, want no error", err)
	}
}

func spawnOne(t *testing.T, d *Director) *Engagement {
	t.Helper()
	engagement, err := d.Spawn(context.Background(), SpawnOptions{Task: "investigate", Brief: "look"})
	if err != nil {
		t.Fatalf("Spawn() = %v, want no error", err)
	}
	return engagement
}

// wakes returns the sends that were addressed at the director's own host,
// which is what separates a wake from a nudge or a prompt to an engagement.
func wakes(adapter *fakeAdapter) []harness.SendRequest {
	var found []harness.SendRequest
	for _, send := range adapter.sends {
		if send.Ref == "w6:p1" {
			found = append(found, send)
		}
	}
	return found
}

func TestAskWakesTheDirector(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	d, adapter := wakeable(t, now)
	engagement := spawnOne(t, d)

	if _, err := d.Ask(context.Background(), engagement.ID, engagement.Token, "May I force-push?"); err != nil {
		t.Fatalf("Ask() = %v, want no error", err)
	}

	sent := wakes(adapter)
	if len(sent) != 1 {
		t.Fatalf("wakes = %d, want exactly one", len(sent))
	}
	// The line points at the record rather than being the record, so a wake
	// that is garbled or delivered twice costs nothing.
	if !strings.Contains(sent[0].Text, engagement.ID) || !strings.Contains(sent[0].Text, "director status") {
		t.Errorf("wake text = %q, want the engagement id and a pointer at status", sent[0].Text)
	}
	if d.State.Host.LastWokenAt.IsZero() {
		t.Error("the wake was not recorded, so the floor would never apply")
	}
}

func TestReportWakesOnlyWhenThereIsNews(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

	t.Run("ordinary progress does not wake anybody", func(t *testing.T) {
		t.Parallel()
		// An agent working through its progress vocabulary is exactly what the
		// director expects. Waking on it would type a line into the
		// conversation for every heartbeat of every engagement.
		d, adapter := wakeable(t, now)
		engagement := spawnOne(t, d)

		if _, err := d.Report(context.Background(), engagement.ID, engagement.Token, "reading", "halfway"); err != nil {
			t.Fatalf("Report() = %v, want no error", err)
		}
		if sent := wakes(adapter); len(sent) != 0 {
			t.Errorf("wakes = %d (%v), want none", len(sent), sent)
		}
	})

	t.Run("terminal progress wakes even while the process is still alive", func(t *testing.T) {
		t.Parallel()
		// Health only calls an engagement complete once the process is gone, so
		// without this the single most useful thing an agent ever says — I am
		// finished — would never ring anybody.
		d, adapter := wakeable(t, now)
		engagement := spawnOne(t, d)

		if _, err := d.Report(context.Background(), engagement.ID, engagement.Token, "delivered", "done"); err != nil {
			t.Fatalf("Report() = %v, want no error", err)
		}
		if sent := wakes(adapter); len(sent) != 1 {
			t.Fatalf("wakes = %d, want exactly one", len(sent))
		}
	})
}

func TestNotifyGuards(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

	t.Run("a host that declares no wake is not rung", func(t *testing.T) {
		t.Parallel()
		d, adapter := wakeable(t, now)
		engagement := spawnOne(t, d)
		host := d.State.Host
		host.Hosting.Wake = false
		setHost(t, d, host)

		err := d.notify(context.Background(), engagement.ID)
		if !errors.Is(err, ErrNoWake) {
			t.Errorf("notify() = %v, want ErrNoWake", err)
		}
		if sent := wakes(adapter); len(sent) != 0 {
			t.Errorf("wakes = %d, want none", len(sent))
		}
	})

	t.Run("a host with no recorded address is not rung", func(t *testing.T) {
		t.Parallel()
		d, _ := wakeable(t, now)
		engagement := spawnOne(t, d)
		host := d.State.Host
		host.Ref = ""
		setHost(t, d, host)

		if err := d.notify(context.Background(), engagement.ID); !errors.Is(err, ErrNoWake) {
			t.Errorf("notify() = %v, want ErrNoWake", err)
		}
	})

	t.Run("a stale claim is not rung", func(t *testing.T) {
		t.Parallel()
		// Nobody is sitting there. Typing into it is at best noise, and at
		// worst it is somebody else's screen now.
		d, adapter := wakeable(t, now)
		engagement := spawnOne(t, d)
		d.Clock = func() time.Time { return now.Add(AttachGrace + time.Minute) }

		if err := d.notify(context.Background(), engagement.ID); !errors.Is(err, ErrStaleHost) {
			t.Errorf("notify() = %v, want ErrStaleHost", err)
		}
		if sent := wakes(adapter); len(sent) != 0 {
			t.Errorf("wakes = %d, want none", len(sent))
		}
	})

	t.Run("a second wake inside the floor is dropped", func(t *testing.T) {
		t.Parallel()
		// The task reports every 5m, so that is the floor: a fleet reporting in
		// unison must not type a dozen lines into a live conversation.
		d, adapter := wakeable(t, now)
		first := spawnOne(t, d)
		second := spawnOne(t, d)

		if _, err := d.Ask(context.Background(), first.ID, first.Token, "one?"); err != nil {
			t.Fatalf("Ask() = %v, want no error", err)
		}
		if _, err := d.Ask(context.Background(), second.ID, second.Token, "two?"); err != nil {
			t.Fatalf("Ask() = %v, want no error", err)
		}
		if sent := wakes(adapter); len(sent) != 1 {
			t.Fatalf("wakes = %d, want one — the second is inside the floor", len(sent))
		}

		// Past the floor, the next piece of news gets through.
		d.Clock = func() time.Time { return now.Add(6 * time.Minute) }
		if err := d.notify(context.Background(), second.ID); err != nil {
			t.Fatalf("notify() past the floor = %v, want a wake", err)
		}
		if sent := wakes(adapter); len(sent) != 2 {
			t.Errorf("wakes = %d, want two", len(sent))
		}
	})

	t.Run("a dropped wake still leaves the news in the state file", func(t *testing.T) {
		t.Parallel()
		// The whole contract: with the wake gone, behaviour is exactly what it
		// was before any of this existed.
		d, _ := wakeable(t, now)
		engagement := spawnOne(t, d)
		setHost(t, d, Host{Source: HostUnknown})

		ask, err := d.Ask(context.Background(), engagement.ID, engagement.Token, "May I force-push?")
		if err != nil {
			t.Fatalf("Ask() = %v, want no error", err)
		}
		reloaded, err := LoadState(d.State.Path)
		if err != nil {
			t.Fatalf("LoadState() = %v, want no error", err)
		}
		if reloaded.Asks[ask.ID] == nil {
			t.Fatal("the question was not recorded, so a lost wake would lose it entirely")
		}
		if pending := reloaded.PendingAskFor(engagement.ID); pending == nil {
			t.Error("status would not show the engagement as blocked")
		}
	})

	t.Run("a send that fails does not fail the report", func(t *testing.T) {
		t.Parallel()
		// herdr tolerates a stalled prompt into a busy pane and reports
		// success, so a wake going nowhere is ordinary. An agent whose report
		// was written must never be told it failed.
		d, adapter := wakeable(t, now)
		engagement := spawnOne(t, d)
		adapter.sendErr = errors.New("the pane is gone")

		if _, err := d.Report(context.Background(), engagement.ID, engagement.Token, "delivered", "done"); err != nil {
			t.Fatalf("Report() = %v, want no error despite the failed wake", err)
		}
		// And it is recorded anyway, so a broken address does not fill the
		// conversation with identical retries.
		if d.State.Host.LastWokenAt.IsZero() {
			t.Error("a failed wake was not recorded, so it would be retried on every report")
		}
	})
}
