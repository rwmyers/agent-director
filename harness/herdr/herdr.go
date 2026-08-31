package herdr

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/rwmyers/agent-director/harness"
)

func init() { harness.Register(New()) }

// Name is the registry name, the [harness.herdr] config section, and what a
// workflow pins.
const Name = "herdr"

// defaultKind is the agent herdr launches when none is configured. It is
// herdr's own enum value — "claude", not director's harness name.
const defaultKind = "claude"

// Adapter drives agents inside herdr.
type Adapter struct {
	// Socket overrides the API socket path.
	Socket string
	// Kind is the herdr agent kind to launch. herdr knows twenty-odd.
	Kind string
	// Now is the clock, for tests.
	Now func() time.Time

	client *client
	// sleep is how the shell-readiness wait passes time, for tests.
	sleep func(time.Duration)
}

// shellWait bounds how long Spawn will wait for a pane herdr has just created
// to become a shell an agent can start in. Generous, because the wait is a
// login shell's own startup — a profile that sources a version manager can take
// seconds — and because the alternative to waiting is a failed spawn.
const shellWait = 15 * time.Second

// shellPoll is how often that wait retries. herdr publishes no readiness field
// for a pane with no agent in it, so asking again is the only signal there is.
const shellPoll = 100 * time.Millisecond

// agentWait bounds how long Spawn will wait for a started agent to become one
// herdr will accept a prompt for. It matches herdr's own documented startup
// allowance for agent.start, because that is the same event being waited on.
const agentWait = 30 * time.Second

// agentPoll is how often that wait asks again.
const agentPoll = 250 * time.Millisecond

func (a *Adapter) pause(d time.Duration) {
	if a.sleep == nil {
		time.Sleep(d)
		return
	}
	a.sleep(d)
}

// New builds an adapter with the defaults.
func New() *Adapter { return &Adapter{Kind: defaultKind} }

// Name identifies the adapter.
func (a *Adapter) Name() string { return Name }

func (a *Adapter) now() time.Time {
	if a.Now == nil {
		return time.Now()
	}
	return a.Now()
}

func (a *Adapter) rpc() *client {
	if a.client == nil {
		a.client = &client{path: socketPath(a.Socket)}
	}
	return a.client
}

func (a *Adapter) kind() string {
	if a.Kind == "" {
		return defaultKind
	}
	return a.Kind
}

// Enforceable lists what this adapter can control.
//
// herdr does not enforce anything itself — it launches an agent binary in a
// pane. What can be held therefore depends entirely on which agent, and is
// expressed by passing that agent its own restrictions on the command line.
func (a *Adapter) Enforceable() []harness.Capability {
	if a.kind() == defaultKind {
		return harness.AllCapabilities
	}
	return nil
}

// Permits applies the fail-closed rule.
//
// For the agent kind whose flags are understood, the same reasoning as driving
// it directly applies. For any other kind this refuses every restricted scope
// outright: herdr will happily launch an agent it knows nothing about, and
// claiming to have constrained it would be a guess about somebody else's
// command-line interface. A guess is exactly what a permission system may not
// make.
func (a *Adapter) Permits(allow []harness.Capability) error {
	if a.kind() != defaultKind {
		if len(harness.Withheld(allow)) > 0 {
			return fmt.Errorf(
				"%s cannot enforce a restricted permission scope for agent kind %q: it launches that agent as a command and does not know its flags. "+
					"Use an unrestricted scope, or a harness that understands this agent",
				Name, a.kind())
		}
		return nil
	}
	if !harness.Allows(allow, harness.CapPush) && harness.Allows(allow, harness.CapExecute) {
		return fmt.Errorf(
			"%s cannot withhold %q while granting %q for agent kind %q: publishing goes through the shell",
			Name, harness.CapPush, harness.CapExecute, a.kind())
	}
	if len(allow) == 0 {
		return fmt.Errorf("%s cannot run an agent with no capabilities at all", Name)
	}
	return nil
}

// tabCreateResult is what tab.create returns: herdr's `tab_created` envelope,
// which reports the tab and its root pane as two nested objects and flattens
// neither into the result. Decoding a level too high is not an error in Go —
// every field simply stays zero — so the nesting has to be right here or the
// adapter silently believes herdr made a tab with no pane in it.
type tabCreateResult struct {
	Tab      tabInfo  `json:"tab"`
	RootPane paneInfo `json:"root_pane"`
}

type tabInfo struct {
	TabID string `json:"tab_id"`
}

type paneInfo struct {
	PaneID string `json:"pane_id"`
	TabID  string `json:"tab_id"`
}

// agentInfo is herdr's view of one agent, per its published schema.
type agentInfo struct {
	PaneID           string `json:"pane_id"`
	Agent            string `json:"agent"`
	AgentStatus      string `json:"agent_status"`
	CWD              string `json:"cwd"`
	Name             string `json:"name"`
	LaunchPending    bool   `json:"launch_pending"`
	InteractiveReady bool   `json:"interactive_ready"`
	Revision         uint64 `json:"revision"`
}

type agentListResult struct {
	Agents []agentInfo `json:"agents"`
}

// Spawn starts an agent in a new tab.
//
// Two calls, because herdr requires it: a pane has to exist and be sitting at
// an interactive shell prompt before an agent can be started in it. This is the
// structural difference from a harness that accepts a caller-assigned identity
// up front, and the reason an engagement carries both an ID and a Ref.
func (a *Adapter) Spawn(_ context.Context, req harness.SpawnRequest) (harness.SpawnResult, error) {
	label := req.Name
	if label == "" {
		label = req.Title
	}

	var tab tabCreateResult
	err := a.rpc().call("tab.create", map[string]any{
		"cwd":   req.Dir,
		"env":   withoutPaneEnv(req.Env),
		"label": label,
		"focus": false,
	}, &tab)
	if err != nil {
		return harness.SpawnResult{}, err
	}

	tabID := tab.Tab.TabID
	if tabID == "" {
		tabID = tab.RootPane.TabID
	}
	paneID := tab.RootPane.PaneID
	if paneID == "" {
		a.discardTab(tabID)
		return harness.SpawnResult{}, fmt.Errorf("herdr created a tab but reported no pane to start an agent in")
	}

	// The trailing arguments go to the agent binary itself. This is what lets a
	// herdr-hosted conversation still be given director's own session id and
	// its permission restrictions, rather than herdr's defaults.
	if err := a.startAgent(agentName(label, req.ID), paneID, a.agentArgs(req)); err != nil {
		a.discardTab(tabID)
		return harness.SpawnResult{}, fmt.Errorf("starting a %s agent in pane %s: %w", a.kind(), paneID, err)
	}

	// The prompt is delivered separately, because agent.start only gets the
	// agent to an interactive prompt — it does not carry input.
	if strings.TrimSpace(req.Prompt) != "" {
		if err := a.awaitAgent(paneID); err != nil {
			a.discardTab(tabID)
			return harness.SpawnResult{}, err
		}
		if err := a.rpc().call("agent.prompt", map[string]any{
			"target": paneID,
			"text":   req.Prompt,
		}, nil); err != nil && !stalled(err) {
			a.discardTab(tabID)
			return harness.SpawnResult{}, fmt.Errorf("delivering the brief to pane %s: %w", paneID, err)
		}
	}

	return harness.SpawnResult{
		Ref: paneID,
		Detail: map[string]string{
			"pane_id": paneID,
			"tab_id":  tabID,
			"kind":    a.kind(),
		},
	}, nil
}

// discardTab closes a tab this adapter created but could not use.
//
// Nothing downstream can do it instead. A failed spawn deliberately keeps its
// state record, but that record's Ref is empty — there is no pane id in it — so
// neither stopping nor removing the engagement has anything to close, and the
// tab would sit in herdr's session for good. The adapter is the last place that
// still knows the id.
//
// Any failure to close is dropped: the caller is being told why the spawn
// failed, and burying that under a cleanup error would replace the answer with
// a detail about the tidying up.
func (a *Adapter) discardTab(tabID string) {
	if tabID == "" {
		return
	}
	_ = a.rpc().call("tab.close", map[string]any{"tab_id": tabID}, nil)
}

// startAgent starts an agent in a pane this adapter has just created, waiting
// out the window in which the pane exists but is not yet a shell.
//
// tab.create returns when the pane is allocated, not when the shell inside it
// has reached its prompt, and agent.start refuses a pane that is not "an
// available shell". The gap is small — a fifth of a second on an unloaded
// machine — which is exactly what makes it dangerous: it passes by hand and
// against every fake server, and fails when a director spawns two engagements
// at once or the machine is busy.
//
// Retrying is the whole mechanism because herdr publishes no readiness for a
// pane with no agent: PaneInfo has no interactive_ready, and agent.list cannot
// report a pane that has no agent in it yet. Asking again is the only question
// available. The retry is confined to a pane the adapter created moments ago,
// so "busy" can only mean "not ready yet" here, never "somebody else's agent is
// in it".
func (a *Adapter) startAgent(name, paneID string, args []string) error {
	params := map[string]any{
		"name":    name,
		"kind":    a.kind(),
		"pane_id": paneID,
		"args":    args,
	}
	attempts := int(shellWait / shellPoll)
	for attempt := 0; ; attempt++ {
		err := a.rpc().call("agent.start", params, nil)
		if err == nil || !paneBusy(err) || attempt >= attempts {
			return err
		}
		a.pause(shellPoll)
	}
}

// awaitAgent waits until herdr will accept a prompt for a pane's agent.
//
// agent.start returns the moment the process is launched, with launch_pending
// set: herdr has asked for an agent but has not yet been told one is live in
// that pane. Prompting in that window is refused outright with agent_not_ready,
// so a spawn that does not wait here delivers the brief nowhere and reports
// success, or fails with an error that reads like a herdr fault rather than a
// race.
//
// What ends the window is herdr's agent integration reporting in from inside
// the agent's own process. Screen detection alone does not do it — herdr will
// happily report the pane as an idle claude while still refusing to prompt it —
// which is why the timeout message names the integration. Without it installed
// this wait can only ever run out, and saying so is the difference between a
// two-minute fix and another investigation.
func (a *Adapter) awaitAgent(paneID string) error {
	attempts := int(agentWait / agentPoll)
	for attempt := 0; ; attempt++ {
		agents, err := a.list()
		if err != nil {
			return err
		}
		for _, agent := range agents {
			if agent.PaneID == paneID && !agent.LaunchPending {
				return nil
			}
		}
		if attempt >= attempts {
			return fmt.Errorf(
				"the %s agent started in pane %s but herdr never reported it ready to be prompted within %s. "+
					"herdr learns that from its agent integration, which reports from inside the agent's own process: check `herdr integration status` and install the one for %s if it is missing",
				a.kind(), paneID, agentWait, a.kind())
		}
		a.pause(agentPoll)
	}
}

// agentName renders a display name as a herdr agent name.
//
// These are two different things wearing one word. A tab's label is prose for a
// person to read and herdr takes it as given; an agent's name is an identifier
// herdr validates against `^[a-z][a-z0-9_-]{0,31}$` and rejects outright,
// before it even looks at the pane. Passing the label straight through means
// every engagement whose title carries a capital letter or a space — which is
// very nearly all of them — fails at agent.start with the tab already made.
//
// The fallback is the engagement id rather than a constant, because two agents
// sharing a name are indistinguishable in herdr's own interface. An id already
// satisfies the rule: prefixed, lowercase, hex.
func agentName(label, id string) string {
	if slug := slugify(label); slug != "" {
		return slug
	}
	if slug := slugify(id); slug != "" {
		return slug
	}
	return defaultKind
}

// slugify reduces a string to what herdr will accept as an agent name, or to
// empty when nothing usable survives.
func slugify(value string) string {
	const limit = 32
	var out []rune
	for _, char := range strings.ToLower(value) {
		switch {
		case char >= 'a' && char <= 'z':
			out = append(out, char)
		case char >= '0' && char <= '9', char == '-', char == '_':
			// Leading characters that are not letters are dropped rather than
			// replaced: herdr requires the first one to be a letter, and a
			// prefix invented here would appear in the pane header as if the
			// director had chosen it.
			if len(out) > 0 {
				out = append(out, char)
			}
		default:
			if len(out) > 0 && out[len(out)-1] != '-' {
				out = append(out, '-')
			}
		}
		if len(out) == limit {
			break
		}
	}
	// Truncation can land on a separator, which is legal but reads as an
	// unfinished word.
	return strings.TrimRight(string(out), "-_")
}

// agentArgs builds the command line for the agent herdr launches.
func (a *Adapter) agentArgs(req harness.SpawnRequest) []string {
	if a.kind() != defaultKind {
		return nil
	}
	var args []string
	if req.Name != "" {
		args = append(args, "--name", req.Name)
	}
	if tools := claudeTools(req.Allow); len(tools) > 0 {
		args = append(args, "--allowedTools", strings.Join(tools, " "))
	}
	return args
}

// claudeTools mirrors the allowlist used when driving that agent directly.
//
// The callback commands are always present, whatever the scope withholds: the
// reporting channel is not one of the capabilities a workflow grants, it is how
// the engagement exists at all. An agent that could do the work but not report
// it would finish correctly and be recorded as abandoned.
func claudeTools(allow []harness.Capability) []string {
	tools := []string{"Bash(director report:*)", "Bash(director ask:*)"}
	if harness.Allows(allow, harness.CapRead) {
		tools = append(tools, "Read", "Glob", "Grep", "NotebookRead")
	}
	if harness.Allows(allow, harness.CapSearch) || harness.Allows(allow, harness.CapNetwork) {
		tools = append(tools, "WebSearch", "WebFetch")
	}
	if harness.Allows(allow, harness.CapEdit) {
		tools = append(tools, "Edit", "Write", "NotebookEdit")
	}
	if harness.Allows(allow, harness.CapExecute) {
		tools = append(tools, "Bash", "BashOutput", "KillShell")
	}
	sort.Strings(tools)
	return tools
}

// Send delivers text to a running agent.
//
// Without a wait: a stalled prompt means the agent is still working and we
// stopped watching, which is not a failure and must not be reported as one.
func (a *Adapter) Send(_ context.Context, req harness.SendRequest) error {
	err := a.rpc().call("agent.prompt", map[string]any{
		"target": req.Ref,
		"text":   req.Text,
	}, nil)
	if err != nil && !stalled(err) {
		return err
	}
	return nil
}

// Get observes one agent.
func (a *Adapter) Get(ctx context.Context, ref string) (harness.Observation, error) {
	agents, err := a.list()
	if err != nil {
		return harness.Observation{}, err
	}
	for _, agent := range agents {
		if agent.PaneID == ref {
			return a.observe(agent), nil
		}
	}
	// herdr answered and has no such pane. That is an ordinary result — panes
	// get closed — and is reported as not-found so the core can distinguish it
	// from the server being unreachable.
	return harness.Observation{Ref: ref, Found: false}, nil
}

// List observes every agent herdr can see.
func (a *Adapter) List(_ context.Context, filter harness.Filter) ([]harness.Observation, error) {
	agents, err := a.list()
	if err != nil {
		return nil, err
	}
	var found []harness.Observation
	for _, agent := range agents {
		if filter.Dir != "" && agent.CWD != filter.Dir {
			continue
		}
		found = append(found, a.observe(agent))
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Ref < found[j].Ref })
	return found, nil
}

func (a *Adapter) list() ([]agentInfo, error) {
	var result agentListResult
	if err := a.rpc().call("agent.list", map[string]any{}, &result); err != nil {
		return nil, err
	}
	return result.Agents, nil
}

// observe maps herdr's view onto director's.
func (a *Adapter) observe(agent agentInfo) harness.Observation {
	observation := harness.Observation{
		Ref:       agent.PaneID,
		Found:     true,
		Lifecycle: lifecycleFor(agent),
		Detail: map[string]string{
			"pane_id":      agent.PaneID,
			"herdr_status": agent.AgentStatus,
		},
	}
	if agent.Agent != "" {
		observation.Detail["agent"] = agent.Agent
	}

	// herdr publishes no activity timestamp, only a monotonic revision counter
	// that means nothing across calls to a stateless adapter. But it does track
	// whether the agent is working, and that determination is itself the
	// activity signal — so a working agent is treated as active now, and
	// anything else leaves the clock unset. Leaving it unset is the honest
	// answer, and the core degrades such an engagement toward "stalled" rather
	// than assuming it is fine.
	if agent.AgentStatus == "working" {
		observation.LastActivityAt = a.now()
	}
	return observation
}

// lifecycleFor maps herdr's agent status.
//
// Startup is checked first and overrides the status, because an agent that has
// been asked for but has not reached its prompt reports a status that would
// otherwise read as ordinary — and sending a brief into a shell that is not yet
// an agent loses it silently.
//
// launch_pending is the whole test. interactive_ready is not read, even though
// it names exactly the thing being asked about, because herdr omits it from a
// live agent: an agent working away in a pane reports neither field, so
// requiring interactive_ready pins every herdr engagement at "starting" for its
// entire life and the director never sees any of them begin.
func lifecycleFor(agent agentInfo) harness.Lifecycle {
	if agent.LaunchPending {
		return harness.LifecycleStarting
	}
	switch agent.AgentStatus {
	case "working":
		return harness.LifecycleWorking
	case "idle":
		return harness.LifecycleIdle
	case "blocked":
		return harness.LifecycleBlocked
	case "done":
		return harness.LifecycleDone
	default:
		return harness.LifecycleUnknown
	}
}

// paneReadResponse is what pane.read returns: herdr's `pane_read` envelope,
// which nests the snapshot under "read" rather than returning its fields at the
// top level.
//
// Getting this level wrong is worse than getting tab.create wrong, because it
// does not fail. A decode aimed one level too high finds nothing, reports no
// error, and hands back an empty screen — which a director reads as the agent
// having said nothing at all.
type paneReadResponse struct {
	Read paneReadResult `json:"read"`
}

type paneReadResult struct {
	PaneID    string `json:"pane_id"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}

// Read returns what is on the agent's screen.
//
// herdr keeps no transcript, only a terminal. The result is therefore reported
// as a screen snapshot with no cursor and Complete false — scrollback is
// finite, so material that has scrolled away is gone rather than absent, and a
// director told otherwise would conclude the agent had said nothing.
func (a *Adapter) Read(_ context.Context, req harness.ReadRequest) (harness.ReadResult, error) {
	lines := req.Limit
	if lines <= 0 {
		lines = 200
	}
	var result paneReadResponse
	err := a.rpc().call("pane.read", map[string]any{
		"pane_id":    req.Ref,
		"source":     "recent_unwrapped",
		"lines":      lines,
		"strip_ansi": true,
	}, &result)
	if err != nil {
		if notFound(err) {
			return harness.ReadResult{}, fmt.Errorf("pane %s no longer exists", req.Ref)
		}
		return harness.ReadResult{}, err
	}

	return harness.ReadResult{
		Kind:     harness.ReadScreen,
		Cursor:   "",
		Complete: false,
		Turns: []harness.Turn{{
			Role: "screen",
			At:   a.now(),
			Text: result.Read.Text,
		}},
	}, nil
}

// Stop stops an engagement.
//
// Interrupt sends an escape, which is what a person would press. End closes the
// pane. Neither destroys anything herdr was keeping, because herdr keeps only
// the terminal.
func (a *Adapter) Stop(_ context.Context, req harness.StopRequest) error {
	if req.Mode == harness.StopInterrupt {
		err := a.rpc().call("agent.send_keys", map[string]any{
			"target": req.Ref,
			"keys":   []string{"escape"},
		}, nil)
		if err != nil && !notFound(err) {
			return err
		}
		return nil
	}
	err := a.rpc().call("pane.close", map[string]any{"pane_id": req.Ref}, nil)
	if err != nil && !notFound(err) {
		return err
	}
	return nil
}
