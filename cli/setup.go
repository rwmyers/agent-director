package cli

import (
	"embed"
	"errors"
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
// They are copied into a root by `director setup` rather than referenced from
// the binary, so that a user's first edit is to a real file they own. A
// workflow that lived inside the executable would be one the user could read
// and not change, which is the opposite of the point.
//
//go:embed all:starters
var starters embed.FS

// setupPrompter is the prompter `director setup` asks with. Only a test
// replaces it, so that the asking path can be driven from scripted input.
var setupPrompter = newPrompter

// workflowExplanation is what setup says before it asks where the workflow
// should live. It is the one piece of the command a first-time user has no
// other way of knowing: the questions that follow only make sense once it is
// clear that a workflow is configuration they will own, and that the director
// about to be registered is tied to it for good.
const workflowExplanation = `
A workflow is this project's delegation policy: it defines the task types a
director can spawn, the permissions each one runs with, and the progress
vocabulary each one reports in. A director is bound to one workflow permanently.
setup installs a .director/ directory holding starter workflows you then edit,
and registers a director bound to the starter.
`

func newSetupCmd() *cobra.Command {
	var scope string
	var hostNames []string
	var dryRun bool
	var workflow, name, harnessName, dir string
	var force, global, createNew bool

	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Install the director skills and set up a workflow root",
		Long: `Sets up directing in one pass: installs the director skills where a
harness will find them, then installs a workflow root and registers a director
in it.

Skills first. Asks which harness(es) to install for and whether to install for
this project or for every project on this machine, then copies the shipped
skills so an agent can pick them up as /director. Project-scope skills go into
the project the workflow is going into — the directory named by --dir, or typed
or accepted at the location question — so that both halves land in the same
repository wherever setup is run from. Under --global or --config the location
is a root rather than a project, and project-scope skills go under the current
directory instead. The harnesses offered are the ones with somewhere to put a
skill, which is not the set director can drive — ` + "`director harnesses`" + ` lists
that. A harness that displays another harness's conversation reads no skills of
its own, so install for the agent you run inside it instead. Nothing about the
skills is written into .director/: they are the director's operating
instructions and ship with the binary, so to change them, fork the file and
point your harness at your copy.

Then the workflow. A workflow defines the task types a director can spawn,
their permissions and their progress vocabularies, and a director is bound to
one permanently. setup asks where the workflow should live — the current
directory unless you type another path — creates a .director/ root there if
there is not one, copies the starter workflows into it, asks which harness this
project spawns into, and registers a director bound to the starter workflow.
--dir answers the location question with a directory, --global answers it with
the user root (~/.config/director) for a machine-wide setup, and --config names
a root outright. --global says nothing about --scope: whether the skills are
project-wide or machine-wide is a separate question from where the workflow
goes.

Running it again is safe: an established root is left exactly as it is and the
director already registered there is adopted, so a setup script can run
unconditionally. Pass --new to add a second director to a root that already has
one — that is a real thing to want, and it is a decision rather than something
you should reach by running the same command twice. The workflow binding is
permanent: a director's engagements are validated against its workflow's task
types and progress vocabularies, so switching it later would leave a live fleet
that nothing could describe.

Without a terminal to ask on, or under --json, nothing is guessed: --scope,
--host, a location (--dir, --global or --config) and — for a root that does not
have a director.conf yet — --harness are all required, and setup refuses before
writing anything if one is missing.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ask := setupPrompter()
			flags := siteFlags{dir: dir, global: global}

			// Every question is settled before anything is written. Half a
			// setup — skills placed, then a refusal over a flag the script did
			// not pass — leaves somebody to work out what they now have, and
			// the refusal is cheapest when it is the only thing that happened.
			if err := requireAnswersUpFront(ask, scope, hostNames, harnessName, force, flags); err != nil {
				return err
			}
			chosenScope, err := resolveScope(scope, ask)
			if err != nil {
				return err
			}
			chosenHosts, err := resolveHosts(hostNames, ask)
			if err != nil {
				return err
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			site, ok, err := resolveSite(flags, ask)
			if err != nil {
				return err
			}
			if !ok {
				fmt.Println("No location chosen, so nothing was set up.")
				return nil
			}
			roots := site.roots
			// Project-scope skills belong to the project the workflow is
			// going into, when the location named one. A root named outright
			// says nothing about which project that is, so the skills stay
			// where setup was run.
			skillsRoot := cwd
			if site.project != "" {
				skillsRoot = site.project
			}
			pending, err := pendingStarters(roots.Primary, force)
			if err != nil {
				return err
			}
			// The harness is the one starter value that is a decision rather
			// than a copy. Either way the question gets asked; which branch
			// runs decides whether the answer is written or reported, never
			// whether it is put.
			var effectiveHarness string
			if writesStarterConfig(pending) {
				effectiveHarness, err = resolveHarness(harnessName, ask)
			} else {
				effectiveHarness, err = reviewHarness(roots.Primary, harnessName, ask)
			}
			if err != nil {
				return err
			}

			// Writes, in the order the help text promises them.
			installed, err := installSkills(chosenHosts, chosenScope, skillsRoot, dryRun)
			if err != nil {
				return err
			}
			if !opts.asJSON {
				reportSkills(installed, skillsRoot, dryRun)
			}

			if dryRun {
				return reportDryRun(roots, pending, installed, effectiveHarness, workflow, createNew)
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
					"skills":    installed,
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
				fmt.Printf("\ndirector %s (%s) is already registered here for workflow %q, so setup used it\n",
					state.DirectorID, state.Name, state.Workflow)
				fmt.Printf("Nothing was created. Pass --new to add a second director to this root.\n")
			}
			fmt.Printf("root: %s\n\n", roots.Primary)
			if roots.Layers[0].Kind == director.LayerProject {
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
			fmt.Printf("  2. Then, in an agent conversation, start directing:\n\n")
			fmt.Printf("       director attach\n\n")
			fmt.Printf("     That decides whether to take over a running director or start one,\n")
			fmt.Printf("     and prints the id to use. You do not need to pick one yourself.\n\n")
			fmt.Printf("Commit %s if you want this workflow shared; leave state/ out of version control.\n",
				director.ProjectDirName)
			return nil
		},
	}
	cmd.Flags().StringVar(&scope, "scope", "", "where the skills go: project (under the workflow's directory) | global (default: ask)")
	cmd.Flags().StringSliceVar(&hostNames, "host", nil, "harness to install skills for, repeatable (default: ask)")
	cmd.Flags().StringVar(&dir, "dir", "", "directory to install the workflow into, as <dir>/.director; project-scope skills go under it too (default: ask, suggesting the current directory)")
	cmd.Flags().BoolVar(&global, "global", false, "install the workflow into the user root rather than a project directory")
	cmd.Flags().StringVar(&harnessName, "harness", "", "harness the workflow spawns on, skipping the question (default: ask)")
	cmd.Flags().StringVar(&workflow, "workflow", "", "workflow to bind the director to (default: default)")
	cmd.Flags().StringVar(&name, "name", "", "human label for the director")
	cmd.Flags().BoolVar(&createNew, "new", false, "register another director rather than using the one already registered")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite starter files that already exist")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print what would be written and change nothing")
	return cmd
}

// interactive reports whether setup may put a question at all.
//
// Two situations rule it out. Without a terminal there is nobody at the other
// end of a prompt. Under --json there may be, but the caller is a program
// parsing stdout, and a prompt rendered onto it — or a default chosen on its
// behalf — is exactly the guess a machine cannot see it was given.
func interactive(ask prompter) bool {
	return ask.terminal && !opts.asJSON
}

// requireAnswersUpFront is the non-interactive contract: every answer comes
// from a flag, and a missing one is refused in a single message naming all of
// them, before a question is put or a file is written.
//
// Refusing piecemeal — one flag per run — was the alternative, and it is what
// the individual resolvers still do as a backstop. But a script author fixing
// flags one run at a time is being told the rules one at a time, and the
// harness flag in particular depends on the location, so the only place all
// of them can be named together is here.
func requireAnswersUpFront(ask prompter, scope string, hosts []string, harnessName string, force bool, flags siteFlags) error {
	if interactive(ask) {
		return nil
	}
	var missing []string
	if scope == "" {
		missing = append(missing, "--scope project|global")
	}
	if len(hosts) == 0 {
		missing = append(missing, "--host <harness>            (repeatable; `director skills --path` lists targets)")
	}
	switch named, err := flags.named(); {
	case err != nil:
		return err
	case !named:
		missing = append(missing, "--dir <path>, --global or --config <root>")
		if harnessName == "" {
			missing = append(missing, fmt.Sprintf("--harness <name>            (for a new root; valid: %s)",
				strings.Join(harness.Names(), ", ")))
		}
	case harnessName == "":
		// The location is known, so whether the harness is needed is too: it
		// is only written into a director.conf that does not exist yet.
		site, _, err := resolveSite(flags, ask)
		if err != nil {
			return err
		}
		pending, err := pendingStarters(site.roots.Primary, force)
		if err != nil {
			return err
		}
		if writesStarterConfig(pending) {
			missing = append(missing, fmt.Sprintf("--harness <name>            (valid: %s)",
				strings.Join(harness.Names(), ", ")))
		}
	}
	if len(missing) == 0 {
		return nil
	}
	why := "no terminal to ask on"
	if opts.asJSON {
		why = "--json has nobody to ask"
	}
	return fmt.Errorf("%s, so every answer has to come from a flag. Missing:\n\n  %s",
		why, strings.Join(missing, "\n  "))
}

// siteFlags are the ways of answering the location question without being
// asked it. --config is the third, and is read from the global options.
type siteFlags struct {
	dir    string
	global bool
}

// named reports whether some flag has answered the location question, and
// refuses two answers to it.
func (f siteFlags) named() (bool, error) {
	var given []string
	if opts.config != "" {
		given = append(given, "--config")
	}
	if f.global {
		given = append(given, "--global")
	}
	if f.dir != "" {
		given = append(given, "--dir")
	}
	if len(given) > 1 {
		return true, fmt.Errorf("%s each name where the workflow goes; pass one of them", strings.Join(given, " and "))
	}
	// DIRECTOR_ROOT is a root somebody named, whether in a setup script or in
	// the environment director injects into the agents it spawns. Resolution
	// puts it above the walk up and so does this, so that a root reached by
	// exporting one variable is the same root every other command would use.
	return len(given) == 1 || os.Getenv(director.EnvRoot) != "", nil
}

// site is the answer to the location question.
type site struct {
	roots director.Roots
	// project is the directory the workflow's root sits in, when the answer
	// was a project directory: --dir, or the path typed or accepted at the
	// question. It is where project-scope skills go. Empty when the answer
	// named a root outright — --global, --config or DIRECTOR_ROOT — because a
	// root says nothing about which project's skills these are.
	project string
}

// resolveSite decides where `director setup` installs the workflow.
//
// Unlike every other command, setup creates rather than resolves, and one part
// of resolution does not survive that change: the walk up. A command run in a
// subdirectory should find its project, and for reading that is right. For
// creating it is a trap — a plain setup in a subdirectory or in a sibling
// worktree would register a director in whichever fleet happened to be above
// it, and the root it landed in would then be left with two directors of the
// same name, which makes every later command refuse to run.
//
// So nothing here walks up on its own. Everything that names a root outright
// is honoured, in the same order ResolveRoots uses: --config or --global, then
// --dir, then DIRECTOR_ROOT. Otherwise the person is asked, with the current
// directory suggested — and a root that exists above the directory they choose
// is pointed out before a second one is created beneath it.
//
// The bool is whether a location was chosen at all: backing out of the
// question is a normal outcome rather than a failure.
func resolveSite(flags siteFlags, ask prompter) (site, bool, error) {
	if _, err := flags.named(); err != nil {
		return site{}, false, err
	}
	wd, err := os.Getwd()
	if err != nil {
		return site{}, false, err
	}
	switch {
	case opts.config != "":
		roots, err := director.ResolveRoots(opts.config, wd)
		return site{roots: roots}, true, err
	case flags.global:
		user := director.UserRoot()
		if user == "" {
			return site{}, false, errors.New("cannot work out the user root: neither $XDG_CONFIG_HOME nor a home directory is set")
		}
		roots, err := director.ResolveRoots(user, wd)
		return site{roots: roots}, true, err
	case flags.dir != "":
		here, err := filepath.Abs(flags.dir)
		if err != nil {
			return site{}, false, err
		}
		roots, err := director.ResolveRoots(filepath.Join(here, director.ProjectDirName), wd)
		return site{roots: roots, project: here}, true, err
	case os.Getenv(director.EnvRoot) != "":
		roots, err := director.ResolveRoots("", wd)
		return site{roots: roots}, true, err
	}

	if opts.asJSON {
		return site{}, false, errors.New("no location chosen, and --json has nobody to ask: pass --dir <path>, --global or --config <root>")
	}
	if !ask.terminal {
		return site{}, false, errors.New("no location chosen and no terminal to ask on: pass --dir <path>, --global or --config <root>")
	}
	root, err := askSite(ask, wd)
	if err != nil || root == "" {
		return site{}, false, err
	}
	roots, err := director.ResolveRoots(root, wd)
	return site{roots: roots, project: filepath.Dir(root)}, true, err
}

// askSite explains what is about to be installed, then asks where.
//
// The current directory is the suggestion, filled in rather than merely
// described, so that Enter accepts it and anything else replaces it. A path
// typed relative is relative to here. A directory that does not exist yet is
// said back and confirmed before anything is created under it, because a typo
// in a path is otherwise a root in a place nobody will look for one.
//
// Declining either confirmation backs out of the whole command rather than
// asking again. Asking again reads better on a terminal, but off one — a test
// driving line mode, or a pipe that has run dry — every re-ask gets the same
// answer and the loop never ends. Returns "" when the person backs out.
func askSite(ask prompter, wd string) (string, error) {
	_, _ = fmt.Fprint(ask.out, workflowExplanation)
	typed, ok, err := ask.input(
		fmt.Sprintf("Where should the workflow be installed? (Enter for %s)", wd),
		"A .director/ directory is created inside it. Type another path, relative to here, to use that instead.",
		wd)
	if err != nil || !ok {
		return "", err
	}
	dir := typed
	if dir == "" {
		dir = wd
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(wd, dir)
	}
	dir = filepath.Clean(dir)
	root := filepath.Join(dir, director.ProjectDirName)

	// A root already there is the ordinary "add to the project I named";
	// nothing further to confirm.
	if info, statErr := os.Stat(root); statErr == nil && info.IsDir() {
		return root, nil
	}

	switch info, statErr := os.Stat(dir); {
	case statErr == nil && !info.IsDir():
		return "", fmt.Errorf("%s is a file, not a directory", dir)
	case statErr != nil:
		create, err := ask.confirm(
			fmt.Sprintf("%s does not exist. Create it?", dir),
			fmt.Sprintf("The workflow would be installed in %s.", root),
			"Create it", "Cancel", true)
		if err != nil || !create {
			return "", err
		}
	}

	// A root above is what the silent walk up would have found. It is
	// pointed out rather than adopted or refused: adding a director to it is
	// one answer and a separate root here is another, and only the person
	// knows which they meant.
	if found, ok := rootAbove(dir); ok {
		separate, err := ask.confirm(
			fmt.Sprintf("A configuration root already exists above this directory, at %s. Create a separate one here anyway?", found),
			"A director registered here is invisible to the fleet above, and the other way round. To add to the root above instead, cancel and run setup again with --dir pointing at its directory.",
			"Create a separate root", "Cancel", false)
		if err != nil || !separate {
			return "", err
		}
	}
	return root, nil
}

// rootAbove is the project root the walk up from dir would land in, if any —
// not counting one in dir itself, which the caller has already ruled out.
func rootAbove(dir string) (string, bool) {
	found, err := director.ResolveRoots("", dir)
	if err != nil || found.Layers[0].Kind != director.LayerProject {
		return "", false
	}
	if found.Primary == filepath.Join(dir, director.ProjectDirName) {
		return "", false
	}
	return found.Primary, true
}

// reportDryRun says what the workflow half would have done, without doing it.
//
// The skills half has already reported its own would-writes. What is left is
// the root: which starters would land, and whether a director would be created
// or the one already there adopted. Register is not consulted, because it
// writes; the same rule it applies is stated here instead.
func reportDryRun(roots director.Roots, pending []starterFile, installed []skillInstall, chosenHarness, workflow string, createNew bool) error {
	if workflow == "" {
		workflow = director.DefaultWorkflow
	}
	existing, _ := director.ListDirectors(roots.Primary)
	action, subject := "would-create", ""
	switch {
	case createNew || len(existing) == 0:
	case len(existing) == 1:
		action, subject = "would-adopt", existing[0].DirectorID
	default:
		action = "would-refuse"
	}

	wrote := make([]string, 0, len(pending))
	for _, file := range pending {
		wrote = append(wrote, file.target)
	}
	if opts.asJSON {
		return emit(map[string]any{
			"dry_run":   true,
			"skills":    installed,
			"director":  subject,
			"workflow":  workflow,
			"root":      roots.Primary,
			"harness":   chosenHarness,
			"action":    action,
			"directors": len(existing),
			"ambiguous": len(existing) > 1,
			"wrote":     wrote,
		})
	}
	for _, path := range wrote {
		fmt.Printf("would write %s\n", path)
	}
	switch action {
	case "would-create":
		fmt.Printf("\nwould register a director bound to workflow %q under %s\n", workflow, roots.Primary)
	case "would-adopt":
		fmt.Printf("\nwould use director %s (%s), already registered under %s; nothing would be created\n",
			existing[0].DirectorID, existing[0].Name, roots.Primary)
	default:
		fmt.Printf("\nwould refuse: %d directors are already registered under %s and setup will not pick between them\n",
			len(existing), roots.Primary)
	}
	return nil
}

// starterConfigPath is the one starter whose contents are decided at setup
// time rather than copied verbatim.
const starterConfigPath = "starters/director.conf"

// harnessPlaceholder is what the chosen harness is substituted for.
const harnessPlaceholder = "{{harness}}"

// starterFile is one starter this setup will write: where it comes from in
// the embedded tree, and where it lands.
type starterFile struct {
	source string
	target string
}

// pendingStarters works out which starters this setup will write, without
// writing any of them.
//
// Deciding first is what lets setup ask its questions before it touches the
// disk, and it keeps the skip rules in one place rather than in one function
// that decides and another that guesses the same thing again.
//
// Only a root that has no workflows yet gets starters, unless forced. Skipping
// files that already exist is not enough: a starter the user deliberately
// deleted would come back on the next setup, quietly reintroducing a workflow
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

// askHarness puts the question, through the same prompter the skills half
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
		description = "Its director.conf was written by hand, so setup will not rewrite it. Answering says what belongs in it."
	}
	return ask.selectOne(title, description, options)
}

// reviewHarness is the harness question for a root that already has a
// director.conf.
//
// The answer is not applied, and that is deliberate: the file is hand-edited
// and commented, and setup reaching into it to change a key would be setup
// editing somebody's configuration behind them. But it is still asked, and
// still answered in full. Saying nothing is what let a director be registered
// against a harness nobody had picked, which is the whole complaint; declining
// to write is a different thing from declining to speak.
//
// Returns the harness the root actually spawns on, which is what it spawned on
// before setup ran.
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
		fmt.Printf("%s is yours, not setup's, so it was left as it is.\n", path)
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
				fmt.Println("no workflows found; run `director setup` to create the starters")
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
				fmt.Printf("no directors under %s; run `director setup`\n", roots.Primary)
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
you run inside it, and ` + "`director setup`" + ` names the harnesses that are
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
				// setup` will not accept.
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
