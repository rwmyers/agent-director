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
its terminal value means finished, which is what "complete" reports.

Open questions are listed by identifier, not reproduced. Run "director status
asks <id>" for the text of one.

MATERIALS is where an engagement said its work can be found, and is shortened
to fit the column. "--json" carries the whole list.`,
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

			printEngagements(engagements, d.OpenAsks())
			return nil
		},
	}
	cmd.Flags().BoolVar(&unhealthy, "unhealthy", false, "show only engagements needing action")
	cmd.AddCommand(newStatusAsksCmd())
	return cmd
}

// newStatusAsksCmd is the other half of not printing questions in the table.
//
// Naming asks gets you their text; naming none gets you the list of names. That
// is the whole rule, and the second half of it is the interesting decision.
// Listing every open question in full is exactly the behaviour `status` was
// changed to stop doing — five blocked agents holding large plans would
// reproduce all five — so it cannot be the default for a command whose name is
// this easy to type by accident. Refusing outright was the alternative and is
// worse: a director that has lost the identifiers, or that ran this to find out
// what the subcommand does, would get an error where the answer it wanted is
// one short line per question.
func newStatusAsksCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "asks [ask ...]",
		Short: "Show the full text of questions engagements are waiting on",
		Long: `The text "director status" deliberately leaves out.

Name the questions you want and their full text is printed. Name none and you
get the open ones by identifier only — the same compact index "director status"
prints, without the fleet table around it — because a command that dumped every
open question would recreate the problem that took them out of the table.

An identifier that names nothing does not cost the ones that do: the questions
found are printed, and the ones that were not are named in the error.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := open()
			if err != nil {
				return err
			}

			if len(args) == 0 {
				pending := d.OpenAsks()
				if opts.asJSON {
					ids := make([]string, 0, len(pending))
					for _, ask := range pending {
						ids = append(ids, ask.ID)
					}
					return emit(ids)
				}
				if len(pending) == 0 {
					fmt.Println("no open questions")
					return nil
				}
				printAskIndex(pending)
				return nil
			}

			asks, lookupErr := d.Asks(args)
			if opts.asJSON {
				if lookupErr != nil {
					return lookupErr
				}
				return emit(asks)
			}
			printAsks(asks)
			return lookupErr
		},
	}
}

// printEngagements renders the fleet. Shared with attach, which shows the same
// table when a conversation inherits work — the same facts should not look
// different depending on which command surfaced them.
func printEngagements(engagements []*director.Engagement, asks []*director.Ask) {
	now := time.Now()
	out := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_ = writeRow(out, "ID\tHEALTH\tLIFECYCLE\tPROGRESS\tSILENT\tMATERIALS\tTITLE\n")
	for _, engagement := range engagements {
		progress := engagement.Progress
		if progress == "" {
			progress = "-"
		}
		_ = writeRow(out, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			engagement.ID, engagement.Health, engagement.Lifecycle,
			progress, short(engagement.SilentFor(now)),
			materialsCell(engagement.Materials), engagement.Title)
	}
	_ = out.Flush()

	printOpenAsks(asks, engagements)
}

// materialWidth is how much of a material one table cell may spend.
//
// Materials are branches, pull request URLs and paths, and a URL alone can run
// past eighty columns. The table is what a director reads every turn, so the
// cell is capped and the rest lives in --json, which is asked for deliberately
// and once.
const materialWidth = 28

// materialsCell renders an engagement's materials short enough to sit in a
// column: the first one, clipped, and a count of the others.
//
// Nothing at all when there are none — not a dash and not "0". An engagement
// that has reported no materials has to look like an engagement that has
// reported no materials, or a director cannot tell "nothing yet" from
// "something".
func materialsCell(materials []string) string {
	if len(materials) == 0 {
		return ""
	}
	cell := clip(materials[0], materialWidth)
	if rest := len(materials) - 1; rest > 0 {
		cell += fmt.Sprintf(" +%d", rest)
	}
	return cell
}

// printOpenAsks says what is waiting, by identifier, under the fleet table.
//
// It reproduces no question text. What it has to preserve from the output it
// replaced is both of that output's jobs: that something is waiting at all, and
// the exact command that resolves it. So the count leads, every identifier is
// listed against the engagement that raised it, and the commands to read and to
// answer follow — with the real identifier substituted when there is only one,
// which is the ordinary case.
func printOpenAsks(asks []*director.Ask, engagements []*director.Engagement) {
	shown := make(map[string]bool, len(engagements))
	for _, engagement := range engagements {
		shown[engagement.ID] = true
	}
	var open []*director.Ask
	for _, ask := range asks {
		if !ask.Answered() && shown[ask.Engagement] {
			open = append(open, ask)
		}
	}
	if len(open) == 0 {
		return
	}

	fmt.Println()
	printAskIndex(open)
}

// printAskIndex is the compact listing itself: one bounded line per question
// saying which it is, whose it is, and how long it has been sitting there, then
// the two commands that act on it.
//
// The read command names every identifier, because reading what is waiting is
// the thing a director does next and the list is short. The answer command
// takes a placeholder unless there is exactly one question, since a different
// answer goes to each — and one is the ordinary case, where the exact command
// is what the output this replaced always gave.
func printAskIndex(open []*director.Ask) {
	if len(open) == 1 {
		fmt.Println("1 open question, text not shown:")
	} else {
		fmt.Printf("%d open questions, text not shown:\n", len(open))
	}

	now := time.Now()
	out := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	ids := make([]string, 0, len(open))
	for _, ask := range open {
		_ = writeRow(out, "  %s\t%s\twaiting %s\n", ask.ID, ask.Engagement, short(now.Sub(ask.AskedAt)))
		ids = append(ids, ask.ID)
	}
	_ = out.Flush()

	target := "<ask>"
	if len(ids) == 1 {
		target = ids[0]
	}
	fmt.Printf("\n  read:    director status asks %s\n", strings.Join(ids, " "))
	fmt.Printf("  answer:  director answer %s \"...\"\n", target)
}

// printAsks prints questions in full — the one place that does.
func printAsks(asks []*director.Ask) {
	now := time.Now()
	for i, ask := range asks {
		if i > 0 {
			fmt.Println()
		}
		fmt.Printf("%s  %s  asked %s ago\n\n", ask.ID, ask.Engagement, short(now.Sub(ask.AskedAt)))
		fmt.Printf("%s\n\n", ask.Question)
		if ask.Answered() {
			fmt.Printf("  answered %s ago: %s\n", short(now.Sub(ask.AnsweredAt)), ask.Answer)
			continue
		}
		fmt.Printf("  answer with: director answer %s \"...\"\n", ask.ID)
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
	var materials []string

	cmd := &cobra.Command{
		Use:   "report",
		Short: "Tell your director what you are doing (run this as an agent)",
		Long: `For an agent running as an engagement, not for a director.

Your director cannot see your conversation. This is the only thing it knows
about you, and silence is what makes it think you are stuck.

--materials is where your work can be found: a branch, a pull request, a path,
a document. Repeat the flag once per item. You are the only source for it — a
director reading it out of your prose is guessing — and nothing else in the
system records where your work landed.

Giving --materials replaces the whole set with what you named, so name
everything that is still current. Leaving it off changes nothing, so an
ordinary progress report never loses what you recorded before.`,
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
			engagement, err := d.Report(cmd.Context(), id, token, director.ReportOptions{
				Progress:  progress,
				Message:   message,
				Materials: materials,
			})
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
	cmd.Flags().StringArrayVar(&materials, "materials", nil,
		"where your work can be found — a branch, a PR, a path; repeat per item, replaces the set")
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
			ask, err := d.Ask(cmd.Context(), id, token, question)
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
	durationFlag(cmd, &timeout, "timeout", time.Hour, "how long to wait")
	cmd.Flags().StringVar(&file, "file", "", "read the question from a file, or from standard input with -")
	return cmd
}
