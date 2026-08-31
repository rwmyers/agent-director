package conf

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// brokenState is what the previous writer produced when a director answered an
// agent with a multi-paragraph answer: the value ran onto the following lines,
// and from then on every command against that director failed on line 4.
const brokenState = `version = 1
director = dir_7c0f3ce91b9d6428

[ask.ask_1]
engagement = eng_1
question = Should the escaping be quoting?
answer = Use quoting — it keeps existing files readable.

The alternative rewrites every line, and every file on disk churns.
asked_at = 2026-08-31T11:29:50Z
`

func TestParseNamesTheKeyAValueRanOnFrom(t *testing.T) {
	t.Parallel()

	_, err := Parse(strings.NewReader(brokenState))
	if err == nil {
		t.Fatal("Parse() = nil error, want a failure")
	}
	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("Parse() error is %T, want a *ParseError", err)
	}
	// Line 8 is where the parse stops, but line 7 — the "answer" key — is where
	// the damage is and what somebody has to fix.
	if got, want := parseErr.Line, 9; got != want {
		t.Errorf("ParseError.Line = %d, want %d", got, want)
	}
	if got, want := parseErr.Key, "answer"; got != want {
		t.Errorf("ParseError.Key = %q, want %q", got, want)
	}
	if got, want := parseErr.KeyLine, 7; got != want {
		t.Errorf("ParseError.KeyLine = %d, want %d", got, want)
	}
	if !strings.Contains(parseErr.Error(), "director repair") {
		t.Errorf("ParseError.Error() = %q, want it to name the way out", parseErr)
	}
}

func TestParseFileNamesTheFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "dir_1.state")
	if err := os.WriteFile(path, []byte(brokenState), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ParseFile(path)
	if err == nil {
		t.Fatal("ParseFile() = nil error, want a failure")
	}
	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("ParseFile() error is %T, want a *ParseError", err)
	}
	if parseErr.Path != path {
		t.Errorf("ParseError.Path = %q, want %q", parseErr.Path, path)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error = %q, want it to name the file", err)
	}
}

func TestRepairFile(t *testing.T) {
	t.Parallel()

	t.Run("a value that ran onto later lines is joined back together", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "dir_1.state")
		if err := os.WriteFile(path, []byte(brokenState), 0o600); err != nil {
			t.Fatal(err)
		}

		report, err := RepairFile(path, true)
		if err != nil {
			t.Fatalf("RepairFile() = %v, want no error", err)
		}
		if !report.Repaired() || !report.Applied {
			t.Fatalf("RepairFile() report = %+v, want a repair that was applied", report)
		}
		if got, want := report.Fixes[0].Key, "answer"; got != want {
			t.Errorf("Fixes[0].Key = %q, want %q", got, want)
		}
		if got, want := report.Fixes[0].Section, "ask.ask_1"; got != want {
			t.Errorf("Fixes[0].Section = %q, want %q", got, want)
		}

		file, err := ParseFile(path)
		if err != nil {
			t.Fatalf("ParseFile() after repair = %v, want a readable file", err)
		}
		section, ok := file.Section("ask.ask_1")
		if !ok {
			t.Fatal("Section(ask.ask_1) missing after repair")
		}
		want := "Use quoting — it keeps existing files readable.\n\n" +
			"The alternative rewrites every line, and every file on disk churns."
		if got := section.Entries.Get("answer"); got != want {
			t.Errorf("answer = %q, want %q", got, want)
		}
		// Nothing after the damage may be lost: that is the rest of the record.
		if got, want := section.Entries.Get("asked_at"), "2026-08-31T11:29:50Z"; got != want {
			t.Errorf("asked_at = %q, want %q — the repair dropped the rest of the section", got, want)
		}
		if got, want := file.Global.Get("director"), "dir_7c0f3ce91b9d6428"; got != want {
			t.Errorf("director = %q, want %q", got, want)
		}
	})

	t.Run("without --write nothing on disk changes", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "dir_1.state")
		if err := os.WriteFile(path, []byte(brokenState), 0o600); err != nil {
			t.Fatal(err)
		}

		report, err := RepairFile(path, false)
		if err != nil {
			t.Fatalf("RepairFile() = %v, want no error", err)
		}
		if !report.Repaired() {
			t.Error("RepairFile() found nothing to repair, want the broken value")
		}
		if report.Applied {
			t.Error("RepairFile(apply=false) wrote the file")
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != brokenState {
			t.Error("RepairFile(apply=false) changed the file on disk")
		}
	})

	t.Run("a readable file is left exactly as it is", func(t *testing.T) {
		t.Parallel()
		const healthy = "# hand written\nversion = 1\n\n[harness.claude-code]\nbin = claude\n"
		path := filepath.Join(t.TempDir(), "director.conf")
		if err := os.WriteFile(path, []byte(healthy), 0o600); err != nil {
			t.Fatal(err)
		}

		report, err := RepairFile(path, true)
		if err != nil {
			t.Fatalf("RepairFile() = %v, want no error", err)
		}
		if report.Repaired() || report.Applied {
			t.Errorf("RepairFile() = %+v on a healthy file, want no change", report)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != healthy {
			t.Errorf("a readable file was rewritten:\n%s", body)
		}
	})

	t.Run("prose that parsed as a key with spaces in it is put back", func(t *testing.T) {
		t.Parallel()
		// This file reads without complaint, which is the quieter half of the
		// same damage: "the fix = escaping" is a sentence out of somebody's
		// answer, read as a key with spaces in it, and the answer above it now
		// stops halfway through. Nothing writes a key with a space in it.
		path := filepath.Join(t.TempDir(), "dir_1.state")
		body := "version = 1\nanswer = Two things.\nthe fix = escaping, not a new format\n"
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ParseFile(path); err != nil {
			t.Fatalf("ParseFile() = %v, want this file to parse — the point is that it does", err)
		}

		report, err := RepairFile(path, true)
		if err != nil {
			t.Fatalf("RepairFile() = %v, want no error", err)
		}
		if !report.Repaired() {
			t.Fatal("RepairFile() found nothing, want the shredded answer")
		}
		file, err := ParseFile(path)
		if err != nil {
			t.Fatalf("ParseFile() after repair = %v", err)
		}
		want := "Two things.\nthe fix = escaping, not a new format"
		if got := file.Global.Get("answer"); got != want {
			t.Errorf("answer = %q, want %q", got, want)
		}
	})

	t.Run("a line with no key above it is refused rather than guessed at", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "dir_1.state")
		if err := os.WriteFile(path, []byte("what even is this\nversion = 1\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		if _, err := RepairFile(path, true); err == nil {
			t.Fatal("RepairFile() = nil error, want a refusal")
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(body), "what even is this") {
			t.Error("a repair it could not make still changed the file")
		}
	})
}

// TestRepairedValuesStayRepaired closes the loop: the repaired file is written
// through the same writer as everything else, so the value that broke the file
// is now quoted and reading it again gives the same string.
func TestRepairedValuesStayRepaired(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "dir_1.state")
	if err := os.WriteFile(path, []byte(brokenState), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RepairFile(path, true); err != nil {
		t.Fatalf("RepairFile() = %v, want no error", err)
	}

	first, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile() = %v", err)
	}
	if err := WriteFile(path, first); err != nil {
		t.Fatalf("WriteFile() = %v", err)
	}
	second, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile() after rewrite = %v", err)
	}

	firstSection, _ := first.Section("ask.ask_1")
	secondSection, _ := second.Section("ask.ask_1")
	if got, want := secondSection.Entries.Get("answer"), firstSection.Entries.Get("answer"); got != want {
		t.Errorf("answer after a second round trip = %q, want %q", got, want)
	}
}
