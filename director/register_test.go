package director

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestRoots builds a scratch root holding one workflow, so a test can
// register directors in it without reaching anything real.
func newTestRoots(t *testing.T) Roots {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "prompts", "task.md"), "Do the thing.")
	writeFile(t, filepath.Join(root, "workflows", "default.conf"), `
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
	writeFile(t, filepath.Join(root, "workflows", "other.conf"), `
description = another

[permission.read-only]
allow = read

[task.investigate]
prompt      = ../prompts/task.md
permissions = read-only
progress    = orienting, delivered
terminal    = delivered
report_on   = progress-change, 5m
`)
	return Roots{Primary: root, Layers: []Layer{{Kind: LayerProject, Path: root}}}
}

func fixedClock(t time.Time) Clock { return func() time.Time { return t } }

func TestRegisterIsIdempotent(t *testing.T) {
	t.Parallel()
	// The reported failure: `director setup` run twice in one root — by a setup
	// script, or by an agent reaching for it after something else complained
	// there was no director — left two directors with the same auto-generated
	// name, and every later command in that root refused to choose between
	// them. Running the same setup command again must not be able to break the
	// first run.
	roots := newTestRoots(t)
	now := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)

	first, created, err := Register(roots, "", "", false, fixedClock(now))
	if err != nil {
		t.Fatalf("first Register() = %v, want no error", err)
	}
	if !created {
		t.Error("first Register() reported created = false, want true in an empty root")
	}

	second, created, err := Register(roots, "", "", false, fixedClock(now))
	if err != nil {
		t.Fatalf("second Register() = %v, want it to adopt rather than fail", err)
	}
	if created {
		t.Error("second Register() reported created = true, want the existing director adopted")
	}
	if second.DirectorID != first.DirectorID {
		t.Errorf("second Register() = %s, want the one already registered: %s", second.DirectorID, first.DirectorID)
	}

	states, _ := ListDirectors(roots.Primary)
	if len(states) != 1 {
		t.Fatalf("directors after two registrations = %d, want 1 — the root must not be left ambiguous", len(states))
	}
	// The whole point of not leaving it ambiguous: a command with nothing to
	// go on can still resolve a director.
	if _, err := Open(roots, "", fixedClock(now)); err != nil {
		t.Errorf("Open() with no id = %v, want the single director resolved", err)
	}
}

func TestRegisterNewCreatesADistinguishableSecond(t *testing.T) {
	t.Parallel()
	// Two fleets in one root is legitimate. Two rows called "default" is not:
	// the id is random hex, so the name is the only part anybody reads.
	roots := newTestRoots(t)
	now := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)

	first, _, err := Register(roots, "", "", false, fixedClock(now))
	if err != nil {
		t.Fatalf("Register() = %v, want no error", err)
	}
	second, created, err := Register(roots, "", "", true, fixedClock(now))
	if err != nil {
		t.Fatalf("Register(--new) = %v, want no error", err)
	}
	if !created {
		t.Error("Register(--new) reported created = false, want a second director")
	}
	if second.Name == first.Name {
		t.Errorf("both directors are named %q, want the second one to be tellable from the first", second.Name)
	}
	if second.Name != first.Name+"-2" {
		t.Errorf("second name = %q, want %q", second.Name, first.Name+"-2")
	}

	third, _, err := Register(roots, "", "", true, fixedClock(now))
	if err != nil {
		t.Fatalf("Register(--new) third = %v, want no error", err)
	}
	if third.Name == first.Name || third.Name == second.Name {
		t.Errorf("third name = %q, want it distinct from %q and %q", third.Name, first.Name, second.Name)
	}
}

func TestRegisterWillNotChooseBetweenSeveral(t *testing.T) {
	t.Parallel()
	// A root that already holds two — from a version that created them, or
	// from somebody who asked for both — is one setup cannot resolve, and
	// registering a third would make it worse rather than better.
	roots := newTestRoots(t)
	now := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)

	if _, _, err := Register(roots, "", "", false, fixedClock(now)); err != nil {
		t.Fatalf("Register() = %v", err)
	}
	if _, _, err := Register(roots, "", "", true, fixedClock(now)); err != nil {
		t.Fatalf("Register(--new) = %v", err)
	}

	_, _, err := Register(roots, "", "", false, fixedClock(now))
	if err == nil {
		t.Fatal("Register() in a root with two directors = nil, want a refusal rather than a third")
	}
	for _, want := range []string{roots.Primary, EnvID, "--new"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err, want)
		}
	}
	if states, _ := ListDirectors(roots.Primary); len(states) != 2 {
		t.Errorf("directors = %d, want the refusal to have created nothing", len(states))
	}
}

func TestRegisterRefusesToAdoptUnderADifferentName(t *testing.T) {
	t.Parallel()
	// Adopting silently would report success while ignoring what it was told.
	roots := newTestRoots(t)
	now := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)

	if _, _, err := Register(roots, "", "lead", false, fixedClock(now)); err != nil {
		t.Fatalf("Register() = %v", err)
	}

	// The same name again is the same request, and stays idempotent.
	same, created, err := Register(roots, "", "lead", false, fixedClock(now))
	if err != nil {
		t.Fatalf("Register() with the same name = %v, want it adopted", err)
	}
	if created || same.Name != "lead" {
		t.Errorf("Register() = %s/%q created=%v, want the existing lead adopted", same.DirectorID, same.Name, created)
	}

	_, _, err = Register(roots, "", "second", false, fixedClock(now))
	if err == nil {
		t.Fatal("Register() with a different name = nil, want a refusal rather than a silently ignored flag")
	}
	if !strings.Contains(err.Error(), "--new") {
		t.Errorf("error = %q, want it to name the flag that asks for a second director", err)
	}
}

func TestRegisterRefusesToAdoptOntoADifferentWorkflow(t *testing.T) {
	t.Parallel()
	// The binding is permanent, so a director on another workflow is another
	// director. Adopting and ignoring --workflow would hand back a fleet
	// validated against task types the caller did not ask for.
	roots := newTestRoots(t)
	now := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)

	if _, _, err := Register(roots, "default", "", false, fixedClock(now)); err != nil {
		t.Fatalf("Register() = %v", err)
	}

	_, _, err := Register(roots, "other", "", false, fixedClock(now))
	if err == nil {
		t.Fatal("Register(--workflow other) = nil, want a refusal rather than a director on the wrong workflow")
	}
	for _, want := range []string{"default", "other", "--new"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err, want)
		}
	}
}

func TestInitNeverMintsTwoDirectorsWithOneName(t *testing.T) {
	t.Parallel()
	// Init is reached by `director attach --new` as well as by `director setup`,
	// and a second conversation asking for its own director must still get one
	// it can be told apart from the first.
	roots := newTestRoots(t)
	now := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)

	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		state, err := Init(roots, "default", "lead", fixedClock(now))
		if err != nil {
			t.Fatalf("Init() = %v", err)
		}
		if seen[state.Name] {
			t.Fatalf("Init() named a director %q twice in one root", state.Name)
		}
		seen[state.Name] = true
	}
}
