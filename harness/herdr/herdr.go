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
}

// New builds an adapter with the defaults.
func New() *Adapter { return &Adapter{Kind: defaultKind} }

// Name identifies the adapter.
func (a *Adapter) Name() string { return Name }

// This adapter deliberately does not implement harness.SkillInstaller, so
// `director install` passes over it.
//
// herdr does not read skills. It opens a pane and launches somebody else's
// agent in it, and that agent loads skills from its own harness — installing
// for claude-code is what puts them in front of a claude running under herdr.
// A herdr entry in the installer would therefore write skills somewhere nothing
// reads them, and the person who picked it would have no way to tell that from
// its having worked.

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

// tabCreateResult is what tab.create returns. Both identifiers are optional in
// the decode because the pane may be reported directly or via a list; the
// adapter resolves whichever arrived.
type tabCreateResult struct {
	TabID  string     `json:"tab_id"`
	PaneID string     `json:"pane_id"`
	Panes  []paneInfo `json:"panes"`
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

	paneID := tab.PaneID
	if paneID == "" && len(tab.Panes) > 0 {
		paneID = tab.Panes[0].PaneID
	}
	if paneID == "" {
		return harness.SpawnResult{}, fmt.Errorf("herdr created a tab but reported no pane to start an agent in")
	}

	// The trailing arguments go to the agent binary itself. This is what lets a
	// herdr-hosted conversation still be given director's own session id and
	// its permission restrictions, rather than herdr's defaults.
	if err := a.rpc().call("agent.start", map[string]any{
		"name":    label,
		"kind":    a.kind(),
		"pane_id": paneID,
		"args":    a.agentArgs(req),
	}, nil); err != nil {
		return harness.SpawnResult{}, fmt.Errorf("starting a %s agent in pane %s: %w", a.kind(), paneID, err)
	}

	// The prompt is delivered separately, because agent.start only gets the
	// agent to an interactive prompt — it does not carry input.
	if strings.TrimSpace(req.Prompt) != "" {
		if err := a.rpc().call("agent.prompt", map[string]any{
			"target": paneID,
			"text":   req.Prompt,
		}, nil); err != nil && !stalled(err) {
			return harness.SpawnResult{}, fmt.Errorf("delivering the brief to pane %s: %w", paneID, err)
		}
	}

	return harness.SpawnResult{
		Ref: paneID,
		Detail: map[string]string{
			"pane_id": paneID,
			"tab_id":  tab.TabID,
			"kind":    a.kind(),
		},
	}, nil
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
func lifecycleFor(agent agentInfo) harness.Lifecycle {
	if agent.LaunchPending || !agent.InteractiveReady {
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

// paneReadResult is what pane.read returns.
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
	var result paneReadResult
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
			Text: result.Text,
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
