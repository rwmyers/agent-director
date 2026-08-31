package cli

import (
	"fmt"
	"os"

	"github.com/rwmyers/agent-director/director"
	"github.com/spf13/cobra"
)

// reportUnreadable says, on stderr, that some director records could not be
// read — without failing the command that listed the ones that could.
func reportUnreadable(err error) {
	if err == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "\nunreadable director records:\n  %v\n\n"+
		"Run `director repair` to see what is wrong, then `director repair --write` to fix it.\n", err)
}

func newRepairCmd() *cobra.Command {
	var write bool

	cmd := &cobra.Command{
		Use:   "repair",
		Short: "Make an unreadable director record readable again",
		Long: `Finds values that were written across several lines and joins them back
together, so a director whose state file cannot be parsed can be used again
without anybody opening it in a text editor.

Nothing is discarded and nothing is guessed. A line that cannot be understood
as the continuation of an earlier value is reported and left alone. A record
that already reads is never rewritten.

By default this only says what it would do. Pass --write to do it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			roots, err := resolveRoots()
			if err != nil {
				return err
			}
			reports, problems := director.RepairStates(roots.Primary, write)
			if opts.asJSON {
				if err := emit(reports); err != nil {
					return err
				}
				return problems
			}

			repaired := 0
			for _, report := range reports {
				if !report.Repaired() {
					continue
				}
				repaired++
				verb := "would join"
				if report.Applied {
					verb = "joined"
				}
				fmt.Printf("%s\n", report.Path)
				for _, fix := range report.Fixes {
					where := fix.Key
					if fix.Section != "" {
						where = fix.Section + "." + fix.Key
					}
					fmt.Printf("  %s %d line(s) back into %q (starts on line %d)\n",
						verb, fix.Folded, where, fix.Line)
				}
			}

			switch {
			case repaired == 0 && problems == nil:
				fmt.Printf("every director record under %s reads correctly\n", roots.Primary)
			case repaired > 0 && !write:
				fmt.Printf("\nnothing has been changed. Run `director repair --write` to apply this.\n")
			}
			// A record that could not even be repaired is the one thing here
			// worth a non-zero exit: it is the case that still needs a person.
			return problems
		},
	}
	cmd.Flags().BoolVar(&write, "write", false, "apply the repair instead of only describing it")
	return cmd
}
