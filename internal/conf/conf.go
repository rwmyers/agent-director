// Package conf reads and writes agent-director's configuration format: a
// line-oriented key = value file with [section] headers.
//
// One format serves two very different files. director.conf is hand-edited,
// stable, and safe to commit; the per-director state files are machine-written
// and churn constantly. Using one parser for both means a human can always read
// what the machine wrote, and a bug in the writer shows up as a file somebody
// can open rather than a corrupt blob.
//
// The shape is deliberately small:
//
//	# a comment; only at the start of a line
//	description = review, then implement
//
//	[task.review]
//	permissions = read-only
//	progress    = triaging, reading, delivered
//
// Entries are kept as an ordered list rather than a map, because repeated keys
// are how a section stores a list. Get gives first-wins scalar access and All
// gives every value for a key.
//
// A value is everything after the first '=' on the line, trimmed. There are no
// inline comments, so a value may contain '#' and '=' freely — which matters
// because notes and briefs are written by humans and by models, and neither
// avoids punctuation on our behalf.
//
// A value that cannot survive that round trip as it stands — one with a newline
// in it, or with leading or trailing whitespace the reader would trim away — is
// written as a Go quoted string instead:
//
//	answer = "first paragraph\n\nsecond paragraph"
//
// Quoting happens only when it is needed, so an ordinary single-line value is
// written exactly as it always was, every file already on disk still reads the
// same way, and an older binary still understands everything it understood
// before. See needsQuoting for why a value that merely looks quoted is left
// alone.
package conf

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Entry is one key = value line.
type Entry struct {
	Key   string
	Value string
}

// Entries is an ordered list of key = value lines.
type Entries []Entry

// Get returns the first value for key, or "" when the key is absent. First-wins
// rather than last-wins so that a repeated key reads as a list with a head,
// not as an accidental override.
func (e Entries) Get(key string) string {
	for _, entry := range e {
		if entry.Key == key {
			return entry.Value
		}
	}
	return ""
}

// Has reports whether key appears at all, which is how a caller tells an
// explicitly empty value from an absent one.
func (e Entries) Has(key string) bool {
	for _, entry := range e {
		if entry.Key == key {
			return true
		}
	}
	return false
}

// All returns every value for key, in file order.
func (e Entries) All(key string) []string {
	var found []string
	for _, entry := range e {
		if entry.Key == key {
			found = append(found, entry.Value)
		}
	}
	return found
}

// Set replaces every existing value for key with one entry, appending if the
// key is absent. Used by the state writer, where a key is always scalar.
func (e *Entries) Set(key, value string) {
	for i := range *e {
		if (*e)[i].Key == key {
			(*e)[i].Value = value
			// Drop any later duplicates so the file stays canonical.
			rest := (*e)[:i+1]
			for _, entry := range (*e)[i+1:] {
				if entry.Key != key {
					rest = append(rest, entry)
				}
			}
			*e = rest
			return
		}
	}
	*e = append(*e, Entry{Key: key, Value: value})
}

// Section is a [name] block and its entries.
type Section struct {
	Name    string
	Entries Entries
}

// File is a parsed configuration file: the entries before any section header,
// then the sections in file order.
type File struct {
	Global   Entries
	Sections []Section
}

// Section returns the named section.
func (f *File) Section(name string) (Section, bool) {
	for _, section := range f.Sections {
		if section.Name == name {
			return section, true
		}
	}
	return Section{}, false
}

// SectionsWithPrefix returns every section whose name starts with prefix,
// sorted by name so that iteration order does not depend on how the file was
// written. Callers use it to find every [task.*] or [engagement.*] at once.
func (f *File) SectionsWithPrefix(prefix string) []Section {
	var found []Section
	for _, section := range f.Sections {
		if strings.HasPrefix(section.Name, prefix) {
			found = append(found, section)
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Name < found[j].Name })
	return found
}

// SetSection replaces the named section's entries, appending the section if it
// is absent.
func (f *File) SetSection(name string, entries Entries) {
	for i := range f.Sections {
		if f.Sections[i].Name == name {
			f.Sections[i].Entries = entries
			return
		}
	}
	f.Sections = append(f.Sections, Section{Name: name, Entries: entries})
}

// DeleteSection removes the named section if present.
func (f *File) DeleteSection(name string) {
	for i := range f.Sections {
		if f.Sections[i].Name == name {
			f.Sections = append(f.Sections[:i], f.Sections[i+1:]...)
			return
		}
	}
}

// needsQuoting reports whether a value has to be written quoted to come back
// unchanged.
//
// Two reasons, and no others. A value with a newline in it would be written as
// several lines and read back as a broken file — that is the bug this exists
// for. A value whose leading or trailing whitespace matters would come back
// trimmed.
//
// The loop is the subtle part, and it is what keeps files written by the older
// code readable. A value that is itself a quoted string is indistinguishable on
// the page from one this writer quoted, so the tie is broken by asking what the
// writer would have done: unquote it, and if the result would not itself have
// needed quoting then the writer would never have produced those quotes, so
// they are the value's own and the reader must keep them. `title = "done"` in a
// file written last month still reads as `"done"`, quotes included, while
// `answer = "a\nb"` written by this code reads as two lines. The same rule runs
// in the writer and the reader, which is what makes the two agree.
func needsQuoting(value string) bool {
	for {
		if value != strings.TrimSpace(value) || strings.ContainsAny(value, "\n\r") {
			return true
		}
		inner, ok := unquoteValue(value)
		if !ok {
			return false
		}
		value = inner
	}
}

// encodeValue renders a value for one line of the file.
func encodeValue(value string) string {
	if needsQuoting(value) {
		return strconv.Quote(value)
	}
	return value
}

// decodeValue reads back what encodeValue wrote.
func decodeValue(text string) string {
	if inner, ok := unquoteValue(text); ok && needsQuoting(inner) {
		return inner
	}
	return text
}

// unquoteValue interprets text as a Go quoted string. Only double quotes count:
// Go's own unquoting also accepts backticks and rune literals, and treating a
// value that happens to start with a backtick as a raw string would corrupt it.
func unquoteValue(text string) (string, bool) {
	if len(text) < 2 || text[0] != '"' || text[len(text)-1] != '"' {
		return "", false
	}
	inner, err := strconv.Unquote(text)
	if err != nil {
		return "", false
	}
	return inner, true
}

// ParseError is a line this parser could not read.
//
// It carries the file, the line and — when the line looks like the rest of a
// value that was written across several lines — the key it belongs to, because
// "line 23 is not a key = value line" sends somebody to the wrong place: the
// damage is on line 22, in the value of a key they can name.
type ParseError struct {
	// Path is the file, when it was read from one.
	Path string
	// Line is the offending line number, counting from 1.
	Line int
	// Text is the offending line, truncated for display.
	Text string
	// Reason is what is wrong with it.
	Reason string
	// Key and KeyLine name the entry this line appears to continue, when there
	// is one. Empty otherwise.
	Key     string
	KeyLine int
}

func (e *ParseError) Error() string {
	var out strings.Builder
	if e.Path != "" {
		out.WriteString(e.Path + ": ")
	}
	fmt.Fprintf(&out, "line %d: %s", e.Line, e.Reason)
	if e.Text != "" {
		fmt.Fprintf(&out, ": %s", e.Text)
	}
	if e.Key != "" {
		fmt.Fprintf(&out, "\n  this looks like the rest of %q, whose value starts on line %d and runs onto this one.\n"+
			"  Run `director repair` to fold it back into the value.", e.Key, e.KeyLine)
	}
	return out.String()
}

// displayText shortens a line for an error message. A value written across
// several lines can be a whole paragraph, and an error message that quotes all
// of it buries the part that says what to do.
func displayText(text string) string {
	const limit = 60
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

// Parse reads a configuration file.
func Parse(r io.Reader) (*File, error) {
	file := &File{}
	current := -1 // index into file.Sections; -1 means the global block

	// The last key seen, so that a line which is really the rest of that key's
	// value can say so instead of pointing at itself.
	lastKey, lastKeyLine := "", 0

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}

		if strings.HasPrefix(text, "[") {
			if !strings.HasSuffix(text, "]") {
				return nil, &ParseError{Line: line, Text: displayText(text), Reason: "unterminated section header"}
			}
			name := strings.TrimSpace(text[1 : len(text)-1])
			if name == "" {
				return nil, &ParseError{Line: line, Reason: "empty section name"}
			}
			file.Sections = append(file.Sections, Section{Name: name})
			current = len(file.Sections) - 1
			lastKey, lastKeyLine = "", 0
			continue
		}

		key, value, found := strings.Cut(text, "=")
		if !found {
			return nil, &ParseError{
				Line:    line,
				Text:    displayText(text),
				Reason:  "not a key = value line",
				Key:     lastKey,
				KeyLine: lastKeyLine,
			}
		}
		entry := Entry{Key: strings.TrimSpace(key), Value: decodeValue(strings.TrimSpace(value))}
		if entry.Key == "" {
			return nil, &ParseError{Line: line, Text: displayText(text), Reason: "empty key"}
		}
		lastKey, lastKeyLine = entry.Key, line

		if current < 0 {
			file.Global = append(file.Global, entry)
		} else {
			file.Sections[current].Entries = append(file.Sections[current].Entries, entry)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading configuration: %w", err)
	}
	return file, nil
}

// ParseFile reads a configuration file from disk, naming the path in any error
// so a misconfigured layer is findable without guessing which one it was.
func ParseFile(path string) (*File, error) {
	handle, err := os.Open(path) // #nosec G304 -- paths come from config resolution
	if err != nil {
		return nil, err
	}
	defer func() { _ = handle.Close() }()

	file, err := Parse(handle)
	if err != nil {
		var parseErr *ParseError
		if errors.As(err, &parseErr) {
			parseErr.Path = path
			return nil, parseErr
		}
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return file, nil
}

// Write renders a file. Round-tripping loses comments and blank lines, which is
// why only machine-written files are ever rewritten; hand-edited config is read
// and never rewritten in place.
//
// Every value it writes is readable by Parse and comes back byte-identical. A
// key or section name that cannot be represented is an error rather than a file
// nothing can read afterwards: there is no escaping for those, and the caller
// choosing such a name has a bug that is worth hearing about at the moment it
// happens.
func Write(w io.Writer, file *File) error {
	out := bufio.NewWriter(w)
	if err := writeEntries(out, file.Global); err != nil {
		return err
	}
	for _, section := range file.Sections {
		if err := checkSectionName(section.Name); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(out, "\n[%s]\n", section.Name); err != nil {
			return err
		}
		if err := writeEntries(out, section.Entries); err != nil {
			return err
		}
	}
	return out.Flush()
}

func writeEntries(out io.Writer, entries Entries) error {
	for _, entry := range entries {
		if err := checkKey(entry.Key); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(out, "%s = %s\n", entry.Key, encodeValue(entry.Value)); err != nil {
			return err
		}
	}
	return nil
}

// checkKey refuses a key that would not read back as itself. A key is a name
// this program chose, not free text, so this is a programming error rather than
// something to escape around.
func checkKey(key string) error {
	switch {
	case key == "":
		return errors.New("cannot write an entry with an empty key")
	case key != strings.TrimSpace(key):
		return fmt.Errorf("cannot write key %q: leading or trailing whitespace would be lost", key)
	case strings.ContainsAny(key, "=\n\r"):
		return fmt.Errorf("cannot write key %q: a key may not contain '=' or a newline", key)
	case strings.HasPrefix(key, "#") || strings.HasPrefix(key, "["):
		return fmt.Errorf("cannot write key %q: a key may not start with '#' or '['", key)
	}
	return nil
}

func checkSectionName(name string) error {
	switch {
	case name == "":
		return errors.New("cannot write a section with an empty name")
	case name != strings.TrimSpace(name):
		return fmt.Errorf("cannot write section %q: leading or trailing whitespace would be lost", name)
	case strings.ContainsAny(name, "\n\r"):
		return fmt.Errorf("cannot write section %q: a section name may not contain a newline", name)
	}
	return nil
}

// WriteFile writes a file atomically: a temp file in the same directory, then a
// rename. Readers take no lock, so this is what stops them seeing a half-written
// state file — rename is atomic, so a reader gets the old bytes or the new ones.
func WriteFile(path string, file *File) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	temp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer func() { _ = os.Remove(tempName) }()

	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if err := Write(temp, file); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, path)
}

// List splits a comma-separated value, trimming and dropping empties. Used for
// every list-shaped key so that "a, b,c" and "a,b,c" mean the same thing.
func List(value string) []string {
	var items []string
	for _, part := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			items = append(items, trimmed)
		}
	}
	return items
}
