package cli

import (
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

	// The description that wraps is not incidental. `director install` asks
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
