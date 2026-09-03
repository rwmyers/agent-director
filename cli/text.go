package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

// textArg resolves the free text a command works on: a brief, a question, an
// answer, a note, a message.
//
// Every one of those is prose somebody or some model wrote, and prose has
// paragraphs. A positional argument can carry them only through $(cat file),
// which is awkward to write and runs into ARG_MAX on exactly the long briefs
// that most want to be written in a file. --file is the other half of storing
// multi-line values safely: conf escaping made them safe to keep, this makes
// them possible to supply.
//
// args and index name the positional the command would otherwise have taken,
// and file is the --file value. Supplying both is an error rather than a
// precedence rule, because a caller who passed both had one of them in mind and
// silently dropping the other loses whichever it was.
func textArg(args []string, index int, file string) (string, error) {
	positional, given := "", index < len(args)
	if given {
		positional = args[index]
	}

	switch {
	case given && file != "":
		return "", errors.New("give the text as an argument or with --file, not both")
	case given:
		return positional, nil
	case file == "":
		return "", errors.New("no text given: pass it as an argument, or with --file (--file - reads standard input)")
	}

	data, err := readTextFile(file)
	if err != nil {
		return "", err
	}
	// A file is UTF-8 or it is not text, and the state file it would be written
	// into is UTF-8 too. Saying so here names the file; letting it through would
	// surface later as mojibake in a brief nobody can trace back.
	if !utf8.Valid(data) {
		return "", fmt.Errorf("%s is not valid UTF-8", sourceName(file))
	}
	return trimFinalNewline(string(data)), nil
}

// readTextFile reads --file, where "-" means standard input.
func readTextFile(file string) ([]byte, error) {
	if file == "-" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, fmt.Errorf("reading standard input: %w", err)
		}
		return data, nil
	}
	data, err := os.ReadFile(file) // #nosec G304 -- the path is the point of the flag
	if err != nil {
		return nil, fmt.Errorf("reading --file: %w", err)
	}
	return data, nil
}

func sourceName(file string) string {
	if file == "-" {
		return "standard input"
	}
	return file
}

// trimFinalNewline drops at most one trailing line ending and nothing else.
//
// Editors end a file with a newline, and `director note "$(cat n.txt)"` never
// carried it, so keeping it would make --file mean something subtly different
// from the positional it replaces. Exactly one, though: a brief that
// deliberately ends in a blank line still does, and no other whitespace is
// touched, because indentation at the start of a line is content in anything
// with a code block in it.
func trimFinalNewline(text string) string {
	if !strings.HasSuffix(text, "\n") {
		return text
	}
	return strings.TrimSuffix(strings.TrimSuffix(text, "\n"), "\r")
}

// clip shortens a value to fit a fixed-width table cell, marking that it did.
//
// Rune-aware rather than byte-aware, because a path or a branch name may hold
// multi-byte characters and cutting one in half produces a cell no terminal can
// render. The ellipsis is inside the width, so the result never exceeds what
// the column was budgeted.
func clip(value string, width int) string {
	if utf8.RuneCountInString(value) <= width {
		return value
	}
	runes := []rune(value)
	if width <= 1 {
		return "…"
	}
	return string(runes[:width-1]) + "…"
}
