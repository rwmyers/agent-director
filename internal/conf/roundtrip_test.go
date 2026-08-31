package conf

import (
	"bytes"
	"strings"
	"testing"
)

// awkwardValues is everything a value has turned out to be in practice, plus
// the shapes that would break a naive escaping scheme.
var awkwardValues = []struct {
	name  string
	value string
}{
	{"empty", ""},
	{"ordinary", "claude-code"},
	{"prose with punctuation", "fix #4 where total = price * qty"},
	{"newline", "first line\nsecond line"},
	{"blank line between paragraphs", "first paragraph\n\nsecond paragraph"},
	{"trailing newline", "answered\n"},
	{"leading newline", "\nanswered"},
	{"carriage return", "one\r\ntwo"},
	{"leading space", "  indented"},
	{"trailing space", "indented  "},
	{"only whitespace", "   "},
	{"tab inside", "a\tb"},
	{"equals", "a = b = c"},
	{"section brackets", "[not a section]"},
	{"comment marker", "# not a comment"},
	{"double quote", `he said "no"`},
	{"wrapped in quotes", `"done"`},
	{"backslash", `C:\new\table`},
	{"looks like an escape", `a\nb`},
	// The value that corrupted a live director in practice: quoted, escaped,
	// and a value that would decode to something needing quotes if the reader
	// were naive about it.
	{"quoted with an escape inside", `"a\nb"`},
	{"quoted with a real newline inside", "\"a\nb\""},
	{"em dash", "recovery required hand-editing — by a person"},
	{"non-ascii", "日本語のノート"},
	{"emoji", "shipped 🚀"},
	{"nul byte", "a\x00b"},
	{"invalid utf-8", "a\xffb"},
	{"a whole answer", "Use the second option.\n\nThe first one changes the on-disk format,\nwhich means every existing file churns — don't.\n"},
}

// TestValueRoundTrip is the guarantee this format has to make: anything Write
// puts on disk, Parse reads back unchanged. A value that survives writing but
// not reading corrupts a fleet silently, and a value that cannot be written at
// all makes the file unreadable for every other director command.
func TestValueRoundTrip(t *testing.T) {
	t.Parallel()
	for _, testCase := range awkwardValues {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			assertRoundTrip(t, testCase.value)
		})
	}
}

func assertRoundTrip(t *testing.T, value string) {
	t.Helper()

	original := &File{}
	original.Global.Set("global", value)
	original.SetSection("engagement.eng_1", Entries{
		{Key: "note", Value: value},
		{Key: "after", Value: "still here"},
	})

	var buf bytes.Buffer
	if err := Write(&buf, original); err != nil {
		t.Fatalf("Write(%q) = %v, want no error", value, err)
	}
	reparsed, err := Parse(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("Parse() = %v, want no error\nwritten:\n%s", err, buf.String())
	}

	if got := reparsed.Global.Get("global"); got != value {
		t.Errorf("global value = %q, want %q\nwritten:\n%s", got, value, buf.String())
	}
	section, ok := reparsed.Section("engagement.eng_1")
	if !ok {
		t.Fatalf("Section() not found after round trip\nwritten:\n%s", buf.String())
	}
	if got := section.Entries.Get("note"); got != value {
		t.Errorf("note = %q, want %q\nwritten:\n%s", got, value, buf.String())
	}
	// The entry after a multi-line value has to survive too: the whole failure
	// mode was one bad value taking the rest of the file with it.
	if got, want := section.Entries.Get("after"), "still here"; got != want {
		t.Errorf("the entry after the value = %q, want %q\nwritten:\n%s", got, want, buf.String())
	}
}

// TestOrdinaryValuesAreWrittenExactlyAsBefore is the compatibility half. If a
// value that never needed escaping starts being written differently, every
// state file on disk churns on the next write and an older binary stops
// understanding what this one produces.
func TestOrdinaryValuesAreWrittenExactlyAsBefore(t *testing.T) {
	t.Parallel()

	file := &File{}
	file.Global.Set("version", "1")
	file.Global.Set("name", "mezner")
	file.SetSection("engagement.eng_1", Entries{
		{Key: "title", Value: "Escape multi-line values in conf state files"},
		{Key: "note", Value: "fix #4 where total = price * qty"},
		{Key: "last_message", Value: "Reading conf format — setting up worktree"},
		{Key: "dir", Value: "/home/mezner/src/agent-director/root"},
	})

	var buf bytes.Buffer
	if err := Write(&buf, file); err != nil {
		t.Fatalf("Write() = %v, want no error", err)
	}
	want := "version = 1\nname = mezner\n" +
		"\n[engagement.eng_1]\n" +
		"title = Escape multi-line values in conf state files\n" +
		"note = fix #4 where total = price * qty\n" +
		"last_message = Reading conf format — setting up worktree\n" +
		"dir = /home/mezner/src/agent-director/root\n"
	if got := buf.String(); got != want {
		t.Errorf("Write() produced:\n%q\nwant:\n%q", got, want)
	}
}

// TestFilesWrittenByTheOlderCodeStillRead pins the other direction of
// compatibility. These are lines the previous writer could produce; none of
// them may change meaning now that quoting exists.
func TestFilesWrittenByTheOlderCodeStillRead(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		line string
		want string
	}{
		{"plain", `title = do the thing`, "do the thing"},
		{"padded", `title =   do the thing  `, "do the thing"},
		{"empty", `title = `, ""},
		{"quoted title keeps its quotes", `title = "done"`, `"done"`},
		{"quoted sentence keeps its quotes", `note = "it works", he said`, `"it works", he said`},
		{"a quoted path keeps its quotes", `dir = "/tmp/a b"`, `"/tmp/a b"`},
		{"an unbalanced quote is left alone", `note = "half quoted`, `"half quoted`},
		{"a windows path is left alone", `dir = "C:\Users\me"`, `"C:\Users\me"`},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			file, err := Parse(strings.NewReader(testCase.line + "\n"))
			if err != nil {
				t.Fatalf("Parse(%q) = %v, want no error", testCase.line, err)
			}
			key, _, _ := strings.Cut(testCase.line, " ")
			if got := file.Global.Get(key); got != testCase.want {
				t.Errorf("Parse(%q) read %q, want %q", testCase.line, got, testCase.want)
			}
		})
	}
}

// TestWriteRefusesAKeyItCannotReadBack — there is no escaping for a key, so a
// key that would come back as something else has to be an error rather than a
// file that cannot be read afterwards.
func TestWriteRefusesAKeyItCannotReadBack(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		key   string
		value string
	}{
		{"empty key", "", "x"},
		{"key with an equals sign", "a=b", "x"},
		{"key with a newline", "a\nb", "x"},
		{"key that starts a section", "[a", "x"},
		{"key that starts a comment", "#a", "x"},
		{"padded key", " a ", "x"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			file := &File{Global: Entries{{Key: testCase.key, Value: testCase.value}}}
			if err := Write(&bytes.Buffer{}, file); err == nil {
				t.Errorf("Write(key %q) = nil error, want a refusal", testCase.key)
			}
		})
	}
}

func TestWriteRefusesASectionNameItCannotReadBack(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"", " padded ", "two\nlines"} {
		file := &File{}
		file.SetSection(name, Entries{{Key: "a", Value: "1"}})
		if err := Write(&bytes.Buffer{}, file); err == nil {
			t.Errorf("Write(section %q) = nil error, want a refusal", name)
		}
	}
}

// FuzzValueRoundTrip is the same guarantee over strings nobody thought of.
func FuzzValueRoundTrip(f *testing.F) {
	for _, testCase := range awkwardValues {
		f.Add(testCase.value)
	}
	f.Add("=")
	f.Add("[x]")
	f.Add("\\")

	f.Fuzz(func(t *testing.T, value string) {
		original := &File{}
		original.Global.Set("note", value)

		var buf bytes.Buffer
		if err := Write(&buf, original); err != nil {
			t.Fatalf("Write(%q) = %v, want no error", value, err)
		}
		reparsed, err := Parse(bytes.NewReader(buf.Bytes()))
		if err != nil {
			t.Fatalf("Parse() = %v after writing %q\nwritten:\n%q", err, value, buf.String())
		}
		if got := reparsed.Global.Get("note"); got != value {
			t.Errorf("round trip of %q gave %q\nwritten:\n%q", value, got, buf.String())
		}
	})
}
