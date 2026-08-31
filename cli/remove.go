package cli

import (
	"fmt"

	"github.com/rwmyers/agent-director/director"
	"github.com/spf13/cobra"
)

func newRemoveCmd() *cobra.Command {
	var opt director.RemoveOptions

	cmd := &cobra.Command{
		Use:     "remove <engagement>",
		Aliases: []string{"forget"},
		Short:   "Drop one engagement from this director's record",
		Long: `Removes a single engagement from the state file, so it stops appearing in
"director status". Use it for a failed spawn or a finished piece of work whose
row is now only noise.

The engagement can be named by its full identifier, by any fragment of it, or
by a fragment of its title. A fragment matching more than one is refused rather
than guessed at.

That record is the only thing mapping the engagement back to its harness, so a
live agent that has been forgotten keeps running, keeps costing money, and can
no longer be listed, read, answered or stopped. This therefore refuses while it
is still alive. Use --stop to end it first, or --force to orphan it
deliberately.

Removing forgets the work; it does not undo it. Transcripts, logs, the
workflow, and whatever the agent actually produced — branches, worktrees,
files — are all untouched. It is not reversible.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := open()
			if err != nil {
				return err
			}
			result, err := d.Remove(cmd.Context(), args[0], opt)
			if err != nil {
				return err
			}
			if opts.asJSON {
				return emit(result)
			}
			reportRemoved(result)
			return nil
		},
	}
	cmd.Flags().BoolVar(&opt.Stop, "stop", false, "end the engagement first")
	cmd.Flags().BoolVar(&opt.Force, "force", false, "remove anyway, leaving a running agent unreachable")
	return cmd
}

func reportRemoved(result *director.RemoveResult) {
	progress := result.Progress
	if progress == "" {
		progress = "-"
	}
	fmt.Printf("removed %s  %s  (%s, %s)\n", result.EngagementID, result.Title, result.Health, progress)
	if result.Stopped {
		fmt.Printf("  stopped it first\n")
	}
	for _, ask := range result.Asks {
		fmt.Printf("  dropped question %s\n", ask)
	}
	if result.Orphaned {
		fmt.Printf("\n  It was still running and nothing can reach it now. The process is still there;\n")
		fmt.Printf("  find it through %s itself.\n", result.Harness)
	}
}
