package cli

import (
	"embed"
	"fmt"
	"io/fs"
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

A skill-aware harness loads them from disk after "director setup". An agent
with only a shell can read the same text here:

    director skills --cat director`,
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
				// Where they would go, per harness, rather than a single
				// answer: skills live wherever each harness looks, and there
				// is no one director-owned directory that anything reads.
				for _, candidate := range installTargets() {
					for _, scope := range []Scope{ScopeGlobal, ScopeProject} {
						// A harness with no such scope is left out rather than
						// printed with a blank path.
						dir, err := targetDir(candidate, scope, "<project>")
						if err != nil {
							continue
						}
						fmt.Printf("%-14s %-7s %s\n", candidate.name, scope, dir)
					}
				}
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
	cmd.Flags().BoolVar(&listPath, "path", false, "print where each harness looks for skills")
	return cmd
}
