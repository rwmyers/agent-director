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

// skillInstall is what the skills half did, or would do, for one harness.
//
// It is the record the JSON report is built from as well as the prose, so the
// two cannot disagree about which files went where.
type skillInstall struct {
	host  host
	Host  string   `json:"host"`
	Scope Scope    `json:"scope"`
	Wrote []string `json:"wrote"`
	// Verified is the harness's own claim that these paths were confirmed
	// against a real installation, repeated here because it is what decides
	// whether the person needs to go and check.
	Verified bool `json:"verified"`
}

// installSkills copies the shipped skills into each chosen harness's directory
// at the chosen scope — or, under dryRun, works out where they would go.
func installSkills(chosen []host, scope Scope, projectRoot string, dryRun bool) ([]skillInstall, error) {
	installed := make([]skillInstall, 0, len(chosen))
	for _, h := range chosen {
		target, err := targetDir(h, scope, projectRoot)
		if err != nil {
			return nil, err
		}
		written, err := copySkills(target, dryRun)
		if err != nil {
			return nil, err
		}
		installed = append(installed, skillInstall{
			host: h, Host: h.name, Scope: scope, Wrote: written, Verified: h.locations.Verified,
		})
	}
	return installed, nil
}

// reportSkills is the prose account of the skills half.
//
// An empty selection is said outright rather than skipped over. Choosing no
// harness is legitimate — the skills can be placed by hand — but a run that
// goes straight on to the workflow without a word leaves the person unsure
// whether the first question was heard.
func reportSkills(installed []skillInstall, projectRoot string, dryRun bool) {
	if len(installed) == 0 {
		fmt.Println("No harness selected, so no skills were installed. They are still readable with:")
		fmt.Println("\n    director skills --cat director")
		return
	}
	for _, done := range installed {
		fmt.Printf("\n%s (%s):\n", done.host.locations.Description, done.Scope)
		for _, path := range done.Wrote {
			if dryRun {
				fmt.Printf("  would write %s\n", path)
			} else {
				fmt.Printf("  wrote %s\n", path)
			}
		}
		if !done.Verified {
			fmt.Printf("  note: these paths are not verified against a real %s installation.\n", done.Host)
			fmt.Printf("        Check the skills are picked up; if not, place them by hand with `director skills --cat`.\n")
		}
		warnOtherScope(done.host, done.Scope, projectRoot)
	}
}

// resolveScope takes the flag or asks.
//
// It asks only on a terminal, and only when nobody is parsing stdout. There
// used to be a fallback to huh's line mode for a pipe, under which an empty
// stdin selected project scope; that is a guess made on somebody's behalf, and
// a script that wanted project scope can say so.
func resolveScope(flag string, ask prompter) (Scope, error) {
	switch Scope(flag) {
	case ScopeProject, ScopeGlobal:
		return Scope(flag), nil
	case "":
	default:
		return "", fmt.Errorf("unknown scope %q (valid: project, global)", flag)
	}
	if opts.asJSON {
		return "", errors.New("no scope chosen, and --json has nobody to ask: pass --scope project|global")
	}
	if !ask.terminal {
		return "", errors.New("no scope chosen and no terminal to ask on: pass --scope project|global")
	}

	chosen, err := ask.selectOne(
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
func resolveHosts(names []string, ask prompter) ([]host, error) {
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
	if opts.asJSON {
		return nil, errors.New("no harness chosen, and --json has nobody to ask: pass --host (`director skills --path` lists the targets)")
	}
	if !ask.terminal {
		return nil, errors.New("no harness chosen and no terminal to ask on: pass --host (`director skills --path` lists the targets)")
	}

	picked, err := ask.selectMany(
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
