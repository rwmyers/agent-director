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
package conf

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
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

// Parse reads a configuration file.
func Parse(r io.Reader) (*File, error) {
	file := &File{}
	current := -1 // index into file.Sections; -1 means the global block

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}

		if strings.HasPrefix(text, "[") {
			if !strings.HasSuffix(text, "]") {
				return nil, fmt.Errorf("line %d: unterminated section header: %s", line, text)
			}
			name := strings.TrimSpace(text[1 : len(text)-1])
			if name == "" {
				return nil, fmt.Errorf("line %d: empty section name", line)
			}
			file.Sections = append(file.Sections, Section{Name: name})
			current = len(file.Sections) - 1
			continue
		}

		key, value, found := strings.Cut(text, "=")
		if !found {
			return nil, fmt.Errorf("line %d: not a key = value line: %s", line, text)
		}
		entry := Entry{Key: strings.TrimSpace(key), Value: strings.TrimSpace(value)}
		if entry.Key == "" {
			return nil, fmt.Errorf("line %d: empty key", line)
		}

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
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return file, nil
}

// Write renders a file. Round-tripping loses comments and blank lines, which is
// why only machine-written files are ever rewritten; hand-edited config is read
// and never rewritten in place.
func Write(w io.Writer, file *File) error {
	out := bufio.NewWriter(w)
	for _, entry := range file.Global {
		if _, err := fmt.Fprintf(out, "%s = %s\n", entry.Key, entry.Value); err != nil {
			return err
		}
	}
	for _, section := range file.Sections {
		if _, err := fmt.Fprintf(out, "\n[%s]\n", section.Name); err != nil {
			return err
		}
		for _, entry := range section.Entries {
			if _, err := fmt.Fprintf(out, "%s = %s\n", entry.Key, entry.Value); err != nil {
				return err
			}
		}
	}
	return out.Flush()
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
