package cli

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/rwmyers/agent-director/director"
	"github.com/rwmyers/agent-director/harness"
	"github.com/spf13/cobra"
)

// starters are the shipped example workflows and prompts.
//
// They are copied into a root by `director init` rather than referenced from
// the binary, so that a user's first edit is to a real file they own. A
// workflow that lived inside the executable would be one the user could read
// and not change, which is the opposite of the point.
//
//go:embed all:starters
var starters embed.FS

func newInitCmd() *cobra.Command {
	var workflow, name string
	var force, global bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Register a director in this configuration root",
		Long: `Creates a configuration root if there is not one already, copies the
starter workflows into it, and registers a director bound to one of them.

The workflow binding is permanent. A director's engagements are validated
against its workflow's task types and progress vocabularies, so switching it
later would leave a live fleet that nothing could describe.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			roots, err := initRoots(global)
			if err != nil {
				return err
			}
			created, err := materialiseStarters(roots.Primary, force)
			if err != nil {
				return err
			}
			for _, path := range created {
				fmt.Printf("wrote %s\n", path)
			}

			// Creating a second director is legitimate, but it must never be
			// silent: an agent that runs init reflexively would otherwise make
			// every subsequent command refuse to run, with nothing to explain
			// why it had started failing.
			existing, err := director.ListDirectors(roots.Primary)
			if err != nil {
				return err
			}

			if workflow == "" {
				workflow = "default"
			}
			state, err := director.Init(roots, workflow, name, director.SystemClock)
			if err != nil {
				return err
			}

			if opts.asJSON {
				return emit(map[string]string{
					"director": state.DirectorID,
					"name":     state.Name,
					"workflow": state.Workflow,
					"root":     roots.Primary,
				})
			}
			fmt.Printf("\ndirector %s (%s) initialised for workflow %q\n", state.DirectorID, state.Name, state.Workflow)
			fmt.Printf("root: %s\n\n", roots.Primary)
			if !global && opts.config == "" {
				fmt.Printf("This root is local to this project. Directors and engagements under it\n")
				fmt.Printf("are invisible to other projects. Use --global for a machine-wide setup.\n\n")
			}

			if len(existing) > 0 {
				fmt.Printf("Note: %d director(s) already existed here, so this is an additional one.\n", len(existing))
				fmt.Printf("If you meant to use an existing one instead, see `director directors`.\n\n")
			}

			// What comes next is a person's work, not an agent's: the task
			// types are decisions about how this project delegates, and they
			// are the whole substance of a workflow. Pointing at spawn here
			// would invite somebody to dispatch work under the starters
			// without having read what they instruct an agent to do.
			fmt.Printf("Next:\n\n")
			fmt.Printf("  1. Make the task types yours. The starters are examples, not defaults:\n\n")
			fmt.Printf("       %s\n", filepath.Join(roots.Primary, "workflows"))
			fmt.Printf("       %s\n\n", filepath.Join(roots.Primary, "prompts"))
			fmt.Printf("     Each task's prompt is what an agent is actually told. `director tasks`\n")
			fmt.Printf("     lists what you have; `director tasks <name>` shows one in full.\n\n")
			fmt.Printf("  2. Install the director skills so your harness can pick them up:\n\n")
			fmt.Printf("       director install\n\n")
			fmt.Printf("  3. Then, in an agent conversation, start directing:\n\n")
			fmt.Printf("       director attach\n\n")
			fmt.Printf("     That decides whether to take over a running director or start one,\n")
			fmt.Printf("     and prints the id to use. You do not need to pick one yourself.\n\n")
			fmt.Printf("Commit %s if you want this workflow shared; leave state/ out of version control.\n",
				director.ProjectDirName)
			return nil
		},
	}
	cmd.Flags().StringVar(&workflow, "workflow", "", "workflow to bind this director to (default: default)")
	cmd.Flags().StringVar(&name, "name", "", "human label for this director")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite starter files that already exist")
	cmd.Flags().BoolVar(&global, "global", false, "set up in the user root rather than this project")
	return cmd
}

// initRoots decides where `director init` sets things up.
//
// Unlike every other command, init defaults to creating a project root here
// rather than resolving an existing one. Setting up is the act of making a
// project a director project, and quietly landing that configuration in the
// user root instead — which is what plain resolution does when no .director
// exists yet — produces a global setup somebody asked for locally, with no
// indication it happened.
//
// An explicit --config still wins, and an existing .director at or above the
// working directory is reused rather than nested inside itself.
func initRoots(global bool) (director.Roots, error) {
	if opts.config != "" || global {
		return resolveRoots()
	}
	wd, err := os.Getwd()
	if err != nil {
		return director.Roots{}, err
	}
	// Reuse a project root that already exists, wherever it is above us.
	if found, err := director.ResolveRoots("", wd); err == nil && found.Layers[0].Kind == director.LayerProject {
		return found, nil
	}
	return director.ResolveRoots(filepath.Join(wd, director.ProjectDirName), wd)
}

// materialiseStarters copies the embedded starters into a root.
//
// Only into a root that has no workflows yet, unless forced. Skipping files
// that already exist is not enough: a starter the user deliberately deleted
// would come back on the next init, quietly reintroducing a workflow they had
// removed — and a second workflow is not inert, it makes creating a director
// ambiguous. An established root is left exactly as its owner left it.
func materialiseStarters(root string, force bool) ([]string, error) {
	var written []string

	if !force {
		if entries, err := os.ReadDir(director.WorkflowsDir(root)); err == nil && len(entries) > 0 {
			return nil, nil
		}
	}

	err := fs.WalkDir(starters, "starters", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative := strings.TrimPrefix(path, "starters/")
		target := filepath.Join(root, relative)

		if _, statErr := os.Stat(target); statErr == nil && !force {
			return nil
		}
		body, err := starters.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, body, 0o644); err != nil { // #nosec G306 -- config is meant to be readable
			return err
		}
		written = append(written, target)
		return nil
	})
	return written, err
}

func newWhereCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "where",
		Short: "Show which configuration root is in effect, and why",
		Long: `Prints the resolved configuration root and every layer searched for
workflows. Run this first when something is configured surprisingly — the
answer is almost always that a different root won than the one expected.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			roots, err := resolveRoots()
			if err != nil {
				return err
			}
			if opts.asJSON {
				return emit(roots)
			}
			fmt.Printf("primary root (state lives here): %s\n\nsearch order for workflows:\n", roots.Primary)
			for i, layer := range roots.Layers {
				fmt.Printf("  %d. %-8s %s\n", i+1, layer.Kind, layer.Path)
			}
			return nil
		},
	}
}

func newWorkflowsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "workflows",
		Short: "List the workflows reachable from this root",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			roots, err := resolveRoots()
			if err != nil {
				return err
			}
			workflows, loadErr := roots.ListWorkflows()
			if loadErr != nil {
				// One broken workflow costs the user that workflow and says so.
				// It must not make the command fail, or an unrelated typo
				// becomes an outage.
				fmt.Fprintf(os.Stderr, "director: some workflows could not be loaded:\n%v\n\n", loadErr)
			}

			if opts.asJSON {
				return emit(workflows)
			}
			if len(workflows) == 0 {
				fmt.Println("no workflows found; run `director init` to create the starters")
				return nil
			}
			out := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			if err := writeRow(out, "NAME\tTASKS\tDESCRIPTION"+"\n"); err != nil {
				return err
			}
			for _, workflow := range workflows {
				_ = writeRow(out, "%s\t%s\t%s\n", workflow.Name,
					strings.Join(workflow.TaskNames(), ","), workflow.Description)
			}
			return out.Flush()
		},
	}
}

func newTasksCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tasks [name]",
		Short: "List the task types this director can spawn, or show one in full",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := open()
			if err != nil {
				return err
			}
			workflow := dir.Workflow

			if len(args) == 1 {
				task, err := workflow.Task(args[0])
				if err != nil {
					return err
				}
				permission, err := workflow.PermissionFor(task)
				if err != nil {
					return err
				}
				if opts.asJSON {
					return emit(map[string]any{"task": task, "permission": permission})
				}
				fmt.Printf("task:        %s\n", task.Name)
				fmt.Printf("description: %s\n", task.Description)
				fmt.Printf("permissions: %s (%s)\n", permission.Name, harness.JoinCapabilities(permission.Allow))
				fmt.Printf("progress:    %s\n", strings.Join(task.Progress, " -> "))
				fmt.Printf("terminal:    %s\n", task.Terminal)
				fmt.Printf("report_on:   %s\n", task.ReportOn)
				fmt.Printf("stalls after %s of silence with no harness activity\n", task.StallThreshold())
				if task.PromptPath != "" {
					fmt.Printf("prompt:      %s\n", task.PromptPath)
				}
				return nil
			}

			if opts.asJSON {
				tasks := make([]director.Task, 0, len(workflow.Tasks))
				for _, name := range workflow.TaskNames() {
					tasks = append(tasks, workflow.Tasks[name])
				}
				return emit(tasks)
			}
			out := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			if err := writeRow(out, "TASK\tPERMISSIONS\tPROGRESS\tDESCRIPTION"+"\n"); err != nil {
				return err
			}
			for _, name := range workflow.TaskNames() {
				task := workflow.Tasks[name]
				_ = writeRow(out, "%s\t%s\t%s\t%s\n", task.Name, task.Permission,
					strings.Join(task.Progress, ","), task.Description)
			}
			return out.Flush()
		},
	}
	return cmd
}

func newDirectorsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "directors",
		Short: "List the directors registered in this root",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			roots, err := resolveRoots()
			if err != nil {
				return err
			}
			states, err := director.ListDirectors(roots.Primary)
			if err != nil {
				return err
			}
			if opts.asJSON {
				type row struct {
					ID          string `json:"id"`
					Name        string `json:"name"`
					Workflow    string `json:"workflow"`
					Engagements int    `json:"engagements"`
				}
				rows := make([]row, 0, len(states))
				for _, state := range states {
					rows = append(rows, row{state.DirectorID, state.Name, state.Workflow, len(state.Engagements)})
				}
				return emit(rows)
			}
			if len(states) == 0 {
				fmt.Printf("no directors under %s; run `director init`\n", roots.Primary)
				return nil
			}
			out := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			if err := writeRow(out, "ID\tNAME\tWORKFLOW\tENGAGEMENTS"+"\n"); err != nil {
				return err
			}
			for _, state := range states {
				_ = writeRow(out, "%s\t%s\t%s\t%d\n", state.DirectorID, state.Name, state.Workflow, len(state.Engagements))
			}
			return out.Flush()
		},
	}
}

func newHarnessesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "harnesses",
		Short: "List the agent harnesses this machine can drive",
		Long: `Run this once before spawning anything.

Spawning into a harness that is not actually available is how a director finds
out too late that its server is not running or its binary is not installed.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			type row struct {
				Name        string   `json:"name"`
				Enforceable []string `json:"enforceable"`
				Read        bool     `json:"read"`
				Resume      bool     `json:"resume"`
			}
			var rows []row
			for _, name := range harness.Names() {
				adapter, err := harness.Lookup(name)
				if err != nil {
					continue
				}
				caps := harness.CapabilitiesOf(adapter)
				enforceable := make([]string, 0, len(adapter.Enforceable()))
				for _, capability := range adapter.Enforceable() {
					enforceable = append(enforceable, string(capability))
				}
				rows = append(rows, row{name, enforceable, caps.Read, caps.Resume})
			}
			if opts.asJSON {
				return emit(rows)
			}
			out := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			if err := writeRow(out, "HARNESS\tREAD\tRESUME\tCAN CONTROL"+"\n"); err != nil {
				return err
			}
			for _, r := range rows {
				_ = writeRow(out, "%s\t%t\t%t\t%s\n", r.Name, r.Read, r.Resume, strings.Join(r.Enforceable, ","))
			}
			return out.Flush()
		},
	}
}
