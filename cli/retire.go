package cli

import (
	"fmt"
	"os"

	"github.com/rwmyers/agent-director/director"
	"github.com/spf13/cobra"
)

func newRetireCmd() *cobra.Command {
	var opt director.RetireOptions

	cmd := &cobra.Command{
		Use:     "retire <director>",
		Aliases: []string{"rm", "delete"},
		Short:   "Remove a director and its record of what it was running",
		Long: `Removes a director's state file.

That file is the only thing mapping an engagement back to its harness, so a
live agent whose director is gone keeps running, keeps costing money, and can
no longer be listed, read, answered or stopped by anything.

So this refuses while anything is still alive, and tells you which. Use --stop
to end them first, or --force to orphan them deliberately.

Retiring does not touch the workflow, the prompts, or any transcript. It
removes one director's memory of its own fleet.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			roots, err := resolveRoots()
			if err != nil {
				return err
			}
			result, err := director.RetireByID(cmd.Context(), roots, args[0], opt, director.SystemClock)
			if err != nil {
				return err
			}

			if opts.asJSON {
				return emit(result)
			}
			fmt.Printf("retired %s (%s)\n", result.DirectorID, result.Name)
			for _, id := range result.Stopped {
				fmt.Printf("  stopped %s\n", id)
			}
			if len(result.Orphaned) > 0 {
				fmt.Printf("\n  %d engagement(s) left running with nothing able to reach them:\n", len(result.Orphaned))
				for _, id := range result.Orphaned {
					fmt.Printf("    %s\n", id)
				}
				fmt.Printf("  Their processes are still there; find them through the harness itself.\n")
			}
			if os.Getenv(director.EnvID) == result.DirectorID {
				fmt.Printf("\n  %s still names the director you just retired. Run `unset %s`.\n",
					director.EnvID, director.EnvID)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&opt.Stop, "stop", false, "end every running engagement first")
	cmd.Flags().BoolVar(&opt.Force, "force", false, "retire anyway, leaving running engagements unreachable")
	return cmd
}
