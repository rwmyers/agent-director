package cli

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/charmbracelet/huh"
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
	var workflow, name, harnessName string
	var force, global, createNew bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Register a director in this configuration root",
		Long: `Creates a configuration root if there is not one already, copies the
starter workflows into it, and registers a director bound to one of them.

Asks which harness this project spawns into and writes it into director.conf.
Pass --harness to answer up front, which is what a setup script wants; without
a terminal to ask on, or under --json, that flag is required rather than
guessed at.

Running it again in the same root is safe: it adopts the director already
registered there and creates nothing, so a setup script can run unconditionally.
Pass --new to add a second director to a root that already has one — that is a
real thing to want, and it is a decision rather than something you should reach
by running the same command twice.

The workflow binding is permanent. A director's engagements are validated
against its workflow's task types and progress vocabularies, so switching it
later would leave a live fleet that nothing could describe.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			roots, err := initRoots(global)
			if err != nil {
				return err
			}
			pending, err := pendingStarters(roots.Primary, force)
			if err != nil {
				return err
			}

			// The harness is settled before anything is written, because it is
			// the one starter value that is a decision rather than a copy, and
			// half a root written before the question is refused would leave
			// somebody to work out what they now have.
			//
			// Either way the question gets asked. Which of the two branches
			// runs decides whether the answer is written or reported, never
			// whether it is put.
			effectiveHarness := ""
			if writesStarterConfig(pending) {
				effectiveHarness, err = resolveHarness(harnessName, initPrompter())
			} else {
				effectiveHarness, err = reviewHarness(roots.Primary, harnessName, initPrompter())
			}
			if err != nil {
				return err
			}

			starterFiles, err := writeStarters(pending, effectiveHarness)
			if err != nil {
				return err
			}
			// The list of files goes into the JSON object rather than ahead of
			// it: prose on stdout before the object leaves a machine reading
			// --json with something it cannot parse.
			if !opts.asJSON {
				for _, path := range starterFiles {
					fmt.Printf("wrote %s\n", path)
				}
			}

			// Adopt-or-create is a decision, so it is the core's and not this
			// command's: a TUI setting a project up would have to make exactly
			// the same one.
			state, created, err := director.Register(roots, workflow, name, createNew, director.SystemClock)
			if err != nil {
				return err
			}
			// What the root holds now, not what it held before. A second
			// director makes every later command refuse to choose, and that has
			// to be said here — by the command that caused it, while somebody
			// is still looking at the output.
			registered, unreadable := director.ListDirectors(roots.Primary)

			if opts.asJSON {
				action := "adopted"
				if created {
					action = "created"
				}
				if starterFiles == nil {
					// An empty list rather than null: a consumer should be able
					// to range over it without a nil check.
					starterFiles = []string{}
				}
				if err := emit(map[string]any{
					"director":  state.DirectorID,
					"name":      state.Name,
					"workflow":  state.Workflow,
					"root":      roots.Primary,
					"harness":   effectiveHarness,
					"action":    action,
					"created":   created,
					"directors": len(registered),
					"ambiguous": len(registered) > 1,
					"wrote":     starterFiles,
				}); err != nil {
					return err
				}
				reportUnreadable(unreadable)
				return nil
			}
			if created {
				fmt.Printf("\ndirector %s (%s) initialised for workflow %q\n", state.DirectorID, state.Name, state.Workflow)
			} else {
				fmt.Printf("\ndirector %s (%s) is already registered here for workflow %q, so init used it\n",
					state.DirectorID, state.Name, state.Workflow)
				fmt.Printf("Nothing was created. Pass --new to add a second director to this root.\n")
			}
			fmt.Printf("root: %s\n\n", roots.Primary)
			if !global && opts.config == "" {
				fmt.Printf("This root is local to this project. Directors and engagements under it\n")
				fmt.Printf("are invisible to other projects. Use --global for a machine-wide setup.\n\n")
			}

			if len(registered) > 1 {
				fmt.Printf("This root now holds %d directors, so no command here can pick one for you.\n", len(registered))
				fmt.Printf("Say which one you are, in every shell that acts as it:\n\n")
				fmt.Printf("    export %s=%s\n\n", director.EnvID, state.DirectorID)
				fmt.Printf("`director directors` lists them; `director retire <id>` removes one you do not want.\n\n")
			}
			reportUnreadable(unreadable)

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
	cmd.Flags().StringVar(&harnessName, "harness", "", "harness to spawn on, skipping the question (default: ask)")
	cmd.Flags().StringVar(&name, "name", "", "human label for this director")
	cmd.Flags().BoolVar(&createNew, "new", false, "register another director here rather than using the one already registered")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite starter files that already exist")
	cmd.Flags().BoolVar(&global, "global", false, "set up in the user root rather than this project")
	return cmd
}

// initRoots decides where `director init` sets things up.
//
// Unlike every other command, init creates rather than resolves, and one part
// of resolution does not survive that change: the walk up. A command run in a
// subdirectory should find its project, and for reading that is right. For
// creating it is a trap — a plain `director init` in a subdirectory or in a
// sibling worktree used to register a director in whichever fleet happened to
// be above it. Nothing said so, and the root it landed in was then left with
// two directors of the same name, which makes every later command refuse to
// run until somebody passes --director.
//
// So a root found only by walking up is reported and not adopted. Everything
// that names a root outright is honoured, in the same order ResolveRoots uses:
// --config or --global, then DIRECTOR_ROOT, then a .director in the working
// directory itself — that last being the ordinary "add another director to the
// project I am standing in". The line is not how near the root is, it is
// whether somebody said where it was.
func initRoots(global bool) (director.Roots, error) {
	if opts.config != "" || global {
		return resolveRoots()
	}
	wd, err := os.Getwd()
	if err != nil {
		return director.Roots{}, err
	}
	// DIRECTOR_ROOT is a root somebody named, whether in a setup script or in
	// the environment director injects into the agents it spawns. Resolution
	// puts it above the walk up and so does this, so that a root reached by
	// exporting one variable is the same root every other command would use.
	if os.Getenv(director.EnvRoot) != "" {
		return resolveRoots()
	}
	here := filepath.Join(wd, director.ProjectDirName)

	if info, statErr := os.Stat(here); statErr == nil && info.IsDir() {
		return director.ResolveRoots(here, wd)
	}
	if found, findErr := director.ResolveRoots("", wd); findErr == nil &&
		found.Layers[0].Kind == director.LayerProject {
		return director.Roots{}, foundAbove(found.Primary, wd, here)
	}
	return director.ResolveRoots(here, wd)
}

// foundAbove is what init says instead of adopting a root it only found by
// walking up out of the directory it was run in.
func foundAbove(found, wd, here string) error {
	return fmt.Errorf(`a configuration root already exists above this directory:

  found:             %s
  working directory: %s

init will not adopt a root it was not pointed at: a director registered in a
fleet you are not looking at is invisible to you and ambiguous to everyone
else. Say which you meant:

  director init --config %s
      add a director to the root that already exists

  director init --config %s
      make this directory a project root of its own`,
		found, wd, found, here)
}

// starterConfigPath is the one starter whose contents are decided at init time
// rather than copied verbatim.
const starterConfigPath = "starters/director.conf"

// harnessPlaceholder is what the chosen harness is substituted for.
const harnessPlaceholder = "{{harness}}"

// initPrompter is the prompter `director init` asks with. Only a test replaces
// it, so that the asking path can be driven from scripted input.
var initPrompter = newPrompter

// starterFile is one starter this init will write: where it comes from in the
// embedded tree, and where it lands.
type starterFile struct {
	source string
	target string
}

// pendingStarters works out which starters this init will write, without
// writing any of them.
//
// Deciding first is what lets init ask its questions before it touches the
// disk, and it keeps the skip rules in one place rather than in one function
// that decides and another that guesses the same thing again.
//
// Only a root that has no workflows yet gets starters, unless forced. Skipping
// files that already exist is not enough: a starter the user deliberately
// deleted would come back on the next init, quietly reintroducing a workflow
// they had removed — and a second workflow is not inert, it makes creating a
// director ambiguous. An established root is left exactly as its owner left it.
func pendingStarters(root string, force bool) ([]starterFile, error) {
	if !force {
		if entries, err := os.ReadDir(director.WorkflowsDir(root)); err == nil && len(entries) > 0 {
			return nil, nil
		}
	}

	var pending []starterFile
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
		pending = append(pending, starterFile{source: path, target: target})
		return nil
	})
	return pending, err
}

// writesStarterConfig reports whether the core configuration is among the
// starters about to be written — which is to say, whether there is anywhere for
// an answer about the harness to go.
func writesStarterConfig(pending []starterFile) bool {
	for _, file := range pending {
		if file.source == starterConfigPath {
			return true
		}
	}
	return false
}

// writeStarters copies the pending starters into the root, substituting the
// chosen harness into the core configuration on the way past.
func writeStarters(pending []starterFile, chosenHarness string) ([]string, error) {
	var written []string
	for _, file := range pending {
		body, err := starters.ReadFile(file.source)
		if err != nil {
			return written, err
		}
		if file.source == starterConfigPath {
			if chosenHarness == "" {
				return written, fmt.Errorf("internal: writing %s with no harness chosen", file.target)
			}
			body = []byte(strings.ReplaceAll(string(body), harnessPlaceholder, chosenHarness))
		}
		if err := os.MkdirAll(filepath.Dir(file.target), 0o755); err != nil {
			return written, err
		}
		if err := os.WriteFile(file.target, body, 0o644); err != nil { // #nosec G306 -- config is meant to be readable
			return written, err
		}
		written = append(written, file.target)
	}
	return written, nil
}

// resolveHarness decides which harness a new root spawns into: the flag, or
// the question.
//
// Asking is the point. The harness is the one part of a root's configuration
// that nothing else can infer, and a default written on somebody's behalf is a
// decision they never made and will not think to look for. So there is no
// fallback: without a terminal to ask on and without the flag, this refuses and
// names the choices, because writing a harness nobody picked is the defect it
// exists to prevent.
//
// --json is the same situation by a different route. A caller parsing JSON
// cannot answer a question, and the prompt would render onto the stdout it is
// reading — so the answer has to arrive as a flag there too.
func resolveHarness(flag string, ask prompter) (string, error) {
	names := harness.Names()

	if flag != "" {
		return flag, checkHarness(flag)
	}
	if len(names) == 0 {
		return "", fmt.Errorf("this build of director has no harness adapters registered, so there is nothing to spawn into")
	}
	if opts.asJSON {
		return "", fmt.Errorf("no harness chosen, and --json has nobody to ask: pass --harness (valid: %s)",
			strings.Join(names, ", "))
	}
	if !ask.terminal {
		return "", fmt.Errorf("no harness chosen and no terminal to ask on: pass --harness (valid: %s)",
			strings.Join(names, ", "))
	}

	chosen, err := askHarness(ask, "")
	if err != nil {
		return "", err
	}
	if chosen == "" {
		return "", fmt.Errorf("no harness chosen (valid: %s)", strings.Join(names, ", "))
	}
	return chosen, nil
}

// checkHarness rejects a name no adapter answers to.
func checkHarness(name string) error {
	if _, err := harness.Lookup(name); err != nil {
		return fmt.Errorf("unknown harness %q (valid: %s)", name, strings.Join(harness.Names(), ", "))
	}
	return nil
}

// askHarness puts the question, through the same prompter `director install`
// asks with. One mechanism, so there is nothing to drift.
//
// The choices are the adapter registry, so what is offered is exactly what this
// binary can drive — including the plugin executables discovery found — and
// there is no second list to fall out of step with the first.
func askHarness(ask prompter, current string) (string, error) {
	names := harness.Names()
	options := make([]huh.Option[string], 0, len(names))
	for _, name := range names {
		options = append(options, huh.NewOption(name, name))
	}

	title := "Which harness should this project spawn engagements into?"
	description := "Written to director.conf as the default placement. A workflow, a task, or `director spawn --harness` still overrides it, and the file is yours to edit afterwards."
	if current != "" {
		title = fmt.Sprintf("This root spawns on %s. Which harness should it use?", current)
		description = "Its director.conf was written by hand, so init will not rewrite it. Answering says what belongs in it."
	}
	return ask.selectOne(title, description, options)
}

// reviewHarness is the harness question for a root that already has a
// director.conf.
//
// The answer is not applied, and that is deliberate: the file is hand-edited
// and commented, and init reaching into it to change a key would be init
// editing somebody's configuration behind them. But it is still asked, and
// still answered in full. Saying nothing is what let a director be registered
// against a harness nobody had picked, which is the whole complaint; declining
// to write is a different thing from declining to speak.
//
// Returns the harness the root actually spawns on, which is what it spawned on
// before init ran.
func reviewHarness(root, flag string, ask prompter) (string, error) {
	path := filepath.Join(root, "director.conf")
	config, err := director.LoadConfig(root)
	if err != nil {
		return "", err
	}
	current := config.Harness

	// A machine reading --json is not being asked anything, and prose on stdout
	// would corrupt what it is parsing. The harness is in the JSON instead.
	if opts.asJSON {
		return current, nil
	}

	chosen := flag
	switch {
	case chosen != "":
		if err := checkHarness(chosen); err != nil {
			return "", err
		}
	case ask.terminal && len(harness.Names()) > 0:
		if chosen, err = askHarness(ask, current); err != nil {
			return "", err
		}
	}

	fmt.Printf("\n")
	switch {
	case chosen == "" && current == "":
		fmt.Printf("%s sets no harness, so every spawn from this root will have to pass\n", path)
		fmt.Printf("--harness. Add one line to it:\n\n    harness = %s\n\n", strings.Join(harness.Names(), " | "))
	case chosen == "":
		fmt.Printf("This root spawns on %s, from %s, which was left as it is.\n\n", current, path)
	case chosen == current:
		fmt.Printf("%s already says harness = %s. Nothing to change.\n\n", path, current)
	default:
		fmt.Printf("%s is yours, not init's, so it was left as it is.\n", path)
		if current != "" {
			fmt.Printf("It says harness = %s. To spawn on %s instead, change that one line:\n\n", current, chosen)
		} else {
			fmt.Printf("It sets no harness. To spawn on %s, add one line:\n\n", chosen)
		}
		fmt.Printf("    harness = %s\n\n", chosen)
	}
	return current, nil
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
			// A director whose record cannot be read is listed as a problem
			// rather than allowed to hide the ones that can: one bad file used
			// to mean this command printed nothing at all.
			states, unreadable := director.ListDirectors(roots.Primary)
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
				if err := emit(rows); err != nil {
					return err
				}
				reportUnreadable(unreadable)
				return nil
			}
			if len(states) == 0 && unreadable == nil {
				fmt.Printf("no directors under %s; run `director init`\n", roots.Primary)
				return nil
			}
			if len(states) > 0 {
				out := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				if err := writeRow(out, "ID\tNAME\tWORKFLOW\tENGAGEMENTS"+"\n"); err != nil {
					return err
				}
				for _, state := range states {
					_ = writeRow(out, "%s\t%s\t%s\t%d\n", state.DirectorID, state.Name, state.Workflow, len(state.Engagements))
				}
				if err := out.Flush(); err != nil {
					return err
				}
			}
			reportUnreadable(unreadable)
			return nil
		},
	}
}

func newHarnessesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "harnesses",
		Short: "List the agent harnesses director can drive",
		Long: `Run this once before spawning anything.

Spawning into a harness that is not actually available is how a director finds
out too late that its server is not running or its binary is not installed.

This is what can be driven, which is not the same set as what skills can be
installed for — driving needs a working protocol, installing needs somewhere to
put a skill. A harness marked DISPLAY shows another harness's conversation
rather than being one, so it reads no skills of its own: install for the agent
you run inside it, and ` + "`director install`" + ` names the harnesses that are
targets.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			type row struct {
				Name        string   `json:"name"`
				Enforceable []string `json:"enforceable"`
				Read        bool     `json:"read"`
				Resume      bool     `json:"resume"`
				// Display is the adapter's own declaration that it shows
				// somebody else's conversation. It is reported here because it
				// is what explains an entry in this table that `director
				// install` will not accept.
				Display bool `json:"display"`
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
				rows = append(rows, row{name, enforceable, caps.Read, caps.Resume, harness.Displays(adapter)})
			}
			if opts.asJSON {
				return emit(rows)
			}
			out := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			if err := writeRow(out, "HARNESS\tREAD\tRESUME\tDISPLAY\tCAN CONTROL"+"\n"); err != nil {
				return err
			}
			for _, r := range rows {
				_ = writeRow(out, "%s\t%t\t%t\t%t\t%s\n", r.Name, r.Read, r.Resume, r.Display, strings.Join(r.Enforceable, ","))
			}
			return out.Flush()
		},
	}
}
