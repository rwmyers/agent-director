package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTemp puts content in a file and returns its path.
func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%s) = %v", path, err)
	}
	return path
}

// withStdin points os.Stdin at content for the duration of one test. Not
// parallel-safe, which is why nothing in this file calls t.Parallel.
func withStdin(t *testing.T, content string) {
	t.Helper()
	path := writeTemp(t, "stdin", content)
	handle, err := os.Open(path) // #nosec G304 -- a path this test just wrote
	if err != nil {
		t.Fatalf("Open(%s) = %v", path, err)
	}
	saved := os.Stdin
	os.Stdin = handle
	t.Cleanup(func() {
		os.Stdin = saved
		_ = handle.Close()
	})
}

func TestTextArg(t *testing.T) {
	t.Run("a positional value is taken as it stands", func(t *testing.T) {
		got, err := textArg([]string{"eng-1", "  two  spaces\tkept"}, 1, "")
		if err != nil {
			t.Fatalf("textArg() = %v, want no error", err)
		}
		if want := "  two  spaces\tkept"; got != want {
			t.Errorf("textArg() = %q, want %q", got, want)
		}
	})

	t.Run("--file reads the file", func(t *testing.T) {
		path := writeTemp(t, "brief.md", "first paragraph\n\nsecond paragraph\n")
		got, err := textArg(nil, 0, path)
		if err != nil {
			t.Fatalf("textArg() = %v, want no error", err)
		}
		if want := "first paragraph\n\nsecond paragraph"; got != want {
			t.Errorf("textArg() = %q, want %q", got, want)
		}
	})

	t.Run("--file - reads standard input", func(t *testing.T) {
		withStdin(t, "piped\nin\n")
		got, err := textArg(nil, 0, "-")
		if err != nil {
			t.Fatalf("textArg() = %v, want no error", err)
		}
		if want := "piped\nin"; got != want {
			t.Errorf("textArg() = %q, want %q", got, want)
		}
	})

	t.Run("a positional and --file together is an error", func(t *testing.T) {
		// Not a precedence rule: whichever one silently lost was something the
		// caller meant to send.
		path := writeTemp(t, "brief.md", "from the file\n")
		_, err := textArg([]string{"from the argument"}, 0, path)
		if err == nil {
			t.Fatal("textArg() = no error, want an error")
		}
		if !strings.Contains(err.Error(), "not both") {
			t.Errorf("textArg() = %v, want it to say both were given", err)
		}
	})

	t.Run("neither a positional nor --file is an error", func(t *testing.T) {
		_, err := textArg(nil, 0, "")
		if err == nil {
			t.Fatal("textArg() = no error, want an error")
		}
	})

	t.Run("at most one trailing newline is stripped", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			content string
			want    string
		}{
			{"the newline an editor adds", "text\n", "text"},
			{"a deliberate blank line survives", "text\n\n", "text\n"},
			{"a CRLF line ending goes with it", "text\r\n", "text"},
			{"no trailing newline at all", "text", "text"},
			{"a lone carriage return is content", "text\r", "text\r"},
			{"leading whitespace is untouched", "  indented\n", "  indented"},
			{"internal blank lines are untouched", "a\n\n\nb\n", "a\n\n\nb"},
			{"an empty file is empty text", "", ""},
			{"a file that is only a newline is empty text", "\n", ""},
		} {
			t.Run(tc.name, func(t *testing.T) {
				path := writeTemp(t, "text.txt", tc.content)
				got, err := textArg(nil, 0, path)
				if err != nil {
					t.Fatalf("textArg() = %v, want no error", err)
				}
				if got != tc.want {
					t.Errorf("textArg(%q) = %q, want %q", tc.content, got, tc.want)
				}
			})
		}
	})

	t.Run("invalid UTF-8 is refused", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "binary")
		if err := os.WriteFile(path, []byte{'o', 'k', 0xff, 0xfe}, 0o600); err != nil {
			t.Fatalf("WriteFile(%s) = %v", path, err)
		}
		_, err := textArg(nil, 0, path)
		if err == nil {
			t.Fatal("textArg() = no error, want an error")
		}
		if !strings.Contains(err.Error(), "UTF-8") {
			t.Errorf("textArg() = %v, want it to name the encoding", err)
		}
		if !strings.Contains(err.Error(), path) {
			t.Errorf("textArg() = %v, want it to name %s", err, path)
		}
	})

	t.Run("a missing file is a clear error", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "not-there.md")
		_, err := textArg(nil, 0, path)
		if err == nil {
			t.Fatal("textArg() = no error, want an error")
		}
		if !strings.Contains(err.Error(), path) {
			t.Errorf("textArg() = %v, want it to name %s", err, path)
		}
		if !strings.Contains(err.Error(), "--file") {
			t.Errorf("textArg() = %v, want it to name the flag", err)
		}
	})
}
