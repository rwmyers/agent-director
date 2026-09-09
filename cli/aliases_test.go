package cli

import (
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// findCommand returns the top-level command with this name, failing if there
// is none.
func findCommand(t *testing.T, name string) *cobra.Command {
	t.Helper()
	for _, cmd := range newRootCmd().Commands() {
		if cmd.Name() == name {
			return cmd
		}
	}
	t.Fatalf("no %q command", name)
	return nil
}

// spokenNames is every word a command answers to.
func spokenNames(cmd *cobra.Command) []string {
	return append([]string{cmd.Name()}, cmd.Aliases...)
}

// TestNoTwoCommandsAnswerToTheSameName is the guard the whole namespace rests
// on. Cobra resolves the first command that claims a name and says nothing
// about the one it shadowed, so a collision is invisible at the point it is
// introduced and shows up later as a command doing something else entirely.
//
// That is not hypothetical here: `rm` was an alias of `retire` and was asked
// for on `remove`, and the two delete at wildly different granularities — one
// engagement, or a whole director and its memory of every engagement it holds.
// This is what makes moving it a deliberate act rather than a silent one.
func TestNoTwoCommandsAnswerToTheSameName(t *testing.T) {
	claimed := map[string]string{}
	for _, cmd := range newRootCmd().Commands() {
		for _, spoken := range spokenNames(cmd) {
			if owner, taken := claimed[spoken]; taken {
				t.Errorf("%q is claimed by both %q and %q; cobra will resolve it to one of them silently",
					spoken, owner, cmd.Name())
				continue
			}
			claimed[spoken] = cmd.Name()
		}
	}
}

// TestRmMeansRemove pins which command owns `rm`, in both directions.
//
// Uniqueness alone would be satisfied by `rm` going back to `retire`, and that
// is the regression worth naming outright: the word reads as "remove"
// everywhere else a person has ever typed it, and pointing it at the command
// that deletes a director and its entire fleet record is the trap this
// namespace was rearranged to close.
func TestRmMeansRemove(t *testing.T) {
	const alias = "rm"

	remove := findCommand(t, "remove")
	if !slices.Contains(remove.Aliases, alias) {
		t.Errorf("director remove answers to %v, want it to include %q", remove.Aliases, alias)
	}
	retire := findCommand(t, "retire")
	if slices.Contains(spokenNames(retire), alias) {
		t.Errorf("director retire answers to %v, want %q to belong to remove alone", spokenNames(retire), alias)
	}

	// And that it actually resolves, rather than merely being declared: the
	// alias is only worth anything if cobra dispatches on it.
	found, _, err := newRootCmd().Find([]string{alias})
	if err != nil {
		t.Fatalf("Find(%q) = %v, want it to resolve", alias, err)
	}
	if found.Name() != "remove" {
		t.Errorf("director %s resolves to %q, want %q", alias, found.Name(), "remove")
	}
}

// TestRemoveAdvertisesItsAliases checks the aliases are discoverable to
// somebody reading the help rather than the source, which was the whole ask:
// `rm` does not need to be a listed top-level option, it needs to be findable
// by anybody reading `director remove --help`. Cobra prints an Aliases line
// without being asked, and this is what says so.
func TestRemoveAdvertisesItsAliases(t *testing.T) {
	remove := findCommand(t, "remove")
	if len(remove.Aliases) == 0 {
		t.Fatal("director remove declares no aliases")
	}
	help := remove.UsageString()
	for _, alias := range remove.Aliases {
		if !strings.Contains(help, alias) {
			t.Errorf("director remove --help does not mention its alias %q:\n%s", alias, help)
		}
	}
}

// TestSetupOwnsInstall pins the rename of `install` to `setup`, in both
// directions.
//
// `install` is in the shipped getting-started instructions and in people's
// setup scripts, so it has to keep resolving to the same command; and `setup`
// has to stay this command's, because a word that reads as "set this project
// up" pointing anywhere else is the same silent shadowing the uniqueness guard
// above exists to catch.
func TestSetupOwnsInstall(t *testing.T) {
	const alias = "install"

	setup := findCommand(t, "setup")
	if !slices.Contains(setup.Aliases, alias) {
		t.Errorf("director setup answers to %v, want it to include %q", setup.Aliases, alias)
	}

	// Both words dispatch, rather than merely being declared.
	for _, spoken := range []string{"setup", alias} {
		found, _, err := newRootCmd().Find([]string{spoken})
		if err != nil {
			t.Fatalf("Find(%q) = %v, want it to resolve", spoken, err)
		}
		if found.Name() != "setup" {
			t.Errorf("director %s resolves to %q, want %q", spoken, found.Name(), "setup")
		}
	}
}

// TestSetupBelongsToSetupAlone is the half of the rename that a later change
// could undo without noticing: `setup` was free when this command took it, and
// nothing stops another command claiming it as a name or an alias tomorrow.
// Cobra would resolve one of the two and say nothing.
func TestSetupBelongsToSetupAlone(t *testing.T) {
	for _, cmd := range newRootCmd().Commands() {
		if cmd.Name() == "setup" {
			continue
		}
		if slices.Contains(spokenNames(cmd), "setup") {
			t.Errorf("director %s also answers to %q, want it to belong to setup alone", cmd.Name(), "setup")
		}
	}
}

// TestSetupAdvertisesItsAlias checks `director setup --help` names `install`,
// so somebody who had the old word in their fingers can see where it went
// without reading the source.
func TestSetupAdvertisesItsAlias(t *testing.T) {
	setup := findCommand(t, "setup")
	help := setup.UsageString()
	if !strings.Contains(help, "install") {
		t.Errorf("director setup --help does not mention its alias %q:\n%s", "install", help)
	}
}
