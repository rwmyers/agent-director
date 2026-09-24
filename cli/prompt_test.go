package cli

import (
	"io"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
)

// renderMultiSelect draws the field the way `p.run` does on a terminal: the
// same form, then the window size huh gets from bubbletea before it first
// paints.
//
// The size is given rather than taken from the machine so the wrapping is the
// test's own. Eighty columns is narrow enough for the descriptions here to wrap
// onto a second line, and forty rows is more than enough to hold every option —
// so anything missing from the view is missing because the field mismeasured
// itself, not because it ran out of terminal.
func renderMultiSelect(t *testing.T, title, description string, options []huh.Option[string]) string {
	t.Helper()
	var chosen []string
	field := multiSelectField(title, description, options, &chosen)
	form := huh.NewForm(huh.NewGroup(field)).WithShowHelp(true)
	form.Init()
	model, _ := form.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	return model.View()
}

func TestMultiSelectShowsEveryOption(t *testing.T) {
	t.Parallel()

	// The description that wraps is not incidental. `director setup` asks
	// with one of these, and on an eighty-column terminal it takes two lines —
	// which is how a two-option question came to render one option, with the
	// other below the fold and nothing on screen to say so.
	const description = "Skills are how an agent learns to act as a director. Pick every harness you direct from."

	cases := map[string][]string{
		"two options":  {"Antigravity (paths unverified)", "Claude Code"},
		"five options": {"One", "Two", "Three", "Four", "Five"},
	}
	for name, labels := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			options := make([]huh.Option[string], 0, len(labels))
			for _, label := range labels {
				options = append(options, huh.NewOption(label, label))
			}
			view := renderMultiSelect(t, "Which harness should be able to direct?", description, options)
			for _, label := range labels {
				if !strings.Contains(view, label) {
					t.Errorf("option %q is not on screen; a choice nobody can see is a choice nobody has:\n%s", label, view)
				}
			}
		})
	}
}

// removeShapedList and retireShapedList are the two pick lists this binary
// asks with, reduced to the part under test: what the confirmation is called
// and which of its buttons opens selected.
//
// Each builds its own options. huh's Options(opts...) keeps the backing array
// it is handed and marks selections in place, so a slice shared between two
// pick lists arrives at the second one already toggled.
func removeShapedList() pickList {
	return pickList{
		title:          "Remove which engagements?",
		verb:           "Remove",
		noun:           "engagement",
		defaultConfirm: true,
		options:        []huh.Option[string]{huh.NewOption("eng_one  the one to forget", "eng_one")},
	}
}

func retireShapedList() pickList {
	return pickList{
		title:   "Retire which directors?",
		verb:    "Retire",
		noun:    "director",
		options: []huh.Option[string]{huh.NewOption("dir_one  the fleet", "dir_one")},
	}
}

// pickAnswering runs a pick list against a scripted answer in accessible mode.
//
// Accessible mode is line-based, so "the answer was just Enter" is writable as
// a script: `byteReader` hands the confirmation one final newline once the
// script runs out, which is exactly the empty line huh reads as "take the
// default".
func pickAnswering(t *testing.T, list pickList, script string) []string {
	t.Helper()
	p := prompter{in: strings.NewReader(script), out: io.Discard, terminal: true, accessible: true}
	chosen, err := p.pick(list)
	if err != nil {
		t.Fatalf("pick(%s) = %v, want no error", list.verb, err)
	}
	return chosen
}

func TestRemovePickListConfirmationDefaultsToRemoving(t *testing.T) {
	t.Parallel()

	// The first option toggled on, 0 to finish the multi-select, and then
	// nothing — the confirmation gets an empty answer and has to fall back on
	// its default.
	chosen := pickAnswering(t, removeShapedList(), "1\n0\n")

	if len(chosen) != 1 || chosen[0] != "eng_one" {
		t.Errorf("pick chose %v, want the confirmation to default to removing what was already picked", chosen)
	}
}

func TestRetirePickListConfirmationStillDefaultsToCancel(t *testing.T) {
	t.Parallel()

	// Same script, the other list. Retiring a director throws away its whole
	// record of a fleet, so the default answer there stays "no".
	chosen := pickAnswering(t, retireShapedList(), "1\n0\n")

	if len(chosen) != 0 {
		t.Errorf("pick chose %v, want retire's confirmation to still default to Cancel", chosen)
	}
}

func TestPickListConfirmationStillTakesAnExplicitAnswer(t *testing.T) {
	t.Parallel()

	// A default is not an override: "n" at a confirmation that opens on Remove
	// still declines, and "y" at one that opens on Cancel still goes ahead.
	if chosen := pickAnswering(t, removeShapedList(), "1\n0\nn\n"); len(chosen) != 0 {
		t.Errorf("pick chose %v after an explicit no, want nothing", chosen)
	}
	if chosen := pickAnswering(t, retireShapedList(), "1\n0\ny\n"); len(chosen) != 1 {
		t.Errorf("pick chose %v after an explicit yes, want the chosen row", chosen)
	}
}

// confirmAborting asks a confirmation with the given default and interrupts it
// the way Ctrl-C does.
//
// This is the full TUI rather than accessible mode, because aborting is a thing
// only the TUI has: 0x03 is what a terminal sends on Ctrl-C, and huh turns it
// into ErrUserAborted. Esc arrives at the same place by the same route.
func confirmAborting(t *testing.T, defaultAffirmative bool) bool {
	t.Helper()
	p := prompter{in: strings.NewReader("\x03"), out: io.Discard, terminal: true}
	confirmed, err := p.confirm("Remove 1 engagement(s)?", "eng_one", "Remove", "Cancel", defaultAffirmative)
	if err != nil {
		t.Fatalf("confirm = %v, want aborting to be a normal outcome", err)
	}
	return confirmed
}

func TestAbortingAConfirmationDeclinesWhicheverWayItOpened(t *testing.T) {
	t.Parallel()

	// The whole risk of moving the default: the abort path returns early, and
	// if it ever returned the bound value instead of a flat no, backing out of
	// a removal would become the removal.
	for _, defaultAffirmative := range []bool{true, false} {
		if confirmAborting(t, defaultAffirmative) {
			t.Errorf("aborting a confirmation defaulting to %v = true, want an abort to read as no", defaultAffirmative)
		}
	}
}

func TestRemoveCommandsPickListDefaultsToRemoving(t *testing.T) {
	// The tests above ask a pick list shaped like remove's. This one asks
	// remove's own, through the command, so that the flag going missing from
	// cli/remove.go is a failure and not just a shape mismatch. It lives here
	// rather than beside the other remove tests so the two defaults — the one
	// that changed and the one that did not — read together.
	root, _ := fleetRoot(t)
	id := spawnEngagement(t, root, "the one to forget")

	// The row toggled on, 0 to finish, and then no answer at all to the
	// confirmation.
	setRemovePrompter(t, answering("1\n0\n"))

	if err := runDirector(t, "remove", "--config", root); err != nil {
		t.Fatalf("remove = %v, want no error", err)
	}
	if _, ok := fleet(t, root)[id]; ok {
		t.Errorf("state still holds %s, want the confirmation to have defaulted to removing it", id)
	}
}
