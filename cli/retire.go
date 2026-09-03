package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/rwmyers/agent-director/director"
	"github.com/spf13/cobra"
)

func newRetireCmd() *cobra.Command {
	var opt director.RetireOptions

	cmd := &cobra.Command{
		Use:     "retire [director...]",
		Aliases: []string{"rm", "delete"},
		Short:   "Remove a director and its record of what it was running",
		Long: `Removes a director's state file. With no arguments, offers a pick list.

That file is the only thing mapping an engagement back to its harness, so a
live agent whose director is gone keeps running, keeps costing money, and can
no longer be listed, read, answered or stopped by anything.

So this refuses while anything is still alive, and tells you which. Use --stop
to end them first, or --force to orphan them deliberately.

Retiring does not touch the workflow, the prompts, or any transcript. It
removes one director's memory of its own fleet.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			roots, err := resolveRoots()
			if err != nil {
				return err
			}

			ids := args
			if len(ids) == 0 {
				ids, err = pickDirectors(cmd, roots)
				if err != nil {
					return err
				}
				if len(ids) == 0 {
					return nil // backing out is a normal outcome, not a failure
				}
			}

			var results []*director.RetireResult
			var failures []error
			for _, id := range ids {
				result, err := director.RetireByID(cmd.Context(), roots, id, opt, director.SystemClock)
				if err != nil {
					// One director that cannot be retired must not stop the
					// others: a pick list of five stale records should not be
					// blocked by the one that still has an agent running.
					failures = append(failures, err)
					continue
				}
				results = append(results, result)
			}

			if opts.asJSON {
				if err := emit(results); err != nil {
					return err
				}
			} else {
				for _, result := range results {
					report(result)
				}
			}

			return combineFailures("retired", failures, len(ids))
		},
	}
	cmd.Flags().BoolVar(&opt.Stop, "stop", false, "end every running engagement first")
	cmd.Flags().BoolVar(&opt.Force, "force", false, "retire anyway, leaving running engagements unreachable")
	return cmd
}

func report(result *director.RetireResult) {
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
}

// pickDirectors offers the directors in this root and returns what was chosen.
//
// It surveys rather than just listing, so each row can say what removing it
// would actually cost — how much work it holds, how much of that is waiting,
// and whether another conversation is currently driving it. A pick list of bare
// identifiers would make the dangerous choice look exactly like the safe one.
func pickDirectors(cmd *cobra.Command, roots director.Roots) ([]string, error) {
	survey, err := director.TakeSurvey(cmd.Context(), roots, director.SystemClock)
	if err != nil {
		return nil, err
	}
	if len(survey.Directors) == 0 {
		fmt.Printf("no directors under %s\n", roots.Primary)
		return nil, nil
	}

	options := make([]huh.Option[string], 0, len(survey.Directors))
	for _, summary := range survey.Directors {
		options = append(options, huh.NewOption(describeForRemoval(summary), summary.ID))
	}

	return newPrompter().pick(pickList{
		title:       "Retire which directors?",
		description: "Removing a director does not stop its agents — it makes them unreachable. Anything still running is refused unless you pass --stop or --force.",
		verb:        "Retire",
		noun:        "director",
		options:     options,
	})
}

// describeForRemoval renders one director the way somebody choosing what to
// delete needs to see it.
func describeForRemoval(summary director.DirectorSummary) string {
	parts := []string{fmt.Sprintf("%s  %s", summary.ID, summary.Name)}
	switch {
	case summary.Engagements == 0:
		parts = append(parts, "idle")
	case summary.NeedsAttention > 0:
		parts = append(parts, fmt.Sprintf("%d engagements, %d waiting", summary.Engagements, summary.NeedsAttention))
	default:
		parts = append(parts, fmt.Sprintf("%d engagements", summary.Engagements))
	}
	if summary.Claimed {
		parts = append(parts, "in use by another conversation")
	}
	return strings.Join(parts, "  ·  ")
}
