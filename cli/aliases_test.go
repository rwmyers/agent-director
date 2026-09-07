package cli

import (
	"strings"
	"testing"
)

// TestNoTwoCommandsAnswerToTheSameName guards the hazard that stopped `rm`
// from being added to `director remove`: `rm` is already an alias of `director
// retire`, and cobra resolves the first command that claims a name without
// saying anything about the one it shadowed.
//
// The two commands that would have collided delete at wildly different
// granularities — one engagement, or a whole director and its memory of every
// engagement it holds — so a name that quietly moved from one to the other is
// not a typo somebody notices. It is checked here rather than left to review
// because nothing in cobra reports it, and adding an alias is a one-line
// change that looks obviously safe.
func TestNoTwoCommandsAnswerToTheSameName(t *testing.T) {
	claimed := map[string]string{}
	for _, cmd := range newRootCmd().Commands() {
		name := cmd.Name()
		for _, spoken := range append([]string{name}, cmd.Aliases...) {
			if owner, taken := claimed[spoken]; taken {
				t.Errorf("%q is claimed by both %q and %q; cobra will resolve it to one of them silently",
					spoken, owner, name)
				continue
			}
			claimed[spoken] = name
		}
	}
}

// TestRemoveAdvertisesItsAliases checks the aliases are discoverable to
// somebody reading the help rather than the source. Cobra prints them without
// being asked, and this is what says so.
func TestRemoveAdvertisesItsAliases(t *testing.T) {
	for _, cmd := range newRootCmd().Commands() {
		if cmd.Name() != "remove" {
			continue
		}
		help := cmd.UsageString()
		for _, alias := range cmd.Aliases {
			if !strings.Contains(help, alias) {
				t.Errorf("director remove --help does not mention its alias %q", alias)
			}
		}
		return
	}
	t.Fatal("no remove command")
}
