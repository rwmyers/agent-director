// Package cli is one front-end over the director core.
//
// It parses flags, formats output, and chooses exit codes. It does not decide
// anything else. The test of whether a piece of logic belongs here is simple:
// if a TUI or a monitoring daemon would need the same behaviour, it belongs in
// the director package instead. Keeping that line is what lets another
// front-end be a peer of this one rather than something that shells out to it
// and parses its output.
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/rwmyers/agent-director/director"
	"github.com/spf13/cobra"
)

// Exit codes. They carry meaning so that both a director and a shell script can
// branch on an outcome without parsing prose.
const (
	exitOK        = 0
	exitError     = 1
	exitNotFound  = 2
	exitUnreachab = 3
	// exitTimeout is distinct from exitError so that a script waiting on a
	// fleet can tell "nothing happened in time" — a normal outcome it should
	// loop on — from "the wait itself failed".
	exitTimeout = 4
	// exitHostCannotWait is distinct from exitError so that a director, or a
	// script, can tell "this host cannot do that" — which is a fact about where
	// it is running and will not change by retrying — from a wait that broke.
	exitHostCannotWait = 5
)

type globals struct {
	config     string
	directorID string
	asJSON     bool
}

var opts globals

// Main runs the command-line interface.
func Main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "director: "+err.Error())
		os.Exit(codeFor(err))
	}
}

func codeFor(err error) int {
	switch {
	case errors.Is(err, director.ErrNotFound):
		return exitNotFound
	case errors.Is(err, errHarnessUnreachable):
		return exitUnreachab
	case errors.Is(err, director.ErrWaitTimeout):
		return exitTimeout
	case errors.Is(err, director.ErrHostCannotWait):
		return exitHostCannotWait
	default:
		return exitError
	}
}

var errHarnessUnreachable = errors.New("harness unreachable")

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "director",
		Short: "Run and coordinate independent agent conversations",
		Long: `director delegates work to independent agent conversations across coding
harnesses, and keeps track of what each of them is doing.

It is a substrate, not an agent. The director is whatever agent conversation
runs these commands — priming itself with the shipped skills — or a person, or
a script. Anything that can run a command can drive it.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.PersistentFlags().StringVar(&opts.config, "config", "",
		"configuration root to use (default: nearest .director, then ~/.config/director)")
	root.PersistentFlags().StringVar(&opts.directorID, "director", "",
		"which director to act as (default: $DIRECTOR_ID, or the only one)")
	root.PersistentFlags().BoolVar(&opts.asJSON, "json", false,
		"emit machine-readable JSON")

	root.AddCommand(
		newInitCmd(),
		newWhereCmd(),
		newWorkflowsCmd(),
		newTasksCmd(),
		newDirectorsCmd(),
		newRepairCmd(),
		newRetireCmd(),
		newHarnessesCmd(),

		newAttachCmd(),
		newSkillsCmd(),
		newInstallCmd(),

		newSpawnCmd(),
		newStatusCmd(),
		newReadCmd(),
		newWatchCmd(),
		newWaitCmd(),
		newResumeCmd(),
		newSendCmd(),
		newNudgeCmd(),
		newAnswerCmd(),
		newStopCmd(),
		newRemoveCmd(),
		newNoteCmd(),

		newReportCmd(),
		newAskCmd(),
	)
	return root
}

// resolveRoots works out which configuration applies to this invocation.
func resolveRoots() (director.Roots, error) {
	wd, err := os.Getwd()
	if err != nil {
		return director.Roots{}, err
	}
	return director.ResolveRoots(opts.config, wd)
}

// open loads the director this invocation acts as.
func open() (*director.Director, error) {
	roots, err := resolveRoots()
	if err != nil {
		return nil, err
	}
	return director.Open(roots, opts.directorID, director.SystemClock)
}

// writeRow writes one tab-separated line to a tabwriter.
//
// Wrapped so the write error is handled in one place rather than ignored at
// every call site: a failed write to stdout is a broken pipe, which happens
// routinely when a consumer pipes into head, and should end the command
// quietly rather than be silently dropped.
func writeRow(out io.Writer, format string, args ...any) error {
	_, err := fmt.Fprintf(out, format, args...)
	return err
}

// combineFailures turns what a multi-target command could not do into one
// error.
//
// A single failure is returned exactly as it stands, so its wrapping survives
// and the exit code still says what kind of failure it was. Several are joined
// under a count of how many of how many, because a command that acted on some
// of its arguments and not others is only intelligible if it says which.
func combineFailures(what string, failures []error, attempted int) error {
	switch len(failures) {
	case 0:
		return nil
	case 1:
		return failures[0]
	default:
		var out strings.Builder
		fmt.Fprintf(&out, "%d of %d could not be %s:", len(failures), attempted, what)
		for _, failure := range failures {
			fmt.Fprintf(&out, "\n\n%v", failure)
		}
		return fmt.Errorf("%s", out.String())
	}
}

// emit writes a value as JSON, used by every --json path so that there is one
// marshalling route and the human and machine views cannot drift in meaning.
func emit(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
