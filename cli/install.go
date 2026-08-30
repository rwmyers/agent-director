package cli

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"
)

// Scope is whether skills are installed for one project or for every project.
type Scope string

const (
	// ScopeProject installs into the harness's project-local configuration, so
	// the skills travel with the repository.
	ScopeProject Scope = "project"
	// ScopeGlobal installs into the harness's user configuration, so every
	// project on this machine has them.
	ScopeGlobal Scope = "global"
)

// host is a harness director knows how to install skills for.
//
// This is the one place a harness name legitimately appears on the director
// side: installing is inherently host-specific — everything else stays neutral.
// A host director has never heard of is not an error; `director skills --cat`
// prints the same text for anyone to place by hand.
type host struct {
	name        string
	description string

	// dirs return where skills live for each scope. Empty means the harness
	// has no such notion and that scope is not offered for it.
	globalDir  func() string
	projectDir func(projectRoot string) string

	// verified records whether these paths were confirmed against a real
	// installation. An unverified guess must say so rather than look as
	// authoritative as one that was checked.
	verified bool

	// detect reports whether this harness appears to be present.
	detect func() bool
}

func knownHosts() []host {
	home, _ := os.UserHomeDir()

	return []host{
		{
			name:        "claude-code",
			description: "Claude Code",
			verified:    true,
			globalDir:   func() string { return filepath.Join(home, ".claude", "skills") },
			projectDir: func(root string) string {
				return filepath.Join(root, ".claude", "skills")
			},
			detect: func() bool {
				_, err := os.Stat(filepath.Join(home, ".claude"))
				return err == nil
			},
		},
		{
			name: "antigravity",
			// Not verified against a real installation — Antigravity is not
			// present on the machine this was written on, and the paths come
			// from its documented layout rather than from having been seen to
			// work. Anyone using this should check the skills were actually
			// picked up, and `director skills --cat` prints the text to place
			// by hand if not.
			description: "Antigravity (paths unverified)",
			verified:    false,
			globalDir:   func() string { return filepath.Join(home, ".gemini", "antigravity-cli", "skills") },
			projectDir: func(root string) string {
				return filepath.Join(root, ".agents", "skills")
			},
			detect: func() bool {
				_, err := os.Stat(filepath.Join(home, ".gemini"))
				return err == nil
			},
		},
	}
}

func lookupHost(name string) (host, bool) {
	for _, candidate := range knownHosts() {
		if candidate.name == name {
			return candidate, true
		}
	}
	return host{}, false
}

func newInstallCmd() *cobra.Command {
	var scope string
	var hostNames []string
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install the director skills for a harness on this machine",
		Long: `Copies the shipped skills where a harness will find them, so an agent
can pick them up as /director.

Asks which harness and whether to install for this project or for every
project. Pass --host and --scope to skip the questions, which is what a setup
script wants.

Nothing is written into .director/ — the skills are the director's operating
instructions and ship with the binary. Edit them and they stop being what
director thinks it installed, so if you want to change them, fork the file and
point your harness at your copy instead.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			chosenScope, err := resolveScope(scope)
			if err != nil {
				return err
			}
			chosenHosts, err := resolveHosts(hostNames)
			if err != nil {
				return err
			}
			if len(chosenHosts) == 0 {
				fmt.Println("Nothing selected. The skills are still readable with:")
				fmt.Println("\n    director skills --cat director")
				return nil
			}

			projectRoot, err := os.Getwd()
			if err != nil {
				return err
			}

			for _, chosen := range chosenHosts {
				target, err := targetDir(chosen, chosenScope, projectRoot)
				if err != nil {
					return err
				}
				written, err := copySkills(target, dryRun)
				if err != nil {
					return err
				}
				fmt.Printf("\n%s (%s):\n", chosen.description, chosenScope)
				for _, path := range written {
					if dryRun {
						fmt.Printf("  would write %s\n", path)
					} else {
						fmt.Printf("  wrote %s\n", path)
					}
				}
				if !chosen.verified {
					fmt.Printf("  note: these paths are not verified against a real %s installation.\n", chosen.name)
					fmt.Printf("        Check the skills are picked up; if not, place them by hand with `director skills --cat`.\n")
				}
				warnOtherScope(chosen, chosenScope, projectRoot)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&scope, "scope", "", "project | global (default: ask)")
	cmd.Flags().StringSliceVar(&hostNames, "host", nil, "harness to install for, repeatable (default: ask)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print what would be written and change nothing")
	return cmd
}

// resolveScope takes the flag or asks.
func resolveScope(flag string) (Scope, error) {
	switch Scope(flag) {
	case ScopeProject, ScopeGlobal:
		return Scope(flag), nil
	case "":
	default:
		return "", fmt.Errorf("unknown scope %q (valid: project, global)", flag)
	}

	// Not a terminal falls back to huh's accessible line mode rather than
	// failing, so piped answers work. Project is listed first and is therefore
	// what an empty stdin selects: it writes inside the current directory and
	// is trivially undone, which is the right way for a guess to be wrong.
	chosen, err := newPrompter().selectOne(
		"Install the director skills where?",
		"Project puts them in this repository, so they travel with it. Global covers every project on this machine.",
		[]huh.Option[string]{
			huh.NewOption("This project only", string(ScopeProject)),
			huh.NewOption("Globally, for every project", string(ScopeGlobal)),
		})
	if err != nil {
		return "", err
	}
	if chosen == "" {
		return "", fmt.Errorf("no scope chosen")
	}
	return Scope(chosen), nil
}

// resolveHosts takes the flags or asks, defaulting the selection to whatever
// looks installed.
func resolveHosts(names []string) ([]host, error) {
	if len(names) > 0 {
		var chosen []host
		for _, name := range names {
			found, ok := lookupHost(name)
			if !ok {
				var known []string
				for _, candidate := range knownHosts() {
					known = append(known, candidate.name)
				}
				return nil, fmt.Errorf("unknown harness %q (known: %s)", name, strings.Join(known, ", "))
			}
			chosen = append(chosen, found)
		}
		return chosen, nil
	}

	var options []huh.Option[string]
	for _, candidate := range knownHosts() {
		label := candidate.description
		if candidate.detect() {
			label += "  (detected)"
		}
		option := huh.NewOption(label, candidate.name)
		// Pre-select what is actually here, so the common case is one keypress
		// and the uncommon one is still visible.
		options = append(options, option.Selected(candidate.detect()))
	}

	picked, err := newPrompter().selectMany(
		"Which harness should be able to direct?",
		"Skills are how an agent learns to act as a director. Pick every harness you direct from.",
		options)
	if err != nil {
		return nil, err
	}

	var chosen []host
	for _, name := range picked {
		if found, ok := lookupHost(name); ok {
			chosen = append(chosen, found)
		}
	}
	return chosen, nil
}

func targetDir(h host, scope Scope, projectRoot string) (string, error) {
	switch scope {
	case ScopeGlobal:
		if h.globalDir == nil {
			return "", fmt.Errorf("%s has no global skills location", h.name)
		}
		return h.globalDir(), nil
	case ScopeProject:
		if h.projectDir == nil {
			return "", fmt.Errorf("%s has no project-local skills location; install it globally instead", h.name)
		}
		return h.projectDir(projectRoot), nil
	}
	return "", fmt.Errorf("unknown scope %q", scope)
}

func copySkills(target string, dryRun bool) ([]string, error) {
	var written []string
	err := fs.WalkDir(skillSet, "skills", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		relative := strings.TrimPrefix(path, "skills/")
		destination := filepath.Join(target, relative)
		written = append(written, destination)
		if dryRun {
			return nil
		}
		body, err := skillSet.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		return os.WriteFile(destination, body, 0o644) // #nosec G306 -- instructions are meant to be readable
	})
	return written, err
}

// warnOtherScope reports a copy of the skills already installed at the scope
// that was not chosen.
//
// A harness that finds the same skill in both its user and its project
// configuration offers it twice, and the two entries are indistinguishable in
// the picker — so somebody ends up choosing between identical options with no
// way to tell which they are getting, and no idea why there are two. Installing
// is exactly when this becomes true, so it is exactly when to say so.
//
// It warns rather than removing: a copy at the other scope may be somebody
// else's, or deliberate, and deleting configuration the caller did not mention
// is worse than telling them it is there.
func warnOtherScope(h host, chosen Scope, projectRoot string) {
	other := ScopeGlobal
	if chosen == ScopeGlobal {
		other = ScopeProject
	}
	dir, err := targetDir(h, other, projectRoot)
	if err != nil {
		return
	}

	var duplicated []string
	names, err := skillNames()
	if err != nil {
		return
	}
	for _, name := range names {
		if _, err := os.Stat(filepath.Join(dir, name, "SKILL.md")); err == nil {
			duplicated = append(duplicated, name)
		}
	}
	if len(duplicated) == 0 {
		return
	}

	fmt.Printf("\n  warning: %s also has these skills installed at %s scope:\n", h.description, other)
	fmt.Printf("             %s\n", dir)
	fmt.Printf("           %s will show each of them twice, with nothing to tell the copies apart.\n", h.description)
	fmt.Printf("           Remove the ones you do not want:\n\n")
	for _, name := range duplicated {
		fmt.Printf("             rm -rf %s\n", filepath.Join(dir, name))
	}
}
