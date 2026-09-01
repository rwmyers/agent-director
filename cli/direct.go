package cli

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/rwmyers/agent-director/director"
	"github.com/rwmyers/agent-director/harness"
	"github.com/spf13/cobra"
)

func newSpawnCmd() *cobra.Command {
	var task, title, name, harnessName, file string

	cmd := &cobra.Command{
		Use:   "spawn [brief]",
		Short: "Delegate a self-contained piece of work to a fresh agent",
		Long: `Starts an independent agent conversation and returns immediately — the
agent has not done anything yet.

The brief is the entire specification the agent receives. It shares none of
your context, none of the original wording of whatever prompted this, and
nothing any other engagement has found. Write it for a competent stranger:
the goal, what "done" looks like, what not to touch, and what to report back.

The agent starts in the directory this command is run from, and the harness
sandboxes it there — so run the spawn from a directory that contains where the
work will land, and let the brief tell the agent to create the rest. Every
spawn prints where it put the agent.`,
		Args: cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Before open(), so an unreadable brief costs nothing: a spawn is
			// the most expensive thing here to half-do.
			brief, err := textArg(args, 0, file)
			if err != nil {
				return err
			}
			d, err := open()
			if err != nil {
				return err
			}
			engagement, err := d.Spawn(cmd.Context(), director.SpawnOptions{
				Task:    task,
				Title:   title,
				Name:    name,
				Brief:   brief,
				Harness: harnessName,
			})
			if err != nil {
				return err
			}
			if opts.asJSON {
				return emit(engagement)
			}
			fmt.Printf("%s  %s  [%s on %s]\n", engagement.ID, engagement.Title, engagement.Task, engagement.Harness)
			// Where the agent starts, always. Nothing in the command names it
			// — it is wherever this process is running — so a working directory
			// that drifted a level down would otherwise move every agent
			// silently. The harness sandboxes the agent to this directory,
			// which makes it the one fact worth reading back before the spawn is
			// ten minutes old.
			fmt.Printf("  starting in %s\n", engagement.Dir)
			// Placement decided by where this director is running rather than by
			// the configuration. Said out loud, because otherwise the only way to
			// find out why an engagement landed somewhere unexpected is to guess.
			if note := engagement.Detail["placement"]; note != "" {
				fmt.Printf("  %s\n", note)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&task, "task", "", "task type from this director's workflow (required)")
	cmd.Flags().StringVar(&title, "title", "", "short label shown by director (default: first line of the brief)")
	cmd.Flags().StringVar(&name, "name", "", "how the harness should label this conversation in its own UI (default: the title)")
	cmd.Flags().StringVar(&harnessName, "harness", "", "override the workflow's placement")
	cmd.Flags().StringVar(&file, "file", "", "read the brief from a file, or from standard input with -")
	_ = cmd.MarkFlagRequired("task")
	return cmd
}

func newStatusCmd() *cobra.Command {
	var unhealthy bool

	cmd := &cobra.Command{
		Use:   "status [engagement]",
		Short: "Check on engagements without reading what they said",
		Long: `Cheap: it reads no transcripts. Run it freely, and always before
deciding what to do next.

HEALTH is the column to act on:

  ok         meeting its reporting contract; leave it alone
  quiet      overdue a report, but the harness shows it working; leave it alone
  stalled    overdue and no sign of activity; run "director nudge"
  blocked    it asked a question; run "director answer"
  abandoned  the process ended without finishing; find out what happened
  complete   the process ended having reached its terminal progress value
  unknown    not enough signal to judge; check again next turn

LIFECYCLE is about the process, not the work. "done" means no process is
attached and the conversation can be resumed — it does NOT mean the work
finished. An agent whose terminal was closed is done. Only progress reaching
its terminal value means finished, which is what "complete" reports.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := open()
			if err != nil {
				return err
			}

			var engagements []*director.Engagement
			if len(args) == 1 {
				engagement, err := d.Get(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				engagements = []*director.Engagement{engagement}
			} else {
				engagements, err = d.Status(cmd.Context())
				if err != nil {
					return err
				}
			}

			if unhealthy {
				var filtered []*director.Engagement
				for _, engagement := range engagements {
					if engagement.Health.NeedsDirector() {
						filtered = append(filtered, engagement)
					}
				}
				engagements = filtered
			}

			if opts.asJSON {
				return emit(engagements)
			}
			if len(engagements) == 0 {
				fmt.Println("no engagements")
				return nil
			}

			printEngagements(engagements)
			return nil
		},
	}
	cmd.Flags().BoolVar(&unhealthy, "unhealthy", false, "show only engagements needing action")
	return cmd
}

// printEngagements renders the fleet. Shared with attach, which shows the same
// table when a conversation inherits work — the same facts should not look
// different depending on which command surfaced them.
func printEngagements(engagements []*director.Engagement) {
	now := time.Now()
	out := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_ = writeRow(out, "ID\tHEALTH\tLIFECYCLE\tPROGRESS\tSILENT\tTITLE\n")
	for _, engagement := range engagements {
		progress := engagement.Progress
		if progress == "" {
			progress = "-"
		}
		_ = writeRow(out, "%s\t%s\t%s\t%s\t%s\t%s\n",
			engagement.ID, engagement.Health, engagement.Lifecycle,
			progress, short(engagement.SilentFor(now)), engagement.Title)
	}
	_ = out.Flush()

	for _, engagement := range engagements {
		if engagement.PendingAsk != nil {
			fmt.Printf("\n%s asked: %s\n  answer with: director answer %s \"...\"\n",
				engagement.ID, engagement.PendingAsk.Question, engagement.PendingAsk.ID)
		}
	}
}

// short renders a duration the way a status line wants it.
func short(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

func newSendCmd() *cobra.Command {
	var file string

	cmd := &cobra.Command{
		Use:   "send <engagement> [text]",
		Short: "Give a running engagement more instruction",
		Long: `Delivers text into an agent's conversation. It does not wait for a
reply — use "director status" and "director read" to see what came of it.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			text, err := textArg(args, 1, file)
			if err != nil {
				return err
			}
			d, err := open()
			if err != nil {
				return err
			}
			return d.Send(cmd.Context(), args[0], text)
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "read the text from a file, or from standard input with -")
	return cmd
}

func newNudgeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "nudge <engagement>",
		Short: "Ask a stalled engagement whether it is still working",
		Long: `What a director does about "stalled".

Sends a standard message asking the agent to report its progress. This exists
so that a human never has to poke an agent by hand — noticing that an agent has
gone quiet, and doing something about it, is the director's job.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := open()
			if err != nil {
				return err
			}
			return d.Nudge(cmd.Context(), args[0])
		},
	}
}

func newAnswerCmd() *cobra.Command {
	var file string

	cmd := &cobra.Command{
		Use:   "answer <ask> [text]",
		Short: "Answer a question an agent is blocked on",
		Long: `Unblocks an agent that called "director ask --wait". Until this is run
the agent is stopped and doing nothing, so answer promptly — a blocked agent
burns wall-clock and no tokens, and nobody else will notice.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			text, err := textArg(args, 1, file)
			if err != nil {
				return err
			}
			d, err := open()
			if err != nil {
				return err
			}
			ask, err := d.Answer(args[0], text)
			if err != nil {
				return err
			}
			if opts.asJSON {
				return emit(ask)
			}
			fmt.Printf("answered %s\n", ask.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "read the answer from a file, or from standard input with -")
	return cmd
}

func newStopCmd() *cobra.Command {
	var mode string
	cmd := &cobra.Command{
		Use:   "stop <engagement>",
		Short: "Stop an engagement that is going the wrong way or is finished",
		Long: `Stopping never destroys a transcript, and leaves the conversation
resumable.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := open()
			if err != nil {
				return err
			}
			return d.Stop(cmd.Context(), args[0], harness.StopMode(mode))
		},
	}
	cmd.Flags().StringVar(&mode, "mode", string(harness.StopEnd), "end | interrupt")
	return cmd
}

func newNoteCmd() *cobra.Command {
	var file string

	cmd := &cobra.Command{
		Use:   "note <engagement> [text]",
		Short: "Record something about an engagement you will need later",
		Long: `Write the note when you form the thought, not when you need it.

Your own context will be compacted; notes attached to an engagement will not.
Use it for why you spawned this, what you decided, what you are waiting on, and
what you promised somebody.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			text, err := textArg(args, 1, file)
			if err != nil {
				return err
			}
			d, err := open()
			if err != nil {
				return err
			}
			return d.Note(args[0], text)
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "read the note from a file, or from standard input with -")
	return cmd
}

// engagementFromEnv reads the identity injected into a spawned agent.
func engagementFromEnv() (id, token string, err error) {
	id = os.Getenv(director.EnvEngagement)
	token = os.Getenv(director.EnvToken)
	if id == "" || token == "" {
		return "", "", fmt.Errorf(
			"this command is for agents running under a director: %s and %s are not set.\n"+
				"If you are the director, you want `director status` instead",
			director.EnvEngagement, director.EnvToken)
	}
	return id, token, nil
}

func newReportCmd() *cobra.Command {
	var progress, message string

	cmd := &cobra.Command{
		Use:   "report",
		Short: "Tell your director what you are doing (run this as an agent)",
		Long: `For an agent running as an engagement, not for a director.

Your director cannot see your conversation. This is the only thing it knows
about you, and silence is what makes it think you are stuck.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			id, token, err := engagementFromEnv()
			if err != nil {
				return err
			}
			d, err := open()
			if err != nil {
				return err
			}
			engagement, err := d.Report(id, token, progress, message)
			if err != nil {
				return err
			}
			if opts.asJSON {
				return emit(engagement)
			}
			fmt.Printf("reported %s\n", strings.TrimSpace(engagement.Progress+" "+message))
			return nil
		},
	}
	cmd.Flags().StringVar(&progress, "progress", "", "progress value from your task's vocabulary ($DIRECTOR_PROGRESS)")
	cmd.Flags().StringVar(&message, "message", "", "one line on what you are doing")
	return cmd
}

func newAskCmd() *cobra.Command {
	var wait bool
	var timeout time.Duration
	var file string

	cmd := &cobra.Command{
		Use:   "ask [question]",
		Short: "Ask your director for a decision (run this as an agent)",
		Long: `For an agent running as an engagement, not for a director.

Use this when a choice is not yours to make. With --wait it blocks until
somebody answers and prints the answer, so it can be used directly:

    ANSWER=$(director ask "May I force-push to feat/auth?" --wait)

Asking marks you as blocked, which is how anyone finds out you are waiting.
Silently picking an option instead is how the wrong thing gets done quietly.`,
		Args: cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			question, err := textArg(args, 0, file)
			if err != nil {
				return err
			}
			id, token, err := engagementFromEnv()
			if err != nil {
				return err
			}
			d, err := open()
			if err != nil {
				return err
			}
			ask, err := d.Ask(id, token, question)
			if err != nil {
				return err
			}
			if !wait {
				if opts.asJSON {
					return emit(ask)
				}
				fmt.Println(ask.ID)
				return nil
			}

			ctx := cmd.Context()
			if timeout > 0 {
				var cancel func()
				ctx, cancel = contextWithTimeout(ctx, timeout)
				defer cancel()
			}
			answered, err := d.AwaitAnswer(ctx, ask.ID, 2*time.Second)
			if err != nil {
				return err
			}
			if opts.asJSON {
				return emit(answered)
			}
			fmt.Println(answered.Answer)
			return nil
		},
	}
	cmd.Flags().BoolVar(&wait, "wait", false, "block until answered and print the answer")
	cmd.Flags().DurationVar(&timeout, "timeout", time.Hour, "how long to wait")
	cmd.Flags().StringVar(&file, "file", "", "read the question from a file, or from standard input with -")
	return cmd
}
