package director

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rwmyers/agent-director/harness"
	"github.com/rwmyers/agent-director/harness/herdr"
)

// fakeAdapter records what the core asked it to do, so a test can assert that
// the core asked for nothing more — a spawn that quietly proceeded past a
// refused permission check would otherwise pass every other assertion.
type fakeAdapter struct {
	name        string
	permitErr   error
	spawnErr    error
	spawns      []harness.SpawnRequest
	sends       []harness.SendRequest
	observation harness.Observation
}

func (f *fakeAdapter) Name() string                                    { return f.name }
func (f *fakeAdapter) Enforceable() []harness.Capability               { return harness.AllCapabilities }
func (f *fakeAdapter) Permits(_ []harness.Capability) error            { return f.permitErr }
func (f *fakeAdapter) Stop(context.Context, harness.StopRequest) error { return nil }

func (f *fakeAdapter) Spawn(_ context.Context, req harness.SpawnRequest) (harness.SpawnResult, error) {
	f.spawns = append(f.spawns, req)
	if f.spawnErr != nil {
		return harness.SpawnResult{}, f.spawnErr
	}
	return harness.SpawnResult{Ref: "ref-" + req.ID}, nil
}

func (f *fakeAdapter) Send(_ context.Context, req harness.SendRequest) error {
	f.sends = append(f.sends, req)
	return nil
}

func (f *fakeAdapter) Get(context.Context, string) (harness.Observation, error) {
	return f.observation, nil
}

func (f *fakeAdapter) List(context.Context, harness.Filter) ([]harness.Observation, error) {
	return []harness.Observation{f.observation}, nil
}

// newTestDirector builds a director over a temporary root with one workflow.
func newTestDirector(t *testing.T, adapter *fakeAdapter, now time.Time) *Director {
	t.Helper()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "prompts", "task.md"), "Do the thing.")
	writeFile(t, filepath.Join(root, "workflows", "test.conf"), `
description = test

[permission.read-only]
allow = read, search

[task.investigate]
prompt      = ../prompts/task.md
permissions = read-only
progress    = orienting, reading, delivered
terminal    = delivered
report_on   = progress-change, 5m
`)

	roots := Roots{Primary: root, Layers: []Layer{{Kind: LayerProject, Path: root}}}
	clock := func() time.Time { return now }

	if _, err := Init(roots, "test", "tester", clock); err != nil {
		t.Fatalf("Init() = %v, want no error", err)
	}
	d, err := Open(roots, "", clock)
	if err != nil {
		t.Fatalf("Open() = %v, want no error", err)
	}
	d.Config.Harness = adapter.name
	d.Lookup = func(name string) (harness.Adapter, error) {
		if name != adapter.name {
			return nil, errors.New("no such harness: " + name)
		}
		return adapter, nil
	}
	// Placement must not depend on where the suite happens to be running. A
	// director inside a herdr pane spawns into panes, and a test that inherited
	// that would pass or fail according to whose terminal ran it.
	d.InPane = func() (string, bool) { return "", false }
	return d
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAFailedSpawnLeavesNoRecordBehind(t *testing.T) {
	t.Parallel()
	// The record is written before the adapter is called so that a crash
	// between the two is recoverable. A returned error is not that case: the
	// adapter got far enough to report, and the record it leaves has an empty
	// Ref, so it names nothing that can be read, stopped or resumed. It shows
	// up in `status` as stalled with an unknown lifecycle — indistinguishable
	// from an agent genuinely lost — and can only be cleared by hand.
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	adapter := &fakeAdapter{name: "fake", spawnErr: errors.New("herdr created a tab but reported no pane")}
	d := newTestDirector(t, adapter, now)

	if _, err := d.Spawn(context.Background(), SpawnOptions{Task: "investigate", Brief: "look"}); err == nil {
		t.Fatal("Spawn() = nil error, want the adapter's failure reported")
	}

	if len(d.State.Engagements) != 0 {
		t.Errorf("Engagements = %v, want none left behind by a spawn that failed", d.State.Engagements)
	}

	// And it must be gone from disk too, not just from this process's copy.
	reopened, err := Open(d.Roots, d.State.DirectorID, func() time.Time { return now })
	if err != nil {
		t.Fatalf("Open() = %v, want no error", err)
	}
	if len(reopened.State.Engagements) != 0 {
		t.Errorf("Engagements after reopening = %v, want none", reopened.State.Engagements)
	}
}

func TestSpawn(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

	t.Run("a harness that cannot enforce the permission scope is refused before anything starts", func(t *testing.T) {
		t.Parallel()
		// The safety-critical case. A permission system that runs the agent
		// anyway and warns about it is worse than none, because the warning is
		// lost and the access is not.
		adapter := &fakeAdapter{name: "fake", permitErr: errors.New("cannot deny push")}
		d := newTestDirector(t, adapter, now)

		_, err := d.Spawn(context.Background(), SpawnOptions{Task: "investigate", Brief: "look"})
		if err == nil {
			t.Fatal("Spawn() = nil error, want a refusal")
		}
		if !strings.Contains(err.Error(), "cannot deny push") {
			t.Errorf("Spawn() error = %q, want it to name what could not be enforced", err)
		}
		if len(adapter.spawns) != 0 {
			t.Errorf("Spawn() called the adapter %d times, want 0 — nothing may start after a refusal", len(adapter.spawns))
		}
	})

	t.Run("the agent is given what it needs to talk back", func(t *testing.T) {
		t.Parallel()
		adapter := &fakeAdapter{name: "fake"}
		d := newTestDirector(t, adapter, now)

		engagement, err := d.Spawn(context.Background(), SpawnOptions{Task: "investigate", Brief: "look at it"})
		if err != nil {
			t.Fatalf("Spawn() = %v, want no error", err)
		}
		req := adapter.spawns[0]

		for _, key := range []string{EnvRoot, EnvID, EnvEngagement, EnvToken, EnvTask, EnvProgress} {
			if req.Env[key] == "" {
				t.Errorf("Spawn() did not inject %s; without it the agent cannot report at all", key)
			}
		}
		if req.Env[EnvEngagement] != engagement.ID {
			t.Errorf("injected engagement = %q, want %q", req.Env[EnvEngagement], engagement.ID)
		}
		if !strings.Contains(req.Prompt, "director report") {
			t.Error("Spawn() prompt does not mention how to report; the reporting contract is the only thing that tells the agent the callback exists")
		}
		if !strings.Contains(req.Prompt, "look at it") {
			t.Error("Spawn() prompt does not contain the brief")
		}
		if !strings.Contains(req.Prompt, "Do the thing.") {
			t.Error("Spawn() prompt does not contain the task's own prompt")
		}
	})

}

func TestReport(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

	spawn := func(t *testing.T) (*Director, *Engagement) {
		t.Helper()
		adapter := &fakeAdapter{name: "fake", observation: harness.Observation{
			Found: true, Lifecycle: harness.LifecycleWorking,
		}}
		d := newTestDirector(t, adapter, now)
		engagement, err := d.Spawn(context.Background(), SpawnOptions{Task: "investigate", Brief: "look"})
		if err != nil {
			t.Fatalf("Spawn() = %v, want no error", err)
		}
		return d, engagement
	}

	t.Run("an engagement's own token is accepted", func(t *testing.T) {
		t.Parallel()
		d, engagement := spawn(t)
		if _, err := d.Report(engagement.ID, engagement.Token, "reading", "halfway"); err != nil {
			t.Fatalf("Report() = %v, want no error", err)
		}
		if got := d.State.Engagements[engagement.ID].Progress; got != "reading" {
			t.Errorf("progress = %q, want %q", got, "reading")
		}
	})

	t.Run("another engagement's token is refused", func(t *testing.T) {
		t.Parallel()
		// Without this an agent could move a sibling's progress or answer for
		// it, and nothing in the output would reveal that it had happened.
		d, engagement := spawn(t)
		_, err := d.Report(engagement.ID, "not-the-right-token", "reading", "")
		if err == nil {
			t.Fatal("Report() = nil error, want a rejection")
		}
		if !strings.Contains(err.Error(), "token") {
			t.Errorf("Report() error = %q, want it to name the token", err)
		}
	})

	t.Run("progress outside the task's vocabulary is refused, and the valid set is named", func(t *testing.T) {
		t.Parallel()
		d, engagement := spawn(t)
		_, err := d.Report(engagement.ID, engagement.Token, "vibing", "")
		if err == nil {
			t.Fatal("Report() = nil error, want a rejection")
		}
		if !strings.Contains(err.Error(), "orienting") {
			t.Errorf("Report() error = %q, want it to list the valid values", err)
		}
	})

	t.Run("a report clears a previous nudge", func(t *testing.T) {
		t.Parallel()
		// So the next time this engagement goes quiet the director is told
		// "stalled and untried" rather than "stalled and already poked", which
		// are different situations calling for different actions.
		d, engagement := spawn(t)
		if err := d.Nudge(context.Background(), engagement.ID); err != nil {
			t.Fatalf("Nudge() = %v, want no error", err)
		}
		if d.State.Engagements[engagement.ID].NudgedAt.IsZero() {
			t.Fatal("Nudge() did not record when it happened")
		}
		if _, err := d.Report(engagement.ID, engagement.Token, "reading", ""); err != nil {
			t.Fatalf("Report() = %v, want no error", err)
		}
		if !d.State.Engagements[engagement.ID].NudgedAt.IsZero() {
			t.Error("Report() left the nudge recorded; a sign of life should clear it")
		}
	})
}

func TestAskBlocksAndAnswerReleases(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

	adapter := &fakeAdapter{name: "fake", observation: harness.Observation{
		Found: true, Lifecycle: harness.LifecycleWorking, LastActivityAt: now,
	}}
	d := newTestDirector(t, adapter, now)
	engagement, err := d.Spawn(context.Background(), SpawnOptions{Task: "investigate", Brief: "look"})
	if err != nil {
		t.Fatalf("Spawn() = %v, want no error", err)
	}

	ask, err := d.Ask(engagement.ID, engagement.Token, "May I force-push?")
	if err != nil {
		t.Fatalf("Ask() = %v, want no error", err)
	}

	t.Run("an engagement with a pending question reads as blocked", func(t *testing.T) {
		// This is what makes blocked honest: no harness reports "waiting on a
		// human", so before the ask channel existed the state was guessed.
		observed, err := d.Get(context.Background(), engagement.ID)
		if err != nil {
			t.Fatalf("Get() = %v, want no error", err)
		}
		if observed.Health != HealthBlocked {
			t.Errorf("Health = %v, want %v", observed.Health, HealthBlocked)
		}
		if observed.PendingAsk == nil || observed.PendingAsk.Question != "May I force-push?" {
			t.Error("Get() did not surface the question, so nobody could answer it")
		}
	})

	t.Run("answering releases it and the question stops being pending", func(t *testing.T) {
		if _, err := d.Answer(ask.ID, "No. Open a PR."); err != nil {
			t.Fatalf("Answer() = %v, want no error", err)
		}
		answered, err := d.AwaitAnswer(context.Background(), ask.ID, time.Millisecond)
		if err != nil {
			t.Fatalf("AwaitAnswer() = %v, want no error", err)
		}
		if answered.Answer != "No. Open a PR." {
			t.Errorf("answer = %q, want %q", answered.Answer, "No. Open a PR.")
		}
		observed, err := d.Get(context.Background(), engagement.ID)
		if err != nil {
			t.Fatalf("Get() = %v, want no error", err)
		}
		if observed.Health == HealthBlocked {
			t.Error("Health is still blocked after the question was answered")
		}
	})

	t.Run("a question cannot be answered twice", func(t *testing.T) {
		if _, err := d.Answer(ask.ID, "changed my mind"); err == nil {
			t.Error("Answer() = nil error on an already-answered question, want a rejection")
		}
	})
}

func TestStatusIsNeverServedFromStoredState(t *testing.T) {
	t.Parallel()
	// The organising rule: the harness is authoritative for anything happening
	// now. This asserts it directly — nothing is written between the two reads,
	// only the adapter's answer changes, and the verdict must follow.
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	adapter := &fakeAdapter{name: "fake", observation: harness.Observation{
		Found: true, Lifecycle: harness.LifecycleWorking, LastActivityAt: now,
	}}
	d := newTestDirector(t, adapter, now)

	engagement, err := d.Spawn(context.Background(), SpawnOptions{Task: "investigate", Brief: "look"})
	if err != nil {
		t.Fatalf("Spawn() = %v, want no error", err)
	}
	if engagement.Lifecycle != harness.LifecycleWorking {
		t.Fatalf("Lifecycle = %v, want %v", engagement.Lifecycle, harness.LifecycleWorking)
	}

	adapter.observation.Lifecycle = harness.LifecycleDone
	observed, err := d.Get(context.Background(), engagement.ID)
	if err != nil {
		t.Fatalf("Get() = %v, want no error", err)
	}
	if observed.Lifecycle != harness.LifecycleDone {
		t.Errorf("Lifecycle = %v, want %v — lifecycle must be re-derived, never cached",
			observed.Lifecycle, harness.LifecycleDone)
	}

	// And nothing lifecycle-shaped may have been persisted on the way past.
	reloaded, err := LoadState(d.State.Path)
	if err != nil {
		t.Fatalf("LoadState() = %v, want no error", err)
	}
	if reloaded.Engagements[engagement.ID].Lifecycle != "" {
		t.Error("lifecycle was written to the state file; a stored lifecycle is wrong the moment a process exits")
	}
}

func TestDirectorsAreIsolatedFromEachOther(t *testing.T) {
	t.Parallel()
	// Two directors in one root must not see each other's fleets, and one
	// agent's token must not reach across.
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	adapter := &fakeAdapter{name: "fake", observation: harness.Observation{
		Found: true, Lifecycle: harness.LifecycleWorking,
	}}
	first := newTestDirector(t, adapter, now)

	second, err := Init(first.Roots, "test", "second", func() time.Time { return now })
	if err != nil {
		t.Fatalf("Init() = %v, want no error", err)
	}
	other, err := Open(first.Roots, second.DirectorID, func() time.Time { return now })
	if err != nil {
		t.Fatalf("Open() = %v, want no error", err)
	}
	other.Lookup = first.Lookup
	other.Config.Harness = adapter.name

	engagement, err := first.Spawn(context.Background(), SpawnOptions{Task: "investigate", Brief: "mine"})
	if err != nil {
		t.Fatalf("Spawn() = %v, want no error", err)
	}

	fleet, err := other.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() = %v, want no error", err)
	}
	if len(fleet) != 0 {
		t.Errorf("the second director sees %d engagements, want 0", len(fleet))
	}
	if _, err := other.Report(engagement.ID, engagement.Token, "reading", ""); err == nil {
		t.Error("Report() across directors = nil error, want a rejection")
	}
}

func TestOpenRefusesToGuessBetweenDirectors(t *testing.T) {
	// Not parallel: it clears DIRECTOR_ID, which is process-global.
	// Picking the wrong director would silently operate on somebody else's
	// fleet, and the mistake would not show up in the output.
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	d := newTestDirector(t, &fakeAdapter{name: "fake"}, now)
	if _, err := Init(d.Roots, "test", "another", func() time.Time { return now }); err != nil {
		t.Fatalf("Init() = %v, want no error", err)
	}

	t.Setenv(EnvID, "")
	_, err := Open(d.Roots, "", func() time.Time { return now })
	if err == nil {
		t.Fatal("Open() = nil error with two directors, want a refusal")
	}
	if !strings.Contains(err.Error(), "--director") {
		t.Errorf("Open() error = %q, want it to say how to disambiguate", err)
	}
}

func TestSpawnDisplayName(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

	t.Run("an explicit name reaches the harness", func(t *testing.T) {
		t.Parallel()
		adapter := &fakeAdapter{name: "fake"}
		d := newTestDirector(t, adapter, now)
		engagement, err := d.Spawn(context.Background(), SpawnOptions{
			Task: "investigate", Title: "review the auth refactor", Name: "auth-review", Brief: "look",
		})
		if err != nil {
			t.Fatalf("Spawn() = %v, want no error", err)
		}
		if got := adapter.spawns[0].Name; got != "auth-review" {
			t.Errorf("harness was told name %q, want %q", got, "auth-review")
		}
		if engagement.Name != "auth-review" {
			t.Errorf("Name = %q, want %q", engagement.Name, "auth-review")
		}
	})

	t.Run("an omitted name falls back to the title rather than to nothing", func(t *testing.T) {
		t.Parallel()
		// An empty name is not neutral: the harness derives one from the
		// working directory, so siblings in one repository all collide.
		adapter := &fakeAdapter{name: "fake"}
		d := newTestDirector(t, adapter, now)
		if _, err := d.Spawn(context.Background(), SpawnOptions{
			Task: "investigate", Title: "review the auth refactor", Brief: "look",
		}); err != nil {
			t.Fatalf("Spawn() = %v, want no error", err)
		}
		if got := adapter.spawns[0].Name; got != "review the auth refactor" {
			t.Errorf("harness was told name %q, want it derived from the title", got)
		}
	})

	t.Run("a long title is shortened, since a name lands in a prompt box not a table", func(t *testing.T) {
		t.Parallel()
		adapter := &fakeAdapter{name: "fake"}
		d := newTestDirector(t, adapter, now)
		long := "work out why the discount calculation in the billing module is inverted"
		if _, err := d.Spawn(context.Background(), SpawnOptions{
			Task: "investigate", Title: long, Brief: "look",
		}); err != nil {
			t.Fatalf("Spawn() = %v, want no error", err)
		}
		if got := adapter.spawns[0].Name; len(got) > 32 {
			t.Errorf("harness was told a %d-character name %q, want it shortened", len(got), got)
		}
	})
}

func TestDisplayNameSurvivesReload(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	adapter := &fakeAdapter{name: "fake", observation: harness.Observation{Found: true}}
	d := newTestDirector(t, adapter, now)

	engagement, err := d.Spawn(context.Background(), SpawnOptions{
		Task: "investigate", Name: "auth-review", Brief: "look",
	})
	if err != nil {
		t.Fatalf("Spawn() = %v, want no error", err)
	}
	reloaded, err := LoadState(d.State.Path)
	if err != nil {
		t.Fatalf("LoadState() = %v, want no error", err)
	}
	if got := reloaded.Engagements[engagement.ID].Name; got != "auth-review" {
		t.Errorf("Name after reload = %q, want %q — it records what a person sees in the harness", got, "auth-review")
	}
}

// The placement order is the crux of running a director inside a pane, and
// every row here is a way of getting it wrong. Detection has to outrank the
// configured default — under a root that names a harness, which is every root
// `director init` writes, a detection placed below it would never fire at all.
// It has to lose to a flag and to a pin, because those are somebody deciding
// about this piece of work and an ambient signal must not overrule a decision.
func TestAdapterForPrecedence(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

	const pane = "w6:p1"

	cases := []struct {
		name         string
		override     string
		pin          string
		configured   string
		autodetect   bool
		inPane       bool
		withoutHerdr bool
		want         string
		wantNote     bool
		wantErr      bool
	}{
		{
			name:       "the configured default when nothing else applies",
			configured: "claude-code",
			autodetect: true,
			want:       "claude-code",
		},
		{
			name:       "the pane this director is running in beats the configured default",
			configured: "claude-code",
			autodetect: true,
			inPane:     true,
			want:       herdr.Name,
			wantNote:   true,
		},
		{
			name:       "a workflow or task pin beats the pane",
			pin:        "claude-code",
			configured: "claude-code",
			autodetect: true,
			inPane:     true,
			want:       "claude-code",
		},
		{
			name:       "an explicit --harness beats the pane",
			override:   "claude-code",
			configured: "claude-code",
			autodetect: true,
			inPane:     true,
			want:       "claude-code",
		},
		{
			name:       "an explicit --harness still beats a pin",
			override:   "claude-code",
			pin:        herdr.Name,
			configured: "claude-code",
			autodetect: true,
			want:       "claude-code",
		},
		{
			name:       "the escape hatch turns detection off entirely",
			configured: "claude-code",
			autodetect: false,
			inPane:     true,
			want:       "claude-code",
		},
		{
			name:       "a detected placement the configuration already agreed with says nothing",
			configured: herdr.Name,
			autodetect: true,
			inPane:     true,
			want:       herdr.Name,
		},
		{
			// Detection is ambient. Something nobody asked for must not be able
			// to break a spawn that the configuration alone would have made.
			name:         "a binary built without the herdr adapter falls through to the configuration",
			configured:   "claude-code",
			autodetect:   true,
			inPane:       true,
			withoutHerdr: true,
			want:         "claude-code",
		},
		{
			name:       "no pane and nothing configured is still an error",
			autodetect: true,
			wantErr:    true,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			d := newTestDirector(t, &fakeAdapter{name: "claude-code"}, now)
			d.Config.Harness = testCase.configured
			d.Config.HerdrAutodetect = testCase.autodetect
			d.InPane = func() (string, bool) { return pane, testCase.inPane }
			d.Lookup = func(name string) (harness.Adapter, error) {
				if name == herdr.Name && testCase.withoutHerdr {
					return nil, errors.New("no harness named " + name)
				}
				return &fakeAdapter{name: name}, nil
			}

			place, err := d.adapterFor(Task{Name: "investigate", Harness: testCase.pin}, testCase.override)
			if testCase.wantErr {
				if err == nil {
					t.Fatalf("adapterFor() = %v, want an error naming how to choose a harness", place.adapter)
				}
				return
			}
			if err != nil {
				t.Fatalf("adapterFor() = %v, want no error", err)
			}
			if got := place.adapter.Name(); got != testCase.want {
				t.Errorf("adapterFor() placed on %q, want %q", got, testCase.want)
			}

			switch {
			case testCase.wantNote && place.note == "":
				t.Error("placement note is empty; nobody can see why the engagement left the configured harness")
			case testCase.wantNote:
				for _, want := range []string{pane, testCase.configured, "herdr_autodetect"} {
					if !strings.Contains(place.note, want) {
						t.Errorf("placement note = %q, want it to mention %q", place.note, want)
					}
				}
			case place.note != "":
				t.Errorf("placement note = %q, want none — the configuration was not overridden", place.note)
			}
		})
	}
}

func TestSpawnRecordsWhyItLeftTheConfiguredHarness(t *testing.T) {
	t.Parallel()
	// The note is stored with the engagement rather than printed and forgotten:
	// the question "why is this thing in a pane" is asked long after the spawn
	// that answered it has scrolled away.
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	d := newTestDirector(t, &fakeAdapter{name: "claude-code"}, now)
	d.Config.Harness = "claude-code"
	d.Lookup = func(name string) (harness.Adapter, error) { return &fakeAdapter{name: name}, nil }
	d.InPane = func() (string, bool) { return "w6:p1", true }

	engagement, err := d.Spawn(context.Background(), SpawnOptions{Task: "investigate", Brief: "look"})
	if err != nil {
		t.Fatalf("Spawn() = %v, want no error", err)
	}
	if engagement.Harness != herdr.Name {
		t.Errorf("Harness = %q, want %q", engagement.Harness, herdr.Name)
	}
	if !strings.Contains(engagement.Detail["placement"], "w6:p1") {
		t.Errorf("detail[placement] = %q, want it to name the pane that decided this", engagement.Detail["placement"])
	}

	reloaded, err := LoadState(d.State.Path)
	if err != nil {
		t.Fatalf("LoadState() = %v, want no error", err)
	}
	if got := reloaded.Engagements[engagement.ID].Detail["placement"]; got == "" {
		t.Error("the placement reason did not survive a reload; it is only useful later")
	}
}

func TestSpawnDoesNotHandAnAgentThisDirectorsPane(t *testing.T) {
	t.Parallel()
	// The inheritance trap. An adapter that starts a process hands it this
	// director's environment, so without this an agent would read herdr's
	// variables, conclude it was the process occupying its parent's pane, and
	// report somebody else's pane as its own.
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	adapter := &fakeAdapter{name: "fake"}
	d := newTestDirector(t, adapter, now)

	if _, err := d.Spawn(context.Background(), SpawnOptions{Task: "investigate", Brief: "look"}); err != nil {
		t.Fatalf("Spawn() = %v, want no error", err)
	}

	env := adapter.spawns[0].Env
	for _, key := range []string{herdr.EnvInPane, herdr.EnvPane} {
		value, present := env[key]
		if !present {
			t.Errorf("Env has no %s; an inherited variable can only be overridden, not omitted", key)
		}
		if value != "" {
			t.Errorf("Env[%s] = %q, want it emptied", key, value)
		}
	}
	if _, present := env[herdr.EnvSocket]; present {
		t.Errorf("Env carries %s; the socket is not a pane identity and is not director's to set", herdr.EnvSocket)
	}
}
