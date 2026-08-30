package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/rwmyers/agent-director/director"
	"github.com/spf13/cobra"
)

func newReadCmd() *cobra.Command {
	var opt director.ReadOptions
	var all bool
	var include string

	cmd := &cobra.Command{
		Use:   "read <engagement>",
		Short: "Find out what an engagement actually did",
		Long: `Run this after "director status" shows an engagement is blocked,
complete or abandoned — status tells you whether to look, this tells you what
you are looking at.

By default it returns only what the agent said, only what is new since your
last read, and trims to fit a context budget. Read repeatedly rather than
asking for everything at once.

Your own brief and follow-ups are excluded: you wrote them, and a composed
brief is long enough to eat most of the budget before the agent has said a
word. Add --include user,assistant to see both. Tool calls and thinking are
larger still and are worth their cost only when diagnosing what an agent
actually ran, never for finding out what it concluded.

Some harnesses can only return a terminal snapshot. The result says so, and an
absence of output there is not evidence that the agent said nothing.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := open()
			if err != nil {
				return err
			}
			opt.SinceLastRead = !all && opt.Cursor == ""
			if include != "" {
				opt.Include = strings.Split(include, ",")
			}

			result, err := d.Read(cmd.Context(), args[0], opt)
			if err != nil {
				return err
			}
			if opts.asJSON {
				return emit(result)
			}

			if result.Omitted > 0 {
				fmt.Printf("[%d earlier turns omitted; --cursor %s to re-read from there]\n\n",
					result.Omitted, result.Cursor)
			}
			for _, turn := range result.Turns {
				stamp := ""
				if !turn.At.IsZero() {
					stamp = turn.At.Local().Format("15:04:05") + " "
				}
				fmt.Printf("%s%s:\n%s\n\n", stamp, turn.Role, strings.TrimSpace(turn.Text))
			}
			if len(result.Turns) == 0 {
				fmt.Println("nothing new since your last read")
			}
			if !result.Complete {
				fmt.Println("[this harness returns a screen snapshot; scrollback is finite, so earlier output may be gone rather than absent]")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&opt.Cursor, "cursor", "", "read from an explicit position instead of your last read")
	cmd.Flags().BoolVar(&all, "all", false, "read from the beginning rather than since your last read")
	cmd.Flags().IntVar(&opt.Limit, "limit", 0, "maximum turns to return")
	cmd.Flags().IntVar(&opt.MaxChars, "max-chars", 0, "context budget; oldest turns are dropped first")
	cmd.Flags().StringVar(&include, "include", "", "comma-separated roles (default: assistant): user,assistant,tool_use,thinking")
	return cmd
}

func newWatchCmd() *cobra.Command {
	var interval time.Duration
	var initial bool

	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Stream engagement transitions until interrupted",
		Long: `Emits a line whenever an engagement's health, lifecycle or progress
changes. Intended for things that are not agent conversations — a status bar, a
monitoring script, a notifier — which want to tail a pipe rather than poll.

Only changes are emitted, so a consumer never has to dedupe:

    director watch --json | while read -r line; do
      printf '%s\n' "$line" | jq -r '"\(.engagement) \(.was // "-")->\(.health) \(.title)"'
    done

A director should generally use "director status" on its own turn instead;
blocking on this would mean doing nothing while it waits.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := open()
			if err != nil {
				return err
			}
			feed, err := d.Watch(cmd.Context(), director.WatchOptions{
				Interval: interval, IncludeInitial: initial,
			})
			if err != nil {
				return err
			}

			encoder := json.NewEncoder(os.Stdout)
			for transition := range feed {
				if opts.asJSON {
					if err := encoder.Encode(transition); err != nil {
						return err
					}
					continue
				}
				was := ""
				if transition.Was != "" {
					was = string(transition.Was) + " -> "
				}
				fmt.Printf("%s  %s  %s%s  %s/%s  %s\n",
					transition.At.Local().Format("15:04:05"), transition.Engagement,
					was, transition.Health, transition.Lifecycle,
					orDash(transition.Progress), transition.Title)
			}
			return nil
		},
	}
	cmd.Flags().DurationVar(&interval, "interval", director.DefaultWatchInterval, "how often to poll the harnesses")
	cmd.Flags().BoolVar(&initial, "initial", false, "emit the current state of every engagement before watching")
	return cmd
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func newResumeCmd() *cobra.Command {
	var fork bool
	var prompt string

	cmd := &cobra.Command{
		Use:   "resume <engagement>",
		Short: "Reopen a finished engagement and carry on",
		Long: `Reopens a conversation that has no process attached, keeping its
context.

--fork branches instead: the original is left untouched and a NEW engagement is
created from its context, with its own id. Use it to try a second approach
without losing the first.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := open()
			if err != nil {
				return err
			}
			engagement, err := d.Resume(cmd.Context(), args[0], director.ResumeOptions{
				Fork: fork, Prompt: prompt,
			})
			if err != nil {
				return err
			}
			if opts.asJSON {
				return emit(engagement)
			}
			if fork {
				fmt.Printf("%s  %s  [forked from %s]\n", engagement.ID, engagement.Title, args[0])
			} else {
				fmt.Printf("%s  %s  [resumed]\n", engagement.ID, engagement.Title)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&fork, "fork", false, "branch a new engagement instead of continuing this one")
	cmd.Flags().StringVar(&prompt, "prompt", "", "what to say on picking it back up")
	return cmd
}
