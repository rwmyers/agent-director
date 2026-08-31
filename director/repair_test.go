package director

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newRootWithWorkflow builds a root a director can be created in.
func newRootWithWorkflow(t *testing.T) Roots {
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
	return Roots{Primary: root, Layers: []Layer{{Kind: LayerProject, Path: root}}}
}

// breakState writes a value across several lines, the way the writer used to
// when an agent asked a multi-line question.
func breakState(t *testing.T, path string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	broken := strings.Replace(string(body), "name = tester",
		"name = tester\nand then a second line nobody can parse", 1)
	if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestOneUnreadableRecordDoesNotHideTheOthers is the blast radius. One director
// with a broken state file used to mean no director on the machine could be
// listed, opened or worked with at all.
func TestOneUnreadableRecordDoesNotHideTheOthers(t *testing.T) {
	t.Parallel()
	roots := newRootWithWorkflow(t)
	clock := func() time.Time { return time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC) }

	broken, err := Init(roots, "test", "tester", clock)
	if err != nil {
		t.Fatalf("Init() = %v, want no error", err)
	}
	healthy, err := Init(roots, "test", "second", clock)
	if err != nil {
		t.Fatalf("Init() = %v, want no error", err)
	}
	breakState(t, broken.Path)

	states, problems := ListDirectors(roots.Primary)
	if len(states) != 1 || states[0].DirectorID != healthy.DirectorID {
		t.Fatalf("ListDirectors() = %v, want only the readable director %s", states, healthy.DirectorID)
	}
	if problems == nil {
		t.Error("ListDirectors() reported no problem, want the unreadable record named")
	} else if !strings.Contains(problems.Error(), broken.Path) {
		t.Errorf("ListDirectors() problem = %q, want it to name %s", problems, broken.Path)
	}

	// And the readable one is still openable without being told which it is.
	if _, err := Open(roots, "", clock); err != nil {
		t.Errorf("Open() = %v, want the readable director", err)
	}
}

// TestOpeningABrokenRecordSaysHowToFixIt — the director this happens to is the
// one that most needs to be told what to do about it.
func TestOpeningABrokenRecordSaysHowToFixIt(t *testing.T) {
	t.Parallel()
	roots := newRootWithWorkflow(t)
	clock := func() time.Time { return time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC) }

	state, err := Init(roots, "test", "tester", clock)
	if err != nil {
		t.Fatalf("Init() = %v, want no error", err)
	}
	breakState(t, state.Path)

	_, err = Open(roots, state.DirectorID, clock)
	if err == nil {
		t.Fatal("Open() = nil error, want a failure")
	}
	for _, want := range []string{state.Path, "director repair", "line"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Open() error = %q, want it to mention %q", err, want)
		}
	}
}

// TestRepairStatesBringsADirectorBack is the path back: no text editor, and
// nothing lost on the way.
func TestRepairStatesBringsADirectorBack(t *testing.T) {
	t.Parallel()
	roots := newRootWithWorkflow(t)
	clock := func() time.Time { return time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC) }

	state, err := Init(roots, "test", "tester", clock)
	if err != nil {
		t.Fatalf("Init() = %v, want no error", err)
	}
	breakState(t, state.Path)

	reports, problems := RepairStates(roots.Primary, false)
	if problems != nil {
		t.Fatalf("RepairStates() = %v, want no error", problems)
	}
	if len(reports) != 1 || !reports[0].Repaired() {
		t.Fatalf("RepairStates() = %+v, want one repairable record", reports)
	}
	if reports[0].Applied {
		t.Error("RepairStates(apply=false) wrote the file")
	}
	if _, err := Open(roots, state.DirectorID, clock); err == nil {
		t.Error("Open() succeeded after a dry run, so the dry run was not dry")
	}

	if _, problems := RepairStates(roots.Primary, true); problems != nil {
		t.Fatalf("RepairStates(apply) = %v, want no error", problems)
	}
	reopened, err := Open(roots, state.DirectorID, clock)
	if err != nil {
		t.Fatalf("Open() after repair = %v, want the director back", err)
	}
	if got, want := reopened.State.Name, "tester\nand then a second line nobody can parse"; got != want {
		t.Errorf("name after repair = %q, want %q — the repair lost what was there", got, want)
	}
}

// TestMultiLineFreeTextSurvivesTheStateFile is the bug itself, at the level it
// was reached from: an agent asks a multi-line question, a director answers
// with several paragraphs, and the record has to still be readable afterwards.
func TestMultiLineFreeTextSurvivesTheStateFile(t *testing.T) {
	t.Parallel()
	roots := newRootWithWorkflow(t)
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	state, err := Init(roots, "test", "tester", clock)
	if err != nil {
		t.Fatalf("Init() = %v, want no error", err)
	}

	question := "Two options here.\n\n  1. Quote the value.\n  2. Change the format — no.\n"
	answer := "Option 1.\n\nEvery file on disk has to keep parsing, and\nthe em dash is not the problem.\t"
	state.Engagements["eng_1"] = &Engagement{
		ID:          "eng_1",
		Harness:     "claude-code",
		Task:        "investigate",
		Title:       "a title",
		Note:        "a note\nover two lines",
		LastMessage: "  padded  ",
		StartedAt:   now,
	}
	state.Asks["ask_1"] = &Ask{
		ID:         "ask_1",
		Engagement: "eng_1",
		Question:   question,
		AskedAt:    now,
		Answer:     answer,
		AnsweredAt: now,
	}
	if err := state.Save(); err != nil {
		t.Fatalf("Save() = %v, want no error", err)
	}

	reloaded, err := LoadState(state.Path)
	if err != nil {
		t.Fatalf("LoadState() = %v, want no error", err)
	}
	if got := reloaded.Asks["ask_1"].Question; got != question {
		t.Errorf("question = %q, want %q", got, question)
	}
	if got := reloaded.Asks["ask_1"].Answer; got != answer {
		t.Errorf("answer = %q, want %q", got, answer)
	}
	if got, want := reloaded.Engagements["eng_1"].Note, "a note\nover two lines"; got != want {
		t.Errorf("note = %q, want %q", got, want)
	}
	if got, want := reloaded.Engagements["eng_1"].LastMessage, "  padded  "; got != want {
		t.Errorf("last_message = %q, want %q", got, want)
	}
}
