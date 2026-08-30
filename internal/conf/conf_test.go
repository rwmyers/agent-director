package conf

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	t.Parallel()

	t.Run("global entries and sections are kept in file order", func(t *testing.T) {
		t.Parallel()
		file, err := Parse(strings.NewReader(`
# a comment
harness = claude-code

[task.review]
permissions = read-only

[task.implement]
permissions = write-local
`))
		if err != nil {
			t.Fatalf("Parse() = %v, want no error", err)
		}
		if got, want := file.Global.Get("harness"), "claude-code"; got != want {
			t.Errorf("Get(harness) = %q, want %q", got, want)
		}
		if got, want := len(file.Sections), 2; got != want {
			t.Fatalf("len(Sections) = %d, want %d", got, want)
		}
		if got, want := file.Sections[0].Name, "task.review"; got != want {
			t.Errorf("Sections[0].Name = %q, want %q", got, want)
		}
	})

	t.Run("a value may contain the characters humans and models actually write", func(t *testing.T) {
		t.Parallel()
		// There are no inline comments, so a value is everything after the
		// first '='. Notes and briefs are written by people and by models, and
		// neither avoids punctuation on our behalf — a note truncated at a '#'
		// would lose exactly the part that mattered.
		file, err := Parse(strings.NewReader(`note = fix #4 where total = price * qty`))
		if err != nil {
			t.Fatalf("Parse() = %v, want no error", err)
		}
		if got, want := file.Global.Get("note"), "fix #4 where total = price * qty"; got != want {
			t.Errorf("Get(note) = %q, want %q", got, want)
		}
	})

	t.Run("a repeated key reads as a list with a head", func(t *testing.T) {
		t.Parallel()
		file, err := Parse(strings.NewReader("conversation = a\nconversation = b\n"))
		if err != nil {
			t.Fatalf("Parse() = %v, want no error", err)
		}
		if got, want := file.Global.Get("conversation"), "a"; got != want {
			t.Errorf("Get() = %q, want the first value %q", got, want)
		}
		if got, want := len(file.Global.All("conversation")), 2; got != want {
			t.Errorf("len(All()) = %d, want %d", got, want)
		}
	})

	t.Run("a malformed line names its line number", func(t *testing.T) {
		t.Parallel()
		_, err := Parse(strings.NewReader("ok = yes\nnot a key value line\n"))
		if err == nil {
			t.Fatal("Parse() = nil error, want a failure")
		}
		if !strings.Contains(err.Error(), "line 2") {
			t.Errorf("Parse() error = %q, want it to name line 2", err)
		}
	})

	t.Run("an unterminated section header is refused", func(t *testing.T) {
		t.Parallel()
		if _, err := Parse(strings.NewReader("[task.review\n")); err == nil {
			t.Error("Parse() = nil error, want a failure")
		}
	})
}

func TestSectionsWithPrefix(t *testing.T) {
	t.Parallel()
	file, err := Parse(strings.NewReader(`
[task.b]
x = 1

[permission.read-only]
allow = read

[task.a]
x = 2
`))
	if err != nil {
		t.Fatalf("Parse() = %v, want no error", err)
	}
	sections := file.SectionsWithPrefix("task.")
	if got, want := len(sections), 2; got != want {
		t.Fatalf("len(SectionsWithPrefix) = %d, want %d", got, want)
	}
	// Sorted, so iteration order does not depend on how the file was written.
	if got, want := sections[0].Name, "task.a"; got != want {
		t.Errorf("SectionsWithPrefix()[0] = %q, want %q", got, want)
	}
}

func TestEntriesSet(t *testing.T) {
	t.Parallel()
	entries := Entries{{Key: "a", Value: "1"}, {Key: "b", Value: "2"}, {Key: "a", Value: "3"}}
	entries.Set("a", "9")
	if got, want := entries.Get("a"), "9"; got != want {
		t.Errorf("Get(a) = %q, want %q", got, want)
	}
	if got, want := len(entries.All("a")), 1; got != want {
		t.Errorf("Set() left %d values for a, want %d — duplicates make the file non-canonical", got, want)
	}
}

func TestRoundTrip(t *testing.T) {
	t.Parallel()
	// The state writer round-trips through this, so a value that survives
	// writing but not reading would corrupt a fleet silently.
	original := &File{}
	original.Global.Set("version", "1")
	original.SetSection("engagement.eng_1", Entries{
		{Key: "title", Value: "fix #4 = maybe"},
		{Key: "detail.pid", Value: "48901"},
	})

	var buf bytes.Buffer
	if err := Write(&buf, original); err != nil {
		t.Fatalf("Write() = %v, want no error", err)
	}
	reparsed, err := Parse(&buf)
	if err != nil {
		t.Fatalf("Parse() = %v, want no error", err)
	}
	section, ok := reparsed.Section("engagement.eng_1")
	if !ok {
		t.Fatal("Section() not found after round trip")
	}
	if got, want := section.Entries.Get("title"), "fix #4 = maybe"; got != want {
		t.Errorf("title = %q, want %q", got, want)
	}
	if got, want := section.Entries.Get("detail.pid"), "48901"; got != want {
		t.Errorf("detail.pid = %q, want %q", got, want)
	}
}

func TestWriteFileIsAtomic(t *testing.T) {
	t.Parallel()
	// Readers take no lock, so the write has to be a rename: a reader sees the
	// old bytes or the new ones, never a torn file.
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "test.state")

	file := &File{}
	file.Global.Set("version", "1")
	if err := WriteFile(path, file); err != nil {
		t.Fatalf("WriteFile() = %v, want no error", err)
	}
	reloaded, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile() = %v, want no error", err)
	}
	if got, want := reloaded.Global.Get("version"), "1"; got != want {
		t.Errorf("version = %q, want %q", got, want)
	}
}

func TestList(t *testing.T) {
	t.Parallel()
	// "a, b,c" and "a,b,c" have to mean the same thing, or a workflow author's
	// spacing changes their task's vocabulary.
	got := List(" orienting, reading ,drafting,, ")
	want := []string{"orienting", "reading", "drafting"}
	if len(got) != len(want) {
		t.Fatalf("List() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("List()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
