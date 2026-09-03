package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/rwmyers/agent-director/harness"
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

// host is a harness that has told the installer where its skills go.
//
// Nothing here knows a harness by name. Every target comes from the registry —
// built-in adapters, install-only declarations, and director-harness-*
// executables alike — so a harness director has never heard of becomes
// installable the moment its declaration is on the machine, with no change to
// this file. A harness with no answer is simply absent from the list;
// `director skills --cat` still prints the same text for anyone to place by
// hand.
type host struct {
	name      string
	locations harness.SkillLocations
}

// installTargets asks every harness that might have an answer where its skills
// belong.
//
// This runs describe on each plugin, which is why it is called once per command
// and never from inside a loop: enumerating is the only part of director that
// pays that cost, and it pays it once.
//
// Three things are skipped, all quietly except the one that indicates a fault:
// a harness that answers ErrNoSkillLocations, one whose answer failed, and one
// that names no directory for either scope — that last would otherwise be
// offered and then fail at the point of writing.
func installTargets() []host {
	var hosts []host
	for _, candidate := range harness.SkillInstallers() {
		locations, err := candidate.Installer.SkillLocations()
		if err != nil {
			// A harness that declines has said all it means to. One that broke
			// says so, because a plugin the user installed and then cannot find
			// in the list is otherwise an unexplainable absence.
			if !errors.Is(err, harness.ErrNoSkillLocations) {
				fmt.Fprintf(os.Stderr, "director: %s: %v\n", candidate.Name, err)
			}
			continue
		}
		if locations.GlobalDir == "" && locations.ProjectDir == "" {
			continue
		}
		if locations.Description == "" {
			locations.Description = candidate.Name
		}
		hosts = append(hosts, host{name: candidate.Name, locations: locations})
	}
	return hosts
}

func lookupHost(hosts []host, name string) (host, bool) {
	for _, candidate := range hosts {
		if candidate.name == name {
			return candidate, true
		}
	}
	return host{}, false
}

// notATarget explains a --host that installing has no answer for.
//
// Two sets go by the word "harness" and they are not the same set. `director
// harnesses` lists what director can drive, which needs a working protocol;
// this lists what has somewhere to put skills, which needs a pair of
// directories. herdr is where the difference shows: director drives it, and it
// reads no skills at all, because a pane displays somebody else's agent and
// that agent loads skills from its own harness. Answering "unknown harness"
// there is simply false, and it sends the reader looking for a broken registry
// instead of at the claude-code entry that is already doing the job.
//
// So the refusal answers whichever question the name belongs to: a harness that
// can be driven is named as one and told where its skills actually come from; a
// name in neither set is the only one that gets "unknown".
func notATarget(name string, available []host) error {
	var known []string
	for _, candidate := range available {
		known = append(known, candidate.name)
	}
	targets := strings.Join(known, ", ")

	adapter, err := harness.Lookup(name)
	if err != nil {
		return fmt.Errorf("unknown harness %q (install targets: %s)\n"+
			"       `director harnesses` lists the separate set director can drive", name, targets)
	}

	because := "it has no skills directory of its own"
	if harness.Displays(adapter) {
		because = "it displays another harness's conversation, and the agent it displays reads skills from its own harness"
	}
	return fmt.Errorf("%s is a harness director can drive, not one skills are installed into: %s\n"+
		"       Install for the agent you direct from inside it instead (install targets: %s)", name, because, targets)
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

The harnesses offered here are the ones with somewhere to put a skill, which is
not the set director can drive — that is what ` + "`director harnesses`" + ` lists. A harness
that displays another harness's conversation reads no skills of its own, so
install for the agent you run inside it instead.

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
				fmt.Printf("\n%s (%s):\n", chosen.locations.Description, chosenScope)
				for _, path := range written {
					if dryRun {
						fmt.Printf("  would write %s\n", path)
					} else {
						fmt.Printf("  wrote %s\n", path)
					}
				}
				if !chosen.locations.Verified {
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
	available := installTargets()

	if len(names) > 0 {
		var chosen []host
		for _, name := range names {
			found, ok := lookupHost(available, name)
			if !ok {
				return nil, notATarget(name, available)
			}
			chosen = append(chosen, found)
		}
		return chosen, nil
	}

	picked, err := newPrompter().selectMany(
		"Which harness should be able to direct?",
		"Skills are how an agent learns to act as a director. Pick every harness you direct from.",
		hostOptions(available))
	if err != nil {
		return nil, err
	}

	var chosen []host
	for _, name := range picked {
		if found, ok := lookupHost(available, name); ok {
			chosen = append(chosen, found)
		}
	}
	return chosen, nil
}

// hostOptions renders every install target as a pickable option.
//
// Every target and nothing else: the list offered must be the list --host
// accepts, or somebody who cannot find their harness in the picker concludes
// director cannot install for it, when naming it on the command line would have
// worked all along.
func hostOptions(available []host) []huh.Option[string] {
	options := make([]huh.Option[string], 0, len(available))
	for _, candidate := range available {
		label := candidate.locations.Description
		if candidate.locations.Present {
			label += "  (detected)"
		}
		option := huh.NewOption(label, candidate.name)
		// Pre-select what is actually here, so the common case is one keypress
		// and the uncommon one is still visible.
		options = append(options, option.Selected(candidate.locations.Present))
	}
	return options
}

func targetDir(h host, scope Scope, projectRoot string) (string, error) {
	switch scope {
	case ScopeGlobal:
		if h.locations.GlobalDir == "" {
			return "", fmt.Errorf("%s has no global skills location", h.name)
		}
		return h.locations.GlobalDir, nil
	case ScopeProject:
		if h.locations.ProjectDir == "" {
			return "", fmt.Errorf("%s has no project-local skills location; install it globally instead", h.name)
		}
		// Joined here rather than by the harness: the project root is the
		// installer's to choose, and a harness that answered with an absolute
		// path would be deciding where somebody else's repository lives.
		return filepath.Join(projectRoot, h.locations.ProjectDir), nil
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

	fmt.Printf("\n  warning: %s also has these skills installed at %s scope:\n", h.locations.Description, other)
	fmt.Printf("             %s\n", dir)
	fmt.Printf("           %s will show each of them twice, with nothing to tell the copies apart.\n", h.locations.Description)
	fmt.Printf("           Remove the ones you do not want:\n\n")
	for _, name := range duplicated {
		fmt.Printf("             rm -rf %s\n", filepath.Join(dir, name))
	}
}
