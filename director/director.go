// Package director is agent-director's core: the part that knows about
// directors, engagements, workflows, permissions, and health.
//
// Everything a front-end could want lives here, and nothing a front-end owns
// does. There is no cobra in this package, no stdout, no exit codes and no
// terminal. That is deliberate and load-bearing: a monitoring daemon, a TUI, an
// HTTP API or an MCP server should each be a peer of the CLI rather than
// something that has to shell out to it and parse its output. The rule to hold
// the line is that cli/ may not contain a decision — if a behaviour would be
// identical for a TUI, it belongs in here.
//
// # What is authoritative for what
//
// The single organising rule. Bindings, cursors, notes, reported progress and
// outstanding questions live in the director's state file, because nothing else
// knows them. Lifecycle, liveness and activity come from the harness on every
// call and are never cached, because a stored lifecycle is wrong the moment a
// process exits. Health is derived from both plus the clock and is likewise
// never stored, because it depends on now — a persisted "ok" goes stale purely
// through time passing, which is the one way a staleness detector can fail
// while continuing to look healthy.
package director

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rwmyers/agent-director/harness"
	"github.com/rwmyers/agent-director/harness/herdr"
	"github.com/rwmyers/agent-director/internal/conf"
)

// repairAdvice is what to do about a state file nothing can read. It is said
// wherever that is discovered, because the alternative somebody reaches for is
// a text editor on a live director's state file.
const repairAdvice = "Run `director repair` to see what is wrong, then `director repair --write` to fix it."

// Director is one registered chief of staff and the fleet it owns.
type Director struct {
	Roots    Roots
	Config   *Config
	Workflow *Workflow
	State    *State

	// Clock is the source of now. Health is a function of elapsed time, so
	// tests move the clock instead of sleeping.
	Clock Clock

	// Lookup resolves a harness name to an adapter. Injectable so the core can
	// be tested against fakes without touching the global registry.
	Lookup func(name string) (harness.Adapter, error)

	// InPane reports the herdr pane this director process is itself running in,
	// when it is running in one. It is the one placement input that comes from
	// the environment, and is injectable for the same reason Lookup is: a test
	// must be able to state where the director is sitting without a herdr
	// installed and without editing the environment the test itself runs in.
	InPane func() (paneID string, ok bool)
}

// ErrNotFound is returned when an identifier names nothing. Callers map it to
// a distinct exit code so a script can tell "no such engagement" from "the
// command failed".
var ErrNotFound = errors.New("not found")

// StartupGrace is how long after a spawn a harness may have no record of a
// conversation before the spawn is judged to have failed.
//
// It has to be generous. An agent that has been asked for but has not yet
// written anything is indistinguishable from one that never started, and
// calling a slow start a failure would have the director abandon healthy work
// and spawn a duplicate. Waiting too long only delays a verdict on something
// already broken.
const StartupGrace = 45 * time.Second

// Init registers a new director in a root, bound to a workflow.
//
// The binding is permanent. Switching a live director's workflow would leave
// its in-flight engagements referring to task types that might no longer exist,
// and the progress vocabulary they were validated against would be gone — so
// the fleet would still be running while nothing could describe it.
func Init(roots Roots, workflowName, name string, clock Clock) (*State, error) {
	if clock == nil {
		clock = SystemClock
	}
	workflow, err := roots.FindWorkflow(workflowName)
	if err != nil {
		return nil, err
	}

	id, err := newID(directorPrefix)
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = workflow.Name
	}
	existing, _ := ListDirectors(roots.Primary)
	name = uniqueName(existing, name)

	state := &State{
		Path:        statePath(roots.Primary, id),
		DirectorID:  id,
		Name:        name,
		Workflow:    workflow.Name,
		CreatedAt:   clock(),
		Engagements: map[string]*Engagement{},
		Asks:        map[string]*Ask{},
	}
	if err := state.Save(); err != nil {
		return nil, err
	}
	return state, nil
}

// DefaultWorkflow is the workflow a director binds to when nobody says which.
// It is the name of the starter `director init` writes.
const DefaultWorkflow = "default"

// Register is what setting up a root does about directors: adopt the one
// already registered there, or create one.
//
// Repeating init has to be safe. It is the first command anybody runs, the one
// a setup script runs unconditionally, and the one an agent reaches for when
// something else complained there was no director — so it gets run twice far
// more often than it gets run once. Creating another director every time is
// what turned that into an outage: a root ends up holding two directors with
// the same auto-generated name, nothing distinguishes them, and every later
// command refuses to guess between them. Running the same setup command a
// second time should not be able to break the first.
//
// So the default is idempotent, and a second director is asked for rather than
// arrived at. Wanting two is legitimate — two fleets, or two conversations that
// must not share a read cursor — but it is a decision, and createNew is where
// somebody makes it. That is the same shape `Attach` already has, where --new
// means start another and the absence of it means use what is here.
//
// Adoption is refused rather than guessed when a flag disagrees with the
// director that is already here, because the alternative is init reporting
// success while quietly ignoring what it was told.
func Register(roots Roots, workflowName, name string, createNew bool, clock Clock) (*State, bool, error) {
	// An unreadable record is somebody else's problem to repair, and it is not
	// a reason to refuse to set up: it cannot be adopted, so it does not count
	// as something to adopt.
	existing, _ := ListDirectors(roots.Primary)

	if createNew || len(existing) == 0 {
		if workflowName == "" {
			workflowName = DefaultWorkflow
		}
		state, err := Init(roots, workflowName, name, clock)
		return state, true, err
	}

	if len(existing) > 1 {
		var labels []string
		for _, candidate := range existing {
			labels = append(labels, fmt.Sprintf("  %s (%s), workflow %q", candidate.DirectorID, candidate.Name, candidate.Workflow))
		}
		return nil, false, fmt.Errorf(`%d directors are already registered under %s:

%s

init will not pick between them, and adding a third would not help. Act as one
of them by exporting its id:

    export %s=%s

or retire the ones you do not want (`+"`director retire <id>`"+`), or say outright
that you want another:

    director init --new --name <label>`,
			len(existing), roots.Primary, strings.Join(labels, "\n"), EnvID, existing[0].DirectorID)
	}

	adopted := existing[0]
	if name != "" && name != adopted.Name {
		return nil, false, fmt.Errorf(`director %s (%s) is already registered under %s, and you asked for one named %q.

init adopts the director that is here rather than renaming it. Ask outright for
a second one if that is what you meant:

    director init --new --name %s`,
			adopted.DirectorID, adopted.Name, roots.Primary, name, name)
	}
	if workflowName != "" && workflowName != adopted.Workflow {
		return nil, false, fmt.Errorf(`director %s (%s) is already registered under %s and is bound to workflow %q, not %q.

A director's workflow is permanent: its engagements are validated against that
workflow's task types, so rebinding one would leave a live fleet that nothing
could describe. A director on %q is a different director:

    director init --new --name <label> --workflow %s`,
			adopted.DirectorID, adopted.Name, roots.Primary, adopted.Workflow, workflowName, workflowName, workflowName)
	}
	return adopted, false, nil
}

// uniqueName keeps two directors in one root from answering to the same label.
//
// The name is the only part of a director anybody reads: ids are random hex,
// and `director directors` is otherwise a list of them. Two rows both called
// "default" — which is exactly what a root looked like after init had been run
// twice — is a list nobody can act on, and the ambiguity error that follows
// names them both without helping. So a label already in use here is suffixed
// rather than handed out again.
//
// It suffixes rather than refuses because the callers that create are the ones
// that mean to. `Attach --new` gets a second director because a second
// conversation asked for its own, and failing that request over a name nobody
// chose would be worse than answering it with a name they can tell apart.
func uniqueName(existing []*State, want string) string {
	taken := make(map[string]bool, len(existing))
	for _, state := range existing {
		taken[state.Name] = true
	}
	if !taken[want] {
		return want
	}
	for n := 2; ; n++ {
		candidate := fmt.Sprintf("%s-%d", want, n)
		if !taken[candidate] {
			return candidate
		}
	}
}

// Open loads a director by identifier.
//
// Selection is explicit id, then DIRECTOR_ID, then the only director in the
// root when there is exactly one. It never guesses between two — picking the
// wrong director would silently operate on somebody else's fleet, and the
// mistake would not be visible in the output.
func Open(roots Roots, id string, clock Clock) (*Director, error) {
	if clock == nil {
		clock = SystemClock
	}
	if id == "" {
		id = os.Getenv(EnvID)
	}

	var state *State
	if id != "" {
		loaded, err := LoadState(statePath(roots.Primary, id))
		if err != nil {
			var parseErr *conf.ParseError
			if errors.As(err, &parseErr) {
				// The record exists and cannot be read. Say where the damage is
				// and how to undo it; the alternative is somebody editing a
				// state file by hand while its fleet keeps running.
				return nil, fmt.Errorf("director %s cannot be read:\n  %v\n\n%s", id, err, repairAdvice)
			}
			if errors.Is(err, os.ErrNotExist) {
				// Almost always a stale DIRECTOR_ID: a shell outlives the
				// state file it was told about, or the root moved. Saying only
				// "not found" leaves somebody staring at an id they exported
				// themselves and believe in. Name what does exist, and the one
				// command that fixes it.
				return nil, fmt.Errorf("%w: no director %q under %s\n%s",
					ErrNotFound, id, roots.Primary, alternatives(roots, id))
			}
			return nil, err
		}
		state = loaded
	} else {
		// An unreadable state file costs its own director, not this command: the
		// whole point of one file per director is that a bad line in one of them
		// does not stop work on the others.
		states, unreadable := ListDirectors(roots.Primary)
		if len(states) == 0 && unreadable != nil {
			return nil, fmt.Errorf("no director under %s can be read:\n  %v\n\n%s",
				roots.Primary, unreadable, repairAdvice)
		}
		switch len(states) {
		case 0:
			return nil, fmt.Errorf("%w: no directors under %s — run `director init` first", ErrNotFound, roots.Primary)
		case 1:
			state = states[0]
		default:
			var labels []string
			for _, candidate := range states {
				labels = append(labels, fmt.Sprintf("%s (%s)", candidate.DirectorID, candidate.Name))
			}
			return nil, fmt.Errorf("more than one director under %s; pass --director or set %s: %s",
				roots.Primary, EnvID, strings.Join(labels, ", "))
		}
	}

	config, err := LoadConfig(roots.Primary)
	if err != nil {
		return nil, err
	}
	workflow, err := roots.FindWorkflow(state.Workflow)
	if err != nil {
		return nil, fmt.Errorf("director %s is bound to workflow %q: %w", state.DirectorID, state.Workflow, err)
	}

	return &Director{
		Roots:    roots,
		Config:   config,
		Workflow: workflow,
		State:    state,
		Clock:    clock,
		Lookup:   harness.Lookup,
		InPane:   herdr.InPane,
	}, nil
}

// placement is a resolved harness and, when the environment rather than the
// configuration chose it, what to say about that.
type placement struct {
	adapter harness.Adapter
	// note explains a placement nothing written down accounts for. Empty unless
	// detection moved the engagement off what the configuration would have used.
	note string
}

// adapterFor resolves which harness an engagement runs on: the caller's
// override, then the task's or workflow's pin, then the herdr pane this
// director is itself running in, then the root's configured default.
//
// Detection sits above the configured default and below everything else, and
// the two halves of that are separate decisions. It beats `harness` in
// director.conf because that key is a standing preference for a project rather
// than a judgement about this spawn, and a director working inside a pane
// nearly always wants its fleet in panes beside it — placed below the config
// key, on any root that sets one, the whole thing would never fire. It loses to
// a --harness flag and to a workflow pin because those are somebody choosing,
// for this piece of work, and an ambient signal must never overrule a choice.
func (d *Director) adapterFor(task Task, override string) (placement, error) {
	name := override
	if name == "" {
		name = d.Workflow.HarnessFor(task)
	}
	if name == "" {
		if detected, ok := d.detectPlacement(); ok {
			return detected, nil
		}
		name = d.Config.Harness
	}
	if name == "" {
		return placement{}, fmt.Errorf("no harness chosen for task %q: pin one in the task or workflow, set `harness` in director.conf, or pass --harness", task.Name)
	}
	lookup := d.Lookup
	if lookup == nil {
		lookup = harness.Lookup
	}
	adapter, err := lookup(name)
	if err != nil {
		return placement{}, err
	}
	return placement{adapter: adapter}, nil
}

// detectPlacement resolves the harness this director's own surroundings imply:
// a director running in a herdr pane spawns into herdr panes.
//
// It declines rather than failing when the herdr adapter is not in this binary,
// because detection is ambient. Something nobody asked for must not be able to
// break a spawn that the configuration alone would have completed — the cost of
// declining is an engagement in the configured harness, which is exactly what
// would have happened anyway.
func (d *Director) detectPlacement() (placement, bool) {
	if !d.Config.HerdrAutodetect {
		return placement{}, false
	}
	inPane := d.InPane
	if inPane == nil {
		inPane = herdr.InPane
	}
	paneID, ok := inPane()
	if !ok {
		return placement{}, false
	}
	lookup := d.Lookup
	if lookup == nil {
		lookup = harness.Lookup
	}
	adapter, err := lookup(herdr.Name)
	if err != nil {
		return placement{}, false
	}
	return placement{adapter: adapter, note: paneNote(paneID, d.Config.Harness)}, true
}

// paneNote is what the person is told when the pane, not the configuration,
// decided where an engagement went.
//
// Silent when the configuration would have chosen herdr anyway: nothing was
// changed, and reporting a difference that does not exist trains people to stop
// reading the line that matters. It names the switch, because somebody reading
// this is being told their configuration was overridden and the next thing they
// will want is the way to stop it.
func paneNote(paneID, configured string) string {
	if configured == herdr.Name {
		return ""
	}
	where := "a herdr pane"
	if paneID != "" {
		where = "herdr pane " + paneID
	}
	if configured == "" {
		return fmt.Sprintf("placed on %s: this director is running in %s", herdr.Name, where)
	}
	return fmt.Sprintf("placed on %s rather than the configured %s: this director is running in %s (set `herdr_autodetect = false` in director.conf to stop)",
		herdr.Name, configured, where)
}

// SpawnOptions is a request to delegate work.
type SpawnOptions struct {
	Task  string
	Title string
	// Name is how the harness should label this conversation in its own
	// interface. Optional; derived from the title when empty.
	Name    string
	Brief   string
	Harness string
}

// Spawn delegates a piece of work to a fresh agent conversation.
//
// The binding is written before the adapter is called, with an empty Ref. A
// crash between the two therefore leaves a recoverable record that `status`
// reports as an orphan, rather than an agent process running somewhere with
// nobody's name on it. Writing afterwards would make that case invisible.
func (d *Director) Spawn(ctx context.Context, opts SpawnOptions) (*Engagement, error) {
	task, err := d.Workflow.Task(opts.Task)
	if err != nil {
		return nil, err
	}
	permission, err := d.Workflow.PermissionFor(task)
	if err != nil {
		return nil, err
	}
	place, err := d.adapterFor(task, opts.Harness)
	if err != nil {
		return nil, err
	}
	adapter := place.adapter

	// Refuse before doing anything if the harness cannot hold the line the
	// workflow asked for. Running with more access than requested and warning
	// about it would be worse than failing, because the warning gets lost and
	// the access does not.
	if err := adapter.Permits(permission.Allow); err != nil {
		return nil, fmt.Errorf("task %q requires permission scope %q: %w", task.Name, permission.Name, err)
	}

	// Where the agent starts is not a caller's to choose. It is this process's
	// working directory, and nothing else.
	//
	// The harness sandboxes an agent to the directory it is started in, so any
	// directory named from outside can be one the agent cannot work in: a path
	// that does not exist yet, or that sits below the workspace instead of
	// above it, locks the agent out of the only directory it exists to work in
	// — reported to it as missing files, and pointing at nothing. A directory
	// this process is already running in cannot be that: it exists, and
	// whatever the director can reach from it, the agent can too.
	//
	// The cost is that the answer moves with the shell, silently, and nothing
	// in the command records it. Every front end is therefore expected to say
	// where it put the agent; Dir is stored on the engagement so it can.
	dir, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	id, err := newID(engagementPrefix)
	if err != nil {
		return nil, err
	}
	token, err := newToken()
	if err != nil {
		return nil, err
	}

	title := opts.Title
	if title == "" {
		title = summarise(opts.Brief)
	}
	name := opts.Name
	if name == "" {
		name = displayName(title, id)
	}

	engagement := &Engagement{
		ID:        id,
		Director:  d.State.DirectorID,
		Harness:   adapter.Name(),
		Task:      task.Name,
		Title:     title,
		Name:      name,
		Dir:       dir,
		StartedAt: d.now(),
		Token:     token,
		Detail:    map[string]string{},
	}
	// Recorded rather than printed, because the core has no stdout. It is stored
	// with the engagement so the reason survives the command that caused it: an
	// engagement found in a pane a week later can still say why it is there.
	if place.note != "" {
		engagement.Detail["placement"] = place.note
	}

	if err := d.mutate(func(state *State) error {
		state.Engagements[id] = engagement
		return nil
	}); err != nil {
		return nil, err
	}

	result, spawnErr := adapter.Spawn(ctx, harness.SpawnRequest{
		ID:     id,
		Dir:    dir,
		Title:  title,
		Name:   name,
		Prompt: d.composePrompt(task, opts.Brief),
		Allow:  permission.Allow,
		Env:    d.agentEnv(engagement, task),
	})
	if spawnErr != nil {
		// Take the record back out. It was written before the call so that a
		// crash between the two leaves something recoverable, and that is the
		// only case it is for: a returned error means the adapter got far
		// enough to report, and an adapter that reports a failed spawn is
		// responsible for what it created on the way to failing.
		//
		// Keeping it costs more than it saves. The record's Ref is empty, so it
		// names nothing a director can read, stop or resume — it can only be
		// removed by hand, and until it is, it sits in `status` as a stalled
		// engagement of unknown lifecycle, which is what a genuinely lost agent
		// looks like. One failed spawn should not make the fleet unreadable.
		//
		// The error is returned rather than recorded because the caller is
		// standing right there: nobody has to go and look up why.
		if removeErr := d.mutate(func(state *State) error {
			delete(state.Engagements, id)
			return nil
		}); removeErr != nil {
			return nil, fmt.Errorf("spawning on %s: %w (and clearing the record failed: %v)",
				adapter.Name(), spawnErr, removeErr)
		}
		return nil, fmt.Errorf("spawning on %s: %w", adapter.Name(), spawnErr)
	}

	if err := d.mutate(func(state *State) error {
		held, ok := state.Engagements[id]
		if !ok {
			return fmt.Errorf("%w: engagement %s vanished during spawn", ErrNotFound, id)
		}
		held.Ref = result.Ref
		for key, value := range result.Detail {
			held.Detail[key] = value
		}
		return nil
	}); err != nil {
		return nil, err
	}

	return d.Get(ctx, id)
}

// agentEnv is what a spawned agent needs in order to talk back. Without it the
// agent has no way to report progress or raise a question, and the whole
// callback channel is dead.
//
// PATH is extended with this executable's own directory. The agent is told to
// run `director`, and requiring that it already be installed somewhere on the
// user's PATH would make the callback channel depend on how the binary happened
// to be deployed — a distinction the agent cannot see and could not fix.
//
// It also takes one thing away. An adapter that starts a process hands it this
// director's environment, so a director sitting in a herdr pane would otherwise
// give every agent it spawns its own pane identity. The agent would then run
// `director` and conclude it was the process occupying that pane — reporting
// somebody else's pane as its own, and placing anything it spawned into a pane
// already busy with the director that spawned it. Emptied rather than omitted,
// because an inherited variable can only be overridden. The herdr adapter drops
// these entirely on the way past, so an agent that really is given its own pane
// gets its identity from herdr and detection stays correct for it.
func (d *Director) agentEnv(engagement *Engagement, task Task) map[string]string {
	env := map[string]string{
		EnvRoot:         d.Roots.Primary,
		EnvID:           d.State.DirectorID,
		EnvEngagement:   engagement.ID,
		EnvToken:        engagement.Token,
		EnvTask:         task.Name,
		EnvProgress:     strings.Join(task.Progress, ","),
		herdr.EnvInPane: "",
		herdr.EnvPane:   "",
	}
	if self, err := os.Executable(); err == nil {
		env[EnvBin] = self
		env["PATH"] = filepath.Dir(self) + string(os.PathListSeparator) + os.Getenv("PATH")
	}
	return env
}

// composePrompt assembles what the agent is actually told: the task's own
// prompt, the reporting contract rendered as prose, and the director's brief.
//
// The contract has to be rendered here rather than left to the task author,
// because it is the only thing that tells the agent the callback API exists at
// all. A task prompt that forgot to mention it would produce an agent that
// silently never reports, and the failure would look like a wedged agent.
func (d *Director) composePrompt(task Task, brief string) string {
	var out strings.Builder
	if task.Prompt != "" {
		out.WriteString(strings.TrimRight(task.Prompt, "\n"))
		out.WriteString("\n\n")
	}
	out.WriteString(renderReportingContract(task))
	out.WriteString("\n\n## Your assignment\n\n")
	out.WriteString(strings.TrimSpace(brief))
	out.WriteString("\n")
	return out.String()
}

func renderReportingContract(task Task) string {
	var out strings.Builder
	out.WriteString("## Reporting back\n\n")
	out.WriteString("You are running as an engagement for a director, which is another agent coordinating several pieces of work. ")
	out.WriteString("It cannot see your conversation. The only thing it knows about you is what you report.\n\n")

	if len(task.Progress) > 0 {
		fmt.Fprintf(&out, "Report progress by running:\n\n    director report --progress <value> --message \"<one line>\"\n\nValid values, in order: %s\n\n",
			strings.Join(task.Progress, ", "))
	}

	switch {
	case task.ReportOn.Never:
		out.WriteString("You are not required to report on a schedule.\n\n")
	default:
		var when []string
		if task.ReportOn.OnProgressChange {
			when = append(when, "every time your progress changes")
		}
		if task.ReportOn.Every > 0 {
			when = append(when, fmt.Sprintf("at least every %s", task.ReportOn.Every))
		}
		if len(when) > 0 {
			out.WriteString("Report " + strings.Join(when, ", and ") + ".\n\n")
		}
	}

	if task.Terminal != "" {
		fmt.Fprintf(&out, "When the work is finished, report %q as your final progress value. ", task.Terminal)
		out.WriteString("The director cannot otherwise tell a finished engagement from one that stopped early.\n\n")
	}

	out.WriteString("If you need a decision only a human or the director can make, ask instead of guessing:\n\n")
	out.WriteString("    ANSWER=$(director ask \"<your question>\" --wait)\n\n")
	out.WriteString("This blocks until you are answered, and marks you as blocked so somebody knows to look. ")
	out.WriteString("Do not silently pick an option when the choice is not yours to make.\n")
	return out.String()
}

// displayName derives a harness-facing label when the caller gave none.
//
// It falls back to the title rather than leaving the field empty, because an
// empty name is not neutral: harnesses derive one from the working directory,
// so three engagements in one repository would all be labelled the same and the
// display would be worse than useless. Kept short, since it lands in a prompt
// box and a terminal title rather than a table.
func displayName(title, id string) string {
	const limit = 32
	trimmed := strings.TrimSpace(title)
	if trimmed == "" {
		return id
	}
	if len(trimmed) <= limit {
		return trimmed
	}
	return strings.TrimSpace(trimmed[:limit])
}

func summarise(brief string) string {
	trimmed := strings.TrimSpace(strings.SplitN(brief, "\n", 2)[0])
	const limit = 60
	if len(trimmed) <= limit {
		return trimmed
	}
	return strings.TrimSpace(trimmed[:limit]) + "…"
}

// Get returns one engagement with lifecycle and health freshly derived.
func (d *Director) Get(ctx context.Context, id string) (*Engagement, error) {
	engagement, ok := d.State.Engagements[id]
	if !ok {
		return nil, fmt.Errorf("%w: no engagement %q", ErrNotFound, id)
	}
	d.observe(ctx, engagement)
	return engagement, nil
}

// Status returns the whole fleet, freshly observed.
func (d *Director) Status(ctx context.Context) ([]*Engagement, error) {
	ids := d.State.EngagementIDs()
	engagements := make([]*Engagement, 0, len(ids))
	for _, id := range ids {
		engagement := d.State.Engagements[id]
		d.observe(ctx, engagement)
		engagements = append(engagements, engagement)
	}
	sort.Slice(engagements, func(i, j int) bool {
		return engagements[i].StartedAt.Before(engagements[j].StartedAt)
	})
	return engagements, nil
}

// observe fills in the fields the harness owns and then derives health.
//
// An adapter that errors does not produce a lifecycle of "unknown": unknown
// means the harness answered and does not know, while an error means it did not
// answer at all. Conflating them would make a stopped herdr server look like a
// fleet of confused agents, and the director would wait on them forever.
func (d *Director) observe(ctx context.Context, engagement *Engagement) {
	engagement.PendingAsk = d.State.PendingAskFor(engagement.ID)

	if engagement.Ref == "" {
		engagement.Lifecycle = harness.LifecycleUnknown
		engagement.Detail["orphan"] = "spawn did not complete"
		engagement.Health = HealthStalled
		return
	}

	lookup := d.Lookup
	if lookup == nil {
		lookup = harness.Lookup
	}
	adapter, err := lookup(engagement.Harness)
	if err != nil {
		engagement.Lifecycle = harness.LifecycleUnknown
		engagement.Detail["harness_error"] = err.Error()
		engagement.Health = HealthUnknown
		return
	}

	observation, err := adapter.Get(ctx, engagement.Ref)
	if err != nil {
		engagement.Lifecycle = harness.LifecycleUnknown
		engagement.Detail["harness_error"] = err.Error()
		engagement.Health = HealthUnknown
		return
	}
	delete(engagement.Detail, "harness_error")

	if !observation.Found {
		// The harness has no record of this conversation. Only the core can
		// tell what that means, because only the core knows when the spawn
		// happened: a conversation that started two seconds ago has simply not
		// come up yet, while one that started an hour ago and left no trace
		// never came up at all. An adapter cannot distinguish those, which is
		// why it reports not-found and declines to interpret it.
		if d.now().Sub(engagement.StartedAt) < StartupGrace {
			engagement.Lifecycle = harness.LifecycleStarting
		} else {
			engagement.Lifecycle = harness.LifecycleDone
			engagement.Detail["spawn_failed"] = "the harness never recorded this conversation; check the spawn log"
		}
	} else {
		delete(engagement.Detail, "spawn_failed")
		engagement.Lifecycle = observation.Lifecycle.Normalize()
		engagement.LastActivityAt = observation.LastActivityAt
		for key, value := range observation.Detail {
			engagement.Detail[key] = value
		}
	}

	task, err := d.Workflow.Task(engagement.Task)
	if err != nil {
		// The task was removed from the workflow after this engagement was
		// spawned. Report what the harness sees and decline to judge, rather
		// than judging against a contract that no longer exists.
		engagement.Health = HealthUnknown
		return
	}

	engagement.Health = deriveHealth(healthInput{
		lifecycle:      engagement.Lifecycle,
		progress:       engagement.Progress,
		task:           task,
		hasPendingAsk:  engagement.PendingAsk != nil,
		lastReportAt:   engagement.LastReportAt,
		lastActivityAt: engagement.LastActivityAt,
		startedAt:      engagement.StartedAt,
		now:            d.now(),
	})
}

// Report records what an agent says about itself.
//
// The token is checked against the named engagement, so an agent can only speak
// for itself. Without that, one confused agent could move a sibling's progress
// or answer for it, and nothing in the output would reveal that it had happened.
func (d *Director) Report(engagementID, token, progress, message string) (*Engagement, error) {
	var updated *Engagement
	err := d.mutate(func(state *State) error {
		engagement, err := authenticate(state, engagementID, token)
		if err != nil {
			return err
		}
		task, err := d.Workflow.Task(engagement.Task)
		if err != nil {
			return err
		}
		if progress != "" {
			if !task.ValidProgress(progress) {
				return fmt.Errorf("task %q has no progress value %q (valid: %s)",
					task.Name, progress, strings.Join(task.Progress, ", "))
			}
			engagement.Progress = progress
		}
		if message != "" {
			engagement.LastMessage = message
		}
		engagement.LastReportAt = d.now()
		// A report is a sign of life, so it clears any previous nudge: the
		// director should be told "stalled and untried" the next time this
		// engagement goes quiet, not "stalled and already poked".
		engagement.NudgedAt = time.Time{}
		updated = engagement
		return nil
	})
	return updated, err
}

// Ask records a question from an agent and blocks it until somebody answers.
func (d *Director) Ask(engagementID, token, question string) (*Ask, error) {
	if strings.TrimSpace(question) == "" {
		return nil, errors.New("a question is required")
	}
	id, err := newID(askPrefix)
	if err != nil {
		return nil, err
	}

	var created *Ask
	err = d.mutate(func(state *State) error {
		if _, err := authenticate(state, engagementID, token); err != nil {
			return err
		}
		created = &Ask{
			ID:         id,
			Engagement: engagementID,
			Question:   strings.TrimSpace(question),
			AskedAt:    d.now(),
		}
		state.Asks[id] = created
		return nil
	})
	return created, err
}

// Answer resolves a question, unblocking the agent waiting on it.
func (d *Director) Answer(askID, text string) (*Ask, error) {
	var answered *Ask
	err := d.mutate(func(state *State) error {
		ask, ok := state.Asks[askID]
		if !ok {
			return fmt.Errorf("%w: no question %q", ErrNotFound, askID)
		}
		if ask.Answered() {
			return fmt.Errorf("question %s was already answered at %s", askID, formatTime(ask.AnsweredAt))
		}
		ask.Answer = text
		ask.AnsweredAt = d.now()
		answered = ask
		return nil
	})
	return answered, err
}

// AwaitAnswer polls until a question is answered or the deadline passes.
//
// Polling a file rather than holding a connection is what lets this work when
// no director is currently running: the question waits in state, and whichever
// director looks next can answer it. A socket would require somebody to be
// listening at the moment the agent asked.
func (d *Director) AwaitAnswer(ctx context.Context, askID string, poll time.Duration) (*Ask, error) {
	if poll <= 0 {
		poll = 2 * time.Second
	}
	for {
		state, err := LoadState(d.State.Path)
		if err != nil {
			return nil, err
		}
		ask, ok := state.Asks[askID]
		if !ok {
			return nil, fmt.Errorf("%w: no question %q", ErrNotFound, askID)
		}
		if ask.Answered() {
			return ask, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(poll):
		}
	}
}

// Send pushes text into a running engagement, through its harness.
func (d *Director) Send(ctx context.Context, id, text string) error {
	engagement, err := d.Get(ctx, id)
	if err != nil {
		return err
	}
	adapter, err := d.lookupFor(engagement)
	if err != nil {
		return err
	}
	return adapter.Send(ctx, harness.SendRequest{Ref: engagement.Ref, Text: text})
}

// NudgeText is what a nudge actually says. It asks for the one thing that
// resolves the ambiguity — a report — rather than asking an open question that
// would cost a turn and answer nothing measurable.
const NudgeText = "Status check from your director: you have not reported recently. " +
	"If you are still working, run `director report --progress <value> --message \"<what you are doing>\"` now and continue. " +
	"If you are stuck or waiting on a decision, run `director ask \"<question>\" --wait` instead."

// Nudge asks a quiet engagement to say something.
//
// This is the director poking the agent, which is the point: without it the
// human ends up doing the poking, and the tool has failed at the thing it
// exists for.
func (d *Director) Nudge(ctx context.Context, id string) error {
	if err := d.Send(ctx, id, NudgeText); err != nil {
		return err
	}
	return d.mutate(func(state *State) error {
		if engagement, ok := state.Engagements[id]; ok {
			engagement.NudgedAt = d.now()
		}
		return nil
	})
}

// Stop stops an engagement. Both modes leave the conversation resumable and
// neither destroys a transcript.
func (d *Director) Stop(ctx context.Context, id string, mode harness.StopMode) error {
	engagement, err := d.Get(ctx, id)
	if err != nil {
		return err
	}
	adapter, err := d.lookupFor(engagement)
	if err != nil {
		return err
	}
	return adapter.Stop(ctx, harness.StopRequest{Ref: engagement.Ref, Mode: mode})
}

// Note attaches a director's own memory to an engagement.
func (d *Director) Note(id, text string) error {
	return d.mutate(func(state *State) error {
		engagement, ok := state.Engagements[id]
		if !ok {
			return fmt.Errorf("%w: no engagement %q", ErrNotFound, id)
		}
		engagement.Note = text
		return nil
	})
}

func (d *Director) lookupFor(engagement *Engagement) (harness.Adapter, error) {
	lookup := d.Lookup
	if lookup == nil {
		lookup = harness.Lookup
	}
	return lookup(engagement.Harness)
}

// authenticate checks that a token belongs to the engagement it claims.
func authenticate(state *State, engagementID, token string) (*Engagement, error) {
	engagement, ok := state.Engagements[engagementID]
	if !ok {
		return nil, fmt.Errorf("%w: no engagement %q", ErrNotFound, engagementID)
	}
	if engagement.Token == "" || token != engagement.Token {
		return nil, fmt.Errorf("token does not match engagement %s", engagementID)
	}
	return engagement, nil
}

// mutate performs a locked read-modify-write of the state file and refreshes
// the in-memory copy.
//
// It re-reads under the lock rather than writing what this process happens to
// hold. Every invocation is a separate process and an agent's report can land
// between this director's own commands, so writing a stale in-memory copy would
// silently drop whatever arrived in between.
func (d *Director) mutate(fn func(state *State) error) error {
	return withStateLock(d.State.Path, func() error {
		fresh, err := LoadState(d.State.Path)
		if err != nil {
			return err
		}
		if err := fn(fresh); err != nil {
			return err
		}
		if err := fresh.Save(); err != nil {
			return err
		}
		d.State = fresh
		return nil
	})
}

// alternatives explains what to do when a named director is not there.
//
// The wording distinguishes the two situations deliberately: a root with other
// directors in it is a stale identifier, while an empty root is a project that
// was never set up — and the fix is different.
func alternatives(roots Roots, wanted string) string {
	states, _ := ListDirectors(roots.Primary)
	if len(states) == 0 {
		return fmt.Sprintf("There are no directors here at all. If this project has not been set up, run `director init`;\n" +
			"otherwise check you are in the right directory — `director where` shows which root is in effect.")
	}

	var out strings.Builder
	source := "--director"
	if os.Getenv(EnvID) == wanted {
		source = EnvID
	}
	fmt.Fprintf(&out, "That %s is stale. These directors do exist here:\n", source)
	for _, state := range states {
		fmt.Fprintf(&out, "  %s  %s  (workflow %s, %d engagements)\n",
			state.DirectorID, state.Name, state.Workflow, len(state.Engagements))
	}
	fmt.Fprintf(&out, "\nRun `director attach` to pick one up, or `unset %s` first if it is exported.", EnvID)
	return out.String()
}
