package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/rwmyers/agent-director/director"
	"github.com/spf13/cobra"
)

// removePrompter is the prompter the pick list asks with. Only a test replaces
// it, so that the asking path can be driven without a terminal.
var removePrompter = newPrompter

func newRemoveCmd() *cobra.Command {
	var opt director.RemoveOptions

	cmd := &cobra.Command{
		Use:     "remove [engagement...]",
		Aliases: []string{"forget"},
		Short:   "Drop engagements from this director's record",
		Long: `Removes engagements from the state file, so they stop appearing in
"director status". Use it for a failed spawn or a finished piece of work whose
row is now only noise. With no arguments, offers a pick list.

Each engagement can be named by its full identifier, by any fragment of it, or
by a fragment of its title. A fragment matching more than one is refused rather
than guessed at. Name several and all of them go in one invocation.

That record is the only thing mapping an engagement back to its harness, so a
live agent that has been forgotten keeps running, keeps costing money, and can
no longer be listed, read, answered or stopped. This therefore refuses while it
is still alive. Use --stop to end it first, or --force to orphan it
deliberately. The refusal is per engagement: naming a batch is not a way past
it.

One engagement that cannot be removed does not hold up the rest. Whatever could
be removed is, whatever could not is reported, and the command exits non-zero.
Nothing is lost by that — the ones it refused are exactly the ones still in the
record, so the fixed command can simply be run again.

Removing forgets the work; it does not undo it. Transcripts, logs, the
workflow, and whatever the agent actually produced — branches, worktrees,
files — are all untouched. It is not reversible.

The one thing it does reclaim is the slot the conversation was given in its
harness — a herdr pane — because director asked for that slot and nothing else
ever gives it back. It is closed only for an engagement the harness confirms
has finished, or one --stop has just successfully ended, and never for --force
or for the conversation this director is itself running in. Harnesses with no
such slot are unaffected. A slot that will not close does not fail the removal:
the row has already gone, so it is reported and left for you to close by hand.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := open()
			if err != nil {
				return err
			}

			fragments := args
			if len(fragments) == 0 {
				fragments, err = pickEngagements(cmd, d)
				if err != nil {
					return err
				}
				if len(fragments) == 0 {
					return nil // backing out is a normal outcome, not a failure
				}
			}
			fragments = deduplicate(fragments)

			results := []*director.RemoveResult{}
			var failures []error
			for _, fragment := range fragments {
				result, err := d.Remove(cmd.Context(), fragment, opt)
				if err != nil {
					// Best-effort, as retire is: one engagement that is still
					// alive, or one identifier that was typed wrong, must not
					// hold back a list of finished rows somebody asked to clear.
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
					reportRemoved(result)
				}
			}

			return combineFailures("removed", failures, len(fragments))
		},
	}
	cmd.Flags().BoolVar(&opt.Stop, "stop", false, "end the engagement first")
	cmd.Flags().BoolVar(&opt.Force, "force", false, "remove anyway, leaving a running agent unreachable")
	return cmd
}

// deduplicate drops repeats while keeping the order they were given in.
//
// The same fragment named twice is one removal, not a removal and a
// "nothing matches" for a row that has just gone.
func deduplicate(fragments []string) []string {
	seen := make(map[string]bool, len(fragments))
	out := make([]string, 0, len(fragments))
	for _, fragment := range fragments {
		if seen[fragment] {
			continue
		}
		seen[fragment] = true
		out = append(out, fragment)
	}
	return out
}

// pickEngagements offers this director's fleet and returns what was chosen.
//
// It asks the same way retire does, through the same picker, because the two
// commands delete the same kind of thing at different granularities and a
// second, differently-behaved pick list in one binary would be a trap.
//
// Without a terminal it refuses rather than asking. huh's accessible mode would
// otherwise read stdin, and a removal that hangs waiting for an answer nobody
// is there to give is worse from a script than an error is.
func pickEngagements(cmd *cobra.Command, d *director.Director) ([]string, error) {
	p := removePrompter()
	if !p.terminal {
		return nil, errors.New(
			"name the engagements to remove: director remove <engagement>...\n\n" +
				"With no arguments this offers a pick list, which needs a terminal. From a script,\n" +
				"name each one — `director status --json` lists what this director holds")
	}

	engagements, err := d.Status(cmd.Context())
	if err != nil {
		return nil, err
	}
	if len(engagements) == 0 {
		fmt.Println("no engagements")
		return nil, nil
	}

	options := make([]huh.Option[string], 0, len(engagements))
	for _, engagement := range engagements {
		options = append(options, huh.NewOption(describeEngagementForRemoval(engagement), engagement.ID))
	}

	return p.pick(pickList{
		title:       "Remove which engagements?",
		description: "Removing an engagement does not stop its agent — it makes it unreachable. Anything still running is refused unless you pass --stop or --force.",
		verb:        "Remove",
		noun:        "engagement",
		// Somebody who got here ran remove and then picked these rows out by
		// hand. Opening the confirmation on Cancel makes the default answer
		// throw that away.
		defaultConfirm: true,
		options:        options,
	})
}

// describeEngagementForRemoval renders one engagement the way somebody choosing
// what to delete needs to see it: enough to tell two rows apart, and enough to
// see which of them is still live.
func describeEngagementForRemoval(engagement *director.Engagement) string {
	progress := engagement.Progress
	if progress == "" {
		progress = "no progress"
	}
	parts := []string{
		fmt.Sprintf("%s  %s", shortID(engagement.ID), engagement.Title),
		fmt.Sprintf("%s, %s", engagement.Health, progress),
	}
	if engagement.Lifecycle.Live() {
		parts = append(parts, "still running")
	}
	return strings.Join(parts, "  ·  ")
}

// shortID is as much of an identifier as a person needs to recognise a row.
// The whole one is carried as the option's value, so nothing is resolved from
// the shortened form.
func shortID(id string) string {
	const shown = 12
	if len(id) <= shown {
		return id
	}
	return id[:shown]
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
	reportDisposal(result)
	for _, ask := range result.Asks {
		fmt.Printf("  dropped question %s\n", ask)
	}
	if result.Orphaned {
		fmt.Printf("\n  It was still running and nothing can reach it now. The process is still there;\n")
		fmt.Printf("  find it through %s itself.\n", result.Harness)
	}
}

// reportDisposal says what became of the slot the conversation was occupying.
//
// Nothing is printed when there was no slot, which is what a harness that does
// not declare the capability reports — removal there reads exactly as it did
// before any of this existed.
//
// A failure is printed rather than returned, because the removal succeeded: the
// row is gone and running the command again would only report that nothing
// matches. What is left is a slot to close by hand, so the line says so.
func reportDisposal(result *director.RemoveResult) {
	switch result.Disposal {
	case director.DisposalClosed:
		fmt.Printf("  closed its %s slot\n", result.Harness)
	case director.DisposalKept:
		fmt.Printf("  left its %s slot alone: %s\n", result.Harness, result.DisposalReason)
	case director.DisposalFailed:
		fmt.Printf("  its %s slot could not be closed: %s\n", result.Harness, result.DisposalReason)
		fmt.Printf("  The row is gone regardless, so there is nothing to run again. Close it in %s directly.\n", result.Harness)
	}
}
