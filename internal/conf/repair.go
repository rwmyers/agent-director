package conf

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// Fix is one value that was found spread across several lines and joined back
// together.
type Fix struct {
	// Key is the entry whose value was broken.
	Key string `json:"key"`
	// Section is the section it is in, empty for the global block.
	Section string `json:"section,omitempty"`
	// Line is where the entry starts.
	Line int `json:"line"`
	// Folded is how many following lines were folded back into its value.
	Folded int `json:"folded_lines"`
}

// RepairReport is what a repair found in one file.
type RepairReport struct {
	Path string `json:"path"`
	// Fixes is empty when the file was already readable.
	Fixes []Fix `json:"fixes,omitempty"`
	// Applied says whether the repaired file was written back.
	Applied bool `json:"applied"`
}

// Repaired reports whether there was anything to fix.
func (r *RepairReport) Repaired() bool { return len(r.Fixes) > 0 }

// RepairFile makes an unreadable file readable again, in the one way it can be
// done without a person deciding anything: a value that runs onto the following
// lines is joined back into one value, keeping the newlines, and the file is
// rewritten with that value quoted so it stays readable.
//
// Nothing is discarded. If a line cannot be understood as the continuation of
// some earlier key — there is no earlier key at all, say — the repair fails and
// says which line, because guessing there would mean throwing away whatever
// somebody actually wrote.
//
// A file that is already both readable and sound is left exactly as it is, so
// this can never damage a healthy director. Repairing rewrites through Write,
// which loses comments and blank lines, so this is for machine-written files:
// state, not hand-edited config.
func RepairFile(path string, apply bool) (*RepairReport, error) {
	report := &RepairReport{Path: path}

	parsed, err := ParseFile(path)
	if err == nil && !shredded(parsed) {
		return report, nil
	}
	var parseErr *ParseError
	if err != nil && !errors.As(err, &parseErr) {
		return nil, err
	}

	data, err := os.ReadFile(path) // #nosec G304 -- the path is a state file this program wrote
	if err != nil {
		return nil, err
	}
	file, fixes, err := repair(bytes.NewReader(data))
	if err != nil {
		if errors.As(err, &parseErr) {
			parseErr.Path = path
		}
		return nil, err
	}
	report.Fixes = fixes

	if apply && len(fixes) > 0 {
		if err := WriteFile(path, file); err != nil {
			return nil, err
		}
		report.Applied = true
	}
	return report, nil
}

// shredded reports whether a file parsed but into entries nothing could have
// written.
//
// This is the quieter half of the same damage. A value written across several
// lines whose second line happens to contain an '=' — "the alternative changes
// every line = every file churns" — parses without complaint, as a key with
// spaces in it, and the rest of that sentence becomes its value. The file reads
// fine and the answer somebody sent has been cut in half. A key with a space in
// it is not something this program can produce, so it is the damage showing.
func shredded(file *File) bool {
	if anyImplausibleKey(file.Global) {
		return true
	}
	for _, section := range file.Sections {
		if anyImplausibleKey(section.Entries) {
			return true
		}
	}
	return false
}

func anyImplausibleKey(entries Entries) bool {
	for _, entry := range entries {
		if !plausibleKey(entry.Key) {
			return true
		}
	}
	return false
}

// repair parses leniently, folding every line that is not a key, a section or a
// comment into the value of the key above it.
func repair(r io.Reader) (*File, []Fix, error) {
	file := &File{}
	current := -1 // index into file.Sections; -1 means the global block
	var fixes []Fix

	// The entry a stray line would belong to, and what has been folded into it
	// so far.
	openEntry, openLine, folded := -1, 0, 0
	// Blank and comment lines are held back rather than dropped: a blank line
	// between two paragraphs of a broken value is part of the value, but the
	// blank line before a section header is just layout. Which one it was is
	// only known once the next real line arrives.
	var pending []string

	entries := func() *Entries {
		if current < 0 {
			return &file.Global
		}
		return &file.Sections[current].Entries
	}
	sectionName := func() string {
		if current < 0 {
			return ""
		}
		return file.Sections[current].Name
	}
	closeEntry := func() {
		if openEntry >= 0 && folded > 0 {
			fixes = append(fixes, Fix{
				Key:     (*entries())[openEntry].Key,
				Section: sectionName(),
				Line:    openLine,
				Folded:  folded,
			})
		}
		openEntry, openLine, folded = -1, 0, 0
		pending = nil
	}

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for line := 1; scanner.Scan(); line++ {
		raw := strings.TrimRight(scanner.Text(), "\r")
		text := strings.TrimSpace(raw)

		if text == "" || strings.HasPrefix(text, "#") {
			if openEntry >= 0 {
				pending = append(pending, raw)
			}
			continue
		}

		if strings.HasPrefix(text, "[") && strings.HasSuffix(text, "]") {
			name := strings.TrimSpace(text[1 : len(text)-1])
			if name == "" {
				return nil, nil, &ParseError{Line: line, Reason: "empty section name"}
			}
			closeEntry()
			file.Sections = append(file.Sections, Section{Name: name})
			current = len(file.Sections) - 1
			continue
		}

		// A key with a space in it is prose, not a key: nothing in this program
		// writes one, so a line like "the fix = escaping" is a sentence out of
		// somebody's paragraph rather than the start of a new entry.
		if key, value, found := strings.Cut(text, "="); found && plausibleKey(strings.TrimSpace(key)) {
			closeEntry()
			*entries() = append(*entries(), Entry{
				Key:   strings.TrimSpace(key),
				Value: decodeValue(strings.TrimSpace(value)),
			})
			openEntry, openLine = len(*entries())-1, line
			continue
		}

		if openEntry < 0 {
			return nil, nil, &ParseError{
				Line:   line,
				Text:   displayText(text),
				Reason: "not a key = value line, and there is no earlier key whose value it could be part of",
			}
		}

		entry := &(*entries())[openEntry]
		for _, held := range pending {
			entry.Value += "\n" + held
		}
		entry.Value += "\n" + raw
		folded += len(pending) + 1
		pending = nil
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("reading configuration: %w", err)
	}
	closeEntry()

	return file, fixes, nil
}

// plausibleKey reports whether a name is one this program could have written.
func plausibleKey(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
		case (r == '.' || r == '-') && i > 0:
		default:
			return false
		}
	}
	return true
}
