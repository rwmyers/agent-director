package director

import (
	"testing"
	"time"

	"github.com/rwmyers/agent-director/harness"
)

// The health verdict is the thing a director acts on, so it is tested as the
// pure function it is: an injected clock and a fabricated input, no sleeping
// and no processes. Every case here is one a director would otherwise have to
// work out from raw timestamps, and several of them look identical until the
// activity signal is taken into account.
func TestDeriveHealth(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	task := Task{
		Name:     "investigate",
		Progress: []string{"orienting", "reading", "delivered"},
		Terminal: "delivered",
		ReportOn: ReportOn{OnProgressChange: true, Every: 5 * time.Minute},
	}
	// StallThreshold is three missed heartbeats.
	if got, want := task.StallThreshold(), 15*time.Minute; got != want {
		t.Fatalf("StallThreshold() = %v, want %v", got, want)
	}

	input := func(mutate func(*healthInput)) healthInput {
		in := healthInput{
			lifecycle:    harness.LifecycleWorking,
			task:         task,
			startedAt:    base,
			lastReportAt: base,
			now:          base,
		}
		mutate(&in)
		return in
	}

	t.Run("an agent reporting on time is ok", func(t *testing.T) {
		t.Parallel()
		in := input(func(in *healthInput) {
			in.now = base.Add(2 * time.Minute)
			in.lastActivityAt = base.Add(90 * time.Second)
		})
		if got := deriveHealth(in); got != HealthOK {
			t.Errorf("deriveHealth() = %v, want %v", got, HealthOK)
		}
	})

	t.Run("an agent that stopped reporting but is still visibly working is quiet, not stalled", func(t *testing.T) {
		t.Parallel()
		// Eight minutes of silence against a five minute contract, but the
		// harness saw it do something a minute ago. This is the case that
		// separates a long tool call from a wedged process, and getting it
		// wrong means either nagging healthy agents or ignoring dead ones.
		in := input(func(in *healthInput) {
			in.now = base.Add(8 * time.Minute)
			in.lastActivityAt = base.Add(7 * time.Minute)
		})
		if got := deriveHealth(in); got != HealthQuiet {
			t.Errorf("deriveHealth() = %v, want %v", got, HealthQuiet)
		}
	})

	t.Run("an agent with neither reports nor activity past the threshold is stalled", func(t *testing.T) {
		t.Parallel()
		in := input(func(in *healthInput) {
			in.now = base.Add(20 * time.Minute)
			in.lastActivityAt = base.Add(1 * time.Minute)
		})
		if got := deriveHealth(in); got != HealthStalled {
			t.Errorf("deriveHealth() = %v, want %v", got, HealthStalled)
		}
	})

	t.Run("a harness that supplies no activity clock degrades toward stalled, never toward ok", func(t *testing.T) {
		t.Parallel()
		// With no second signal there is not enough evidence to say the agent
		// is fine. A needless nudge costs one turn; an ignored dead agent costs
		// the whole engagement, so the doubt resolves toward looking.
		early := input(func(in *healthInput) { in.now = base.Add(8 * time.Minute) })
		if got := deriveHealth(early); got != HealthUnknown {
			t.Errorf("deriveHealth() with no activity clock = %v, want %v", got, HealthUnknown)
		}
		late := input(func(in *healthInput) { in.now = base.Add(20 * time.Minute) })
		if got := deriveHealth(late); got != HealthStalled {
			t.Errorf("deriveHealth() past the threshold = %v, want %v", got, HealthStalled)
		}
	})

	t.Run("a pending question outranks everything else", func(t *testing.T) {
		t.Parallel()
		// Even an agent that looks perfectly healthy is blocked if it asked,
		// and even one that looks dead is blocked rather than abandoned —
		// nudging or writing it off would both be wrong.
		healthy := input(func(in *healthInput) {
			in.hasPendingAsk = true
			in.lastActivityAt = base
		})
		if got := deriveHealth(healthy); got != HealthBlocked {
			t.Errorf("deriveHealth() = %v, want %v", got, HealthBlocked)
		}
		finished := input(func(in *healthInput) {
			in.hasPendingAsk = true
			in.lifecycle = harness.LifecycleDone
			in.progress = "delivered"
		})
		if got := deriveHealth(finished); got != HealthBlocked {
			t.Errorf("deriveHealth() on a finished-but-asking engagement = %v, want %v", got, HealthBlocked)
		}
	})

	t.Run("a process that ended short of terminal progress is abandoned, not complete", func(t *testing.T) {
		t.Parallel()
		// The silent-failure case. An agent whose terminal was closed looks
		// exactly like one that finished, and the only thing distinguishing
		// them is whether the work reached its declared finish line.
		in := input(func(in *healthInput) {
			in.lifecycle = harness.LifecycleDone
			in.progress = "reading"
			in.now = base.Add(time.Hour)
		})
		if got := deriveHealth(in); got != HealthAbandoned {
			t.Errorf("deriveHealth() = %v, want %v", got, HealthAbandoned)
		}
	})

	t.Run("a process that ended at terminal progress is complete", func(t *testing.T) {
		t.Parallel()
		in := input(func(in *healthInput) {
			in.lifecycle = harness.LifecycleDone
			in.progress = "delivered"
			in.now = base.Add(time.Hour)
		})
		if got := deriveHealth(in); got != HealthComplete {
			t.Errorf("deriveHealth() = %v, want %v", got, HealthComplete)
		}
	})

	t.Run("an idle engagement at terminal progress is complete even after the stall threshold", func(t *testing.T) {
		t.Parallel()
		// In session-based harnesses, reaching the finish line leaves the
		// session idle rather than terminating the process. Once the declared
		// terminal progress is reached and no turn is in flight, the work is
		// complete and must not be marked stalled by silence.
		in := input(func(in *healthInput) {
			in.lifecycle = harness.LifecycleIdle
			in.progress = "delivered"
			in.now = base.Add(time.Hour)
		})
		if got := deriveHealth(in); got != HealthComplete {
			t.Errorf("deriveHealth() = %v, want %v", got, HealthComplete)
		}
	})

	t.Run("an engagement at terminal progress that is actively working a turn is ok", func(t *testing.T) {
		t.Parallel()
		in := input(func(in *healthInput) {
			in.lifecycle = harness.LifecycleWorking
			in.progress = "delivered"
			in.now = base.Add(time.Hour)
		})
		if got := deriveHealth(in); got != HealthOK {
			t.Errorf("deriveHealth() = %v, want %v", got, HealthOK)
		}
	})

	t.Run("a task with no finish line cannot be accused of abandoning the work", func(t *testing.T) {
		t.Parallel()
		open := task
		open.Terminal = ""
		in := input(func(in *healthInput) {
			in.task = open
			in.lifecycle = harness.LifecycleDone
			in.now = base.Add(time.Hour)
		})
		if got := deriveHealth(in); got != HealthComplete {
			t.Errorf("deriveHealth() = %v, want %v", got, HealthComplete)
		}
	})

	t.Run("a freshly spawned engagement is not judged on silence", func(t *testing.T) {
		t.Parallel()
		in := input(func(in *healthInput) {
			in.lifecycle = harness.LifecycleStarting
			in.lastReportAt = time.Time{}
			in.now = base.Add(10 * time.Second)
		})
		if got := deriveHealth(in); got != HealthOK {
			t.Errorf("deriveHealth() = %v, want %v", got, HealthOK)
		}
	})

	t.Run("an engagement stuck starting past the threshold is stalled", func(t *testing.T) {
		t.Parallel()
		in := input(func(in *healthInput) {
			in.lifecycle = harness.LifecycleStarting
			in.lastReportAt = time.Time{}
			in.now = base.Add(30 * time.Minute)
		})
		if got := deriveHealth(in); got != HealthStalled {
			t.Errorf("deriveHealth() = %v, want %v", got, HealthStalled)
		}
	})

	t.Run("an agent that never reported is measured from when it was spawned", func(t *testing.T) {
		t.Parallel()
		// Otherwise an agent that never speaks has been silent for zero
		// seconds forever, and would never be noticed at all.
		in := input(func(in *healthInput) {
			in.lastReportAt = time.Time{}
			in.lastActivityAt = base
			in.now = base.Add(20 * time.Minute)
		})
		if got := deriveHealth(in); got != HealthStalled {
			t.Errorf("deriveHealth() = %v, want %v", got, HealthStalled)
		}
	})

	t.Run("a task that asked for no reports is judged only on activity", func(t *testing.T) {
		t.Parallel()
		quiet := task
		quiet.ReportOn = ReportOn{Never: true}
		in := input(func(in *healthInput) {
			in.task = quiet
			in.lastReportAt = time.Time{}
			in.lastActivityAt = base.Add(4 * time.Minute)
			in.now = base.Add(5 * time.Minute)
		})
		if got := deriveHealth(in); got != HealthOK {
			t.Errorf("deriveHealth() = %v, want %v", got, HealthOK)
		}
	})
}

func TestHealthNeedsDirector(t *testing.T) {
	t.Parallel()

	// The set a director should be checking every turn. ok and quiet are
	// deliberately absent: an agent that is working, or working and not
	// talking, does not want interrupting.
	needs := map[Health]bool{
		HealthOK:        false,
		HealthQuiet:     false,
		HealthUnknown:   false,
		HealthStalled:   true,
		HealthBlocked:   true,
		HealthAbandoned: true,
		HealthComplete:  true,
	}
	for health, want := range needs {
		if got := health.NeedsDirector(); got != want {
			t.Errorf("Health(%q).NeedsDirector() = %t, want %t", health, got, want)
		}
	}
}
