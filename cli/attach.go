package cli

import (
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/rwmyers/agent-director/director"
	"github.com/spf13/cobra"
)

func newAttachCmd() *cobra.Command {
	var createNew bool
	var name, workflow string
	var surveyOnly bool

	cmd := &cobra.Command{
		Use:   "attach [director]",
		Short: "Become a director: take over an existing session, or start one",
		Long: `Run this once at the start of a directing session, before anything
else.

It inspects the directors in this project and works out whether you should take
over one that is already running or start your own:

  - Nothing here yet, or every director is already being driven by another
    conversation, and it creates one.
  - One director is unattended, and it attaches to that.
  - Something is waiting — a blocked engagement, a stalled one — and it
    attaches to whoever owns it, because unattended waiting work matters more
    than a clean slate.
  - Two unattended directors both have work, and it refuses to guess. Nothing
    distinguishes them, and picking wrong means quietly operating on somebody
    else's fleet with nothing in the output to show it.

Attaching records a claim, so a conversation arriving in the next %s can tell
somebody is already here. The claim is advisory — it expires, and nothing stops
two conversations sharing a director if they insist.

Pass a director id to attach to a specific one, or --new to force a fresh one.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			roots, err := resolveRoots()
			if err != nil {
				return err
			}

			survey, err := director.TakeSurvey(cmd.Context(), roots, director.SystemClock)
			if err != nil {
				return err
			}

			if surveyOnly {
				if opts.asJSON {
					return emit(survey)
				}
				printSurvey(survey)
				return nil
			}

			target := ""
			if len(args) == 1 {
				target = args[0]
			}

			// Only the caller's own explicit instruction overrides the survey.
			// When it says to ask, asking is the answer — resolving it here
			// would be exactly the guess it declined to make.
			if target == "" && !createNew {
				switch survey.Recommend {
				case director.RecommendAttach:
					target = survey.RecommendID
				case director.RecommendCreate:
					createNew = true
				case director.RecommendAsk:
					if opts.asJSON {
						return emit(survey)
					}
					printSurvey(survey)
					fmt.Printf("\nPick one:      director attach <id>\n")
					fmt.Printf("Or start new:  director attach --new --name <yours>\n")
					return fmt.Errorf("cannot choose a director for you: %s", survey.Reason)
				}
			}

			d, created, err := director.Attach(cmd.Context(), roots, target, createNew, name, workflow, director.SystemClock)
			if err != nil {
				return err
			}

			if opts.asJSON {
				return emit(map[string]any{
					"director": d.State.DirectorID,
					"name":     d.State.Name,
					"workflow": d.State.Workflow,
					"root":     roots.Primary,
					"created":  created,
					"reason":   survey.Reason,
					"host":     d.State.Host,
					"survey":   survey,
				})
			}

			verb := "attached to"
			if created {
				verb = "created"
			}
			fmt.Printf("%s director %s (%s), workflow %q\n", verb, d.State.DirectorID, d.State.Name, d.State.Workflow)
			fmt.Printf("because: %s\n\n", survey.Reason)
			fmt.Printf("Act as it by exporting this, or by passing --director on every command:\n\n")
			fmt.Printf("    export %s=%s\n\n", director.EnvID, d.State.DirectorID)

			// Said every attach, because it is detected every attach and can
			// change between two sessions in the same project. It is also the
			// only place a director is told how it gets its next turn, so the
			// skill defers to this line rather than forking on a harness name.
			fmt.Printf("host: %s\n", d.State.Host.Describe())
			fmt.Printf("next turn: %s\n\n", d.State.Host.NextTurn())

			if !created {
				engagements, err := d.Status(cmd.Context())
				if err == nil && len(engagements) > 0 {
					fmt.Printf("You have inherited %d engagement(s). Deal with anything waiting before starting new work:\n\n", len(engagements))
					printEngagements(engagements)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&createNew, "new", false, "start a new director rather than attaching")
	cmd.Flags().StringVar(&name, "name", "", "human label, when creating")
	cmd.Flags().StringVar(&workflow, "workflow", "", "workflow to bind to, when creating")
	cmd.Flags().BoolVar(&surveyOnly, "survey", false, "report what it would do and change nothing")

	cmd.Long = fmt.Sprintf(cmd.Long, director.AttachGrace)
	return cmd
}

func printSurvey(survey *director.Survey) {
	fmt.Printf("root: %s\n\n", survey.Root)
	if len(survey.Directors) == 0 {
		fmt.Println("no directors here yet")
	} else {
		out := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		_ = writeRow(out, "ID\tNAME\tWORKFLOW\tENGAGEMENTS\tWAITING\tSTATUS\n")
		for _, summary := range survey.Directors {
			claim := "unattended"
			if summary.Claimed {
				claim = "claimed " + short(time.Since(summary.AttachedAt)) + " ago"
			}
			_ = writeRow(out, "%s\t%s\t%s\t%d\t%d\t%s\n",
				summary.ID, summary.Name, summary.Workflow,
				summary.Engagements, summary.NeedsAttention, claim)
		}
		_ = out.Flush()
	}
	fmt.Printf("\nrecommendation: %s — %s\n", survey.Recommend, survey.Reason)
}
