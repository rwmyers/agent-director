package cli

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// skillSet is the shipped chief-of-staff context.
//
// Embedded so `director skills --cat` works from a single binary with nothing
// installed: an agent with only a shell can read the same instructions a
// skill-aware host would load for it, which is what keeps the tool usable from
// harnesses that have no skill mechanism at all.
//
//go:embed all:skills
var skillSet embed.FS

// skillNames lists the shipped skills, nearest-to-the-work first.
func skillNames() ([]string, error) {
	entries, err := fs.ReadDir(skillSet, "skills")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

func newSkillsCmd() *cobra.Command {
	var cat string
	var listPath bool

	cmd := &cobra.Command{
		Use:   "skills",
		Short: "Show the director instructions, or where they are installed",
		Long: `The shipped skills tell an agent how to act as a director.

A skill-aware harness loads them from disk after "director install". An agent
with only a shell can read the same text here:

    director skills --cat agent-director`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if cat != "" {
				body, err := skillSet.ReadFile(filepath.Join("skills", cat, "SKILL.md"))
				if err != nil {
					names, _ := skillNames()
					return fmt.Errorf("no skill %q (available: %s)", cat, strings.Join(names, ", "))
				}
				fmt.Print(string(body))
				return nil
			}

			names, err := skillNames()
			if err != nil {
				return err
			}
			if listPath {
				roots, err := resolveRoots()
				if err != nil {
					return err
				}
				fmt.Println(filepath.Join(roots.Primary, "skills"))
				return nil
			}
			if opts.asJSON {
				return emit(names)
			}
			for _, name := range names {
				fmt.Println(name)
			}
			fmt.Printf("\nRead one with: director skills --cat %s\n", names[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&cat, "cat", "", "print one skill's text")
	cmd.Flags().BoolVar(&listPath, "path", false, "print where skills are installed under this root")
	return cmd
}

// skillHost is somewhere the skills can be installed to.
type skillHost struct {
	name   string
	dir    string
	detect func() bool
}

// knownHosts is where skills go for hosts we know the convention for.
//
// This is the one place a harness name legitimately appears on the director
// side: installing is inherently host-specific. Everything else — the skills,
// the commands, the output — stays neutral, and an unknown host is given the
// path rather than an error, so a harness nobody has heard of is still usable.
func knownHosts() []skillHost {
	home, _ := os.UserHomeDir()
	return []skillHost{
		{
			name: "claude-code",
			dir:  filepath.Join(home, ".claude", "skills"),
			detect: func() bool {
				_, err := os.Stat(filepath.Join(home, ".claude"))
				return err == nil
			},
		},
	}
}

func newInstallCmd() *cobra.Command {
	var host string
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install the director skills for a harness on this machine",
		Long: `Copies the shipped skills where a harness will find them, and copies
them into this configuration root so they can be edited.

Prints exactly what it would write with --dry-run. An unrecognised host is not
an error: the path is printed so the skills can be placed by hand.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			roots, err := resolveRoots()
			if err != nil {
				return err
			}

			targets := []string{filepath.Join(roots.Primary, "skills")}
			for _, candidate := range knownHosts() {
				if host != "" && host != candidate.name {
					continue
				}
				if host == "" && !candidate.detect() {
					continue
				}
				targets = append(targets, candidate.dir)
			}

			if len(targets) == 1 && host == "" {
				fmt.Println("No known harness detected on this machine.")
				fmt.Println("The skills are still readable with: director skills --cat agent-director")
				fmt.Println()
			}

			for _, target := range targets {
				written, err := copySkills(target, dryRun)
				if err != nil {
					return err
				}
				for _, path := range written {
					if dryRun {
						fmt.Printf("would write %s\n", path)
					} else {
						fmt.Printf("wrote %s\n", path)
					}
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&host, "host", "", "install for a specific harness rather than autodetecting")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print what would be written and change nothing")
	return cmd
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
