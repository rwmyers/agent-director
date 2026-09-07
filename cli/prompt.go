package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/x/term"
)

// promptField is the part of a huh field this package uses. Both
// *huh.Select and *huh.MultiSelect satisfy it.
type promptField interface {
	huh.Field
	RunAccessible(w io.Writer, r io.Reader) error
}

// prompter drives interactive questions with huh.
//
// On a terminal the fields render as a full TUI. When stdin is not a terminal
// it falls back to huh's accessible line-based mode, so the same command still
// works from a script or a pipe — which matters here because `director install`
// is exactly the sort of thing somebody puts in a setup script.
type prompter struct {
	in  io.Reader
	out io.Writer
	// terminal is whether stdin is a terminal. It decides both whether a
	// question can honestly be asked at all and whether huh renders its full
	// TUI.
	terminal bool
	// accessible forces huh's line-based mode even on a terminal. Only a test
	// sets it, so that the asking path can be exercised against scripted input
	// without a pty.
	accessible bool
}

func newPrompter() prompter {
	return prompter{in: os.Stdin, out: os.Stdout, terminal: term.IsTerminal(os.Stdin.Fd())}
}

// byteReader hands out at most one byte per Read.
//
// Accessible mode builds a fresh bufio.Scanner for every prompt it issues, and
// a buffered reader would read past the newline and swallow the input meant for
// the next prompt. Reading a byte at a time keeps each scanner from consuming
// more than its own line.
//
// Once the underlying reader is exhausted it yields one final newline before
// reporting EOF. huh's accessible prompts re-read after rejecting a line but
// hold on to the rejected text, so input ending straight after an invalid
// answer would be answered with something the field cannot parse — in a select
// that means indexing its options with -1.
type byteReader struct {
	r    io.Reader
	done bool
}

func (b *byteReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	n, err := b.r.Read(p[:1])
	if n == 0 && errors.Is(err, io.EOF) && !b.done {
		b.done = true
		p[0] = '\n'
		return 1, nil
	}
	return n, err
}

func (p prompter) run(field promptField) error {
	if !p.terminal || p.accessible {
		return field.RunAccessible(p.out, &byteReader{r: p.in})
	}
	// The field's own Run() builds a form with the help footer switched off,
	// which would leave the keybindings undiscoverable, so build it here.
	//
	// The reader and writer are handed over explicitly. In production they are
	// os.Stdin and os.Stdout, which is what the form would have used anyway;
	// saying so lets a test drive the full TUI — an abort, in particular —
	// without a pty.
	return huh.NewForm(huh.NewGroup(field)).WithShowHelp(true).
		WithInput(p.in).WithOutput(p.out).Run()
}

// selectOne asks a single-choice question. Aborting returns the zero value and
// no error, since backing out is a normal outcome rather than a failure.
func (p prompter) selectOne(title, description string, options []huh.Option[string]) (string, error) {
	var chosen string
	field := huh.NewSelect[string]().
		Title(title).
		Description(description).
		Options(options...).
		Value(&chosen)

	if err := p.run(field); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return "", nil
		}
		return "", err
	}
	return chosen, nil
}

// multiSelectField builds the multiple-choice field, with every option on
// screen.
//
// The height is deliberately left unset. huh's Height is the height of the
// whole field — title and description included — and it subtracts those before
// sizing the list, so a height computed from the option count silently loses
// rows off the bottom: a two-option question whose description wraps to two
// lines has one line left for the list, and the second option is below the fold
// with nothing on screen to say the list scrolls. Unset, huh sizes the list to
// the options, and the form still shrinks it to fit a terminal too short to
// hold them — which is a real constraint, unlike the arithmetic.
func multiSelectField(title, description string, options []huh.Option[string], chosen *[]string) *huh.MultiSelect[string] {
	return huh.NewMultiSelect[string]().
		Title(title).
		Description(description).
		Options(options...).
		Value(chosen)
}

// selectMany asks a multiple-choice question.
func (p prompter) selectMany(title, description string, options []huh.Option[string]) ([]string, error) {
	var chosen []string
	field := multiSelectField(title, description, options, &chosen)

	if err := p.run(field); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return nil, nil
		}
		return nil, err
	}
	return chosen, nil
}

// confirm asks a yes/no question.
//
// defaultAffirmative decides which of the two buttons opens selected, and so
// what an answer of just Enter — or, in accessible mode, an empty line — means.
// It is a per-question decision rather than a house style: defaulting to yes is
// reasonable when the person has already said what they want and this is a
// second look at a choice they made, and unreasonable when the confirmation is
// the only thing between one keystroke and something large and irreversible.
//
// Aborting always declines, whatever the default is, so backing out of a
// question never triggers the action it was asking about.
func (p prompter) confirm(title, description, affirmative, negative string, defaultAffirmative bool) (bool, error) {
	confirmed := defaultAffirmative
	field := huh.NewConfirm().
		Title(title).
		Description(description).
		Affirmative(affirmative).
		Negative(negative).
		Value(&confirmed)

	if err := p.run(field); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return false, nil
		}
		return false, err
	}
	return confirmed, nil
}

// pickList is one multiple-choice question about things that are about to be
// deleted: what to offer, and what the deletion is called.
type pickList struct {
	title       string
	description string
	// verb names the action in the confirmation and on its button — "Retire",
	// "Remove".
	verb string
	// noun names one of the things being chosen — "director", "engagement".
	noun string
	// defaultConfirm is whether the confirmation opens on the verb rather than
	// on Cancel.
	//
	// The zero value is the cautious one on purpose: a pick list added later
	// gets Cancel selected unless somebody decides otherwise for it, and the
	// decision is per list because the two lists here are not the same size of
	// mistake. Choosing engagements to remove and then confirming is a second
	// look at rows already picked out by hand; retiring a director discards
	// its whole record of a fleet, and a confirmation that opens on "Retire"
	// makes that one keystroke away.
	defaultConfirm bool
	options        []huh.Option[string]
}

// pick offers a multi-select and then confirms what came back.
//
// The confirmation is not ceremony. A multi-select is one stray keypress away
// from a decision, and every caller here is about to delete a record that
// nothing restores, so the chosen rows are said back before anything happens.
// Backing out at either step returns nothing chosen and no error: declining to
// delete something is a normal outcome rather than a failure.
func (p prompter) pick(list pickList) ([]string, error) {
	chosen, err := p.selectMany(list.title, list.description, list.options)
	if err != nil || len(chosen) == 0 {
		return nil, err
	}

	var labels []string
	for _, option := range list.options {
		for _, value := range chosen {
			if option.Value == value {
				labels = append(labels, option.Key)
			}
		}
	}
	confirmed, err := p.confirm(
		fmt.Sprintf("%s %d %s(s)?", list.verb, len(chosen), list.noun),
		strings.Join(labels, "\n"),
		list.verb, "Cancel", list.defaultConfirm)
	if err != nil || !confirmed {
		return nil, err
	}
	return chosen, nil
}
