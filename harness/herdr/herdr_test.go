package herdr

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rwmyers/agent-director/harness"
)

// fakeServer replays canned replies over a real Unix socket.
//
// The whole herdr adapter is testable this way with no herdr installed and no
// server running, which matters because the server is frequently not running
// even on a machine that has it — so a test suite that needed one would be a
// test suite nobody could run.
type fakeServer struct {
	t       *testing.T
	path    string
	replies map[string]any
	calls   []request
}

func newFakeServer(t *testing.T, replies map[string]any) *fakeServer {
	t.Helper()
	// A short directory name: a Unix socket path has a hard length limit well
	// below PATH_MAX, and t.TempDir() under a long test name can exceed it.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := &fakeServer{t: t, path: filepath.Join(dir, "h.sock"), replies: replies}

	listener, err := net.Listen("unix", server.path)
	if err != nil {
		t.Skipf("cannot create a unix socket here: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go server.handle(conn)
		}
	}()
	return server
}

// sequence answers successive calls of one method differently, which is the
// only way to express a transient condition — a pane that is not a shell yet
// but will be. The last reply repeats, so a sequence can describe either
// something that clears or something that never does.
type sequence struct {
	mu      sync.Mutex
	replies []any
}

func (s *sequence) next() any {
	s.mu.Lock()
	defer s.mu.Unlock()
	reply := s.replies[0]
	if len(s.replies) > 1 {
		s.replies = s.replies[1:]
	}
	return reply
}

func (s *fakeServer) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return
	}
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		return
	}
	s.calls = append(s.calls, req)

	reply := map[string]any{"id": req.ID}
	if canned, ok := s.replies[req.Method]; ok {
		if steps, staged := canned.(*sequence); staged {
			canned = steps.next()
		}
		if failure, isErr := canned.(*responseError); isErr {
			reply["error"] = map[string]string{"code": failure.Code, "message": failure.Message}
		} else {
			reply["result"] = canned
		}
	} else {
		reply["result"] = map[string]any{}
	}
	body, _ := json.Marshal(reply)
	_, _ = conn.Write(append(body, '\n'))
}

// The replies below are herdr's own envelopes, carrying every field its
// published schema marks required. They are transcribed from
// `herdr api schema --json` — protocol 19, the schema the installed server
// prints — and deliberately not from the Go structs that decode them.
//
// That direction matters. Fixtures written to match the structs agree with the
// adapter by construction and can only ever confirm it, which is how a decode
// aimed at the wrong nesting level passed these tests for its whole life
// without once working against a real server.

// tabCreatedReply is what tab.create answers with: the tab and its root pane as
// separate nested objects, neither flattened into the result.
func tabCreatedReply(tabID, paneID string) map[string]any {
	return map[string]any{
		"type": "tab_created",
		"tab": map[string]any{
			"tab_id": tabID, "workspace_id": "w1", "number": 1,
			"label": "auth-review", "focused": false,
			"pane_count": 1, "agent_status": "unknown",
		},
		"root_pane": map[string]any{
			"pane_id": paneID, "terminal_id": "term1", "workspace_id": "w1",
			"tab_id": tabID, "focused": false,
			"agent_status": "unknown", "revision": 0,
		},
	}
}

// agentListReply is what agent.list answers with.
func agentListReply(agents ...map[string]any) map[string]any {
	return map[string]any{"type": "agent_list", "agents": agents}
}

// agentWaitReply is what agent.wait answers with: one agent nested under
// "agent", not its fields at the top level.
func agentWaitReply(agent map[string]any) map[string]any {
	return map[string]any{"type": "agent_info", "agent": agent}
}

// waitTimeoutReply is what agent.wait answers with when it was asked to watch
// for a change and none came. herdr calls that a timeout; it is not a failure.
func waitTimeoutReply() *responseError {
	return &responseError{Code: "timeout", Message: "timed out waiting for agent status"}
}

// promptableAgent is an agent herdr will accept a prompt for.
//
// It reports neither launch_pending nor interactive_ready, because that is the
// shape a live agent actually arrives in: herdr omits both once the launch has
// completed, and never republishes interactive_ready afterwards.
func promptableAgent(paneID string) map[string]any {
	return map[string]any{
		"pane_id": paneID, "terminal_id": "term1", "workspace_id": "w1",
		"tab_id": "t1", "focused": false, "agent": "claude",
		"agent_status": "idle", "revision": 1,
	}
}

// launchingAgent is the same pane in the window herdr refuses to prompt.
func launchingAgent(paneID string) map[string]any {
	agent := promptableAgent(paneID)
	agent["launch_pending"] = true
	agent["agent_status"] = "unknown"
	return agent
}

// paneReadReply is what pane.read answers with: the snapshot nested under
// "read", not returned at the top level.
func paneReadReply(paneID, text string, truncated bool) map[string]any {
	return map[string]any{
		"type": "pane_read",
		"read": map[string]any{
			"pane_id": paneID, "workspace_id": "w1", "tab_id": "t1",
			"source": "recent_unwrapped", "format": "text",
			"text": text, "revision": 0, "truncated": truncated,
		},
	}
}

func (s *fakeServer) adapter() *Adapter {
	fixed := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	return &Adapter{Socket: s.path, Kind: defaultKind, Now: func() time.Time { return fixed }}
}

func TestServerNotRunningIsReportedNotGuessed(t *testing.T) {
	t.Parallel()
	// The distinction the whole design rests on: a stopped server is an error,
	// not a fleet of engagements reported as "unknown". Conflating them would
	// make a director wait forever on agents that are not there.
	adapter := &Adapter{Socket: filepath.Join(t.TempDir(), "absent.sock")}

	_, err := adapter.List(context.Background(), harness.Filter{})
	if err == nil {
		t.Fatal("List() = nil error against a dead socket, want a failure")
	}
	if !errors.Is(err, ErrServerNotRunning) {
		t.Errorf("List() = %v, want ErrServerNotRunning", err)
	}
	if !strings.Contains(err.Error(), "absent.sock") {
		t.Errorf("List() error = %q, want it to name the socket so the fix is obvious", err)
	}
}

func TestObserve(t *testing.T) {
	t.Parallel()

	t.Run("an agent still coming up is starting, whatever its status says", func(t *testing.T) {
		t.Parallel()
		// Checked before the status, because sending a brief into a pane that
		// is still a shell loses it silently.
		server := newFakeServer(t, map[string]any{
			"agent.list": map[string]any{"agents": []any{map[string]any{
				"pane_id": "3", "agent_status": "idle",
				"launch_pending": true, "interactive_ready": false,
			}}},
		})
		observation, err := server.adapter().Get(context.Background(), "3")
		if err != nil {
			t.Fatalf("Get() = %v, want no error", err)
		}
		if observation.Lifecycle != harness.LifecycleStarting {
			t.Errorf("Lifecycle = %v, want %v", observation.Lifecycle, harness.LifecycleStarting)
		}
	})

	t.Run("a working agent supplies an activity clock", func(t *testing.T) {
		t.Parallel()
		// herdr publishes no activity timestamp, but it does determine whether
		// an agent is working — and that determination is itself the signal.
		server := newFakeServer(t, map[string]any{
			"agent.list": map[string]any{"agents": []any{map[string]any{
				"pane_id": "3", "agent_status": "working", "interactive_ready": true,
			}}},
		})
		observation, err := server.adapter().Get(context.Background(), "3")
		if err != nil {
			t.Fatalf("Get() = %v, want no error", err)
		}
		if observation.Lifecycle != harness.LifecycleWorking {
			t.Errorf("Lifecycle = %v, want %v", observation.Lifecycle, harness.LifecycleWorking)
		}
		if observation.LastActivityAt.IsZero() {
			t.Error("LastActivityAt is zero for a working agent; without it health cannot tell quiet from stalled")
		}
	})

	t.Run("an idle agent supplies no activity clock rather than a fabricated one", func(t *testing.T) {
		t.Parallel()
		server := newFakeServer(t, map[string]any{
			"agent.list": map[string]any{"agents": []any{map[string]any{
				"pane_id": "3", "agent_status": "idle", "interactive_ready": true,
			}}},
		})
		observation, err := server.adapter().Get(context.Background(), "3")
		if err != nil {
			t.Fatalf("Get() = %v, want no error", err)
		}
		if !observation.LastActivityAt.IsZero() {
			t.Error("LastActivityAt was set for an idle agent; herdr does not know when it last did anything")
		}
	})

	t.Run("a pane herdr has never heard of is not-found, not an error", func(t *testing.T) {
		t.Parallel()
		// Panes get closed. That is ordinary, and must be distinguishable from
		// the adapter being broken.
		server := newFakeServer(t, map[string]any{
			"agent.list": map[string]any{"agents": []any{}},
		})
		observation, err := server.adapter().Get(context.Background(), "99")
		if err != nil {
			t.Fatalf("Get() = %v, want no error", err)
		}
		if observation.Found {
			t.Error("Found = true for a pane that does not exist")
		}
	})
}

func TestSpawnIsTwoCalls(t *testing.T) {
	t.Parallel()
	// herdr requires a pane at an interactive shell prompt before an agent can
	// start in it, so spawning is structurally two steps. This is the shape
	// that forced the engagement ID and the harness Ref apart.
	server := newFakeServer(t, map[string]any{
		"tab.create": tabCreatedReply("t1", "p7"),
		"agent.wait": agentWaitReply(promptableAgent("p7")),
	})

	result, err := server.adapter().Spawn(context.Background(), harness.SpawnRequest{
		ID: "eng_abc", Dir: "/tmp/x", Name: "auth-review", Prompt: "do the thing",
		Allow: []harness.Capability{harness.CapRead, harness.CapSearch},
		Env:   map[string]string{"DIRECTOR_TOKEN": "t"},
	})
	if err != nil {
		t.Fatalf("Spawn() = %v, want no error", err)
	}
	if result.Ref != "p7" {
		t.Errorf("Ref = %q, want herdr's own pane id %q", result.Ref, "p7")
	}
	if result.Detail["tab_id"] != "t1" {
		t.Errorf("Detail[tab_id] = %q, want %q from the nested tab object", result.Detail["tab_id"], "t1")
	}

	methods := make([]string, len(server.calls))
	for i, call := range server.calls {
		methods[i] = call.Method
	}
	// agent.wait sits between the two because herdr will not accept a prompt for
	// an agent whose launch it has not settled.
	want := []string{"tab.create", "agent.start", "agent.wait", "agent.prompt"}
	if strings.Join(methods, ",") != strings.Join(want, ",") {
		t.Errorf("Spawn() called %v, want %v", methods, want)
	}
}

func TestSpawnWaitsForTheNewPaneToBecomeAShell(t *testing.T) {
	t.Parallel()
	// tab.create returns when the pane is allocated, not when the shell inside
	// it has reached a prompt, and agent.start refuses a pane that is not yet
	// "an available shell". The gap is a fifth of a second on an idle machine,
	// which is why it survives being tried by hand and only shows up when a
	// director spawns several engagements at once.
	paneBusyReply := func() *responseError {
		return &responseError{Code: "agent_pane_busy", Message: "agent target pane p7 is not an available shell"}
	}

	t.Run("the shell arrives", func(t *testing.T) {
		t.Parallel()
		server := newFakeServer(t, map[string]any{
			"tab.create": tabCreatedReply("t1", "p7"),
			"agent.start": &sequence{replies: []any{
				paneBusyReply(),
				paneBusyReply(),
				map[string]any{"type": "agent_started"},
			}},
			"agent.wait": agentWaitReply(promptableAgent("p7")),
		})
		adapter := server.adapter()
		waits := 0
		adapter.sleep = func(time.Duration) { waits++ }

		result, err := adapter.Spawn(context.Background(), harness.SpawnRequest{
			ID: "eng_abc", Dir: "/tmp/x", Name: "auth-review", Prompt: "do the thing",
			Allow: harness.AllCapabilities,
		})
		if err != nil {
			t.Fatalf("Spawn() = %v, want the busy pane waited out rather than reported", err)
		}
		if result.Ref != "p7" {
			t.Errorf("Ref = %q, want %q", result.Ref, "p7")
		}
		if waits != 2 {
			t.Errorf("waited %d times, want 2 — one per busy reply", waits)
		}
		for _, call := range server.calls {
			if call.Method == "tab.close" {
				t.Error("Spawn() closed the tab it went on to use")
			}
		}
	})

	t.Run("the shell never arrives", func(t *testing.T) {
		t.Parallel()
		// Waiting is bounded. A pane that stays busy has to end as a reported
		// failure with the tab cleaned up, not as a director blocked for good.
		server := newFakeServer(t, map[string]any{
			"tab.create":  tabCreatedReply("t1", "p7"),
			"agent.start": &sequence{replies: []any{paneBusyReply()}},
		})
		adapter := server.adapter()
		adapter.sleep = func(time.Duration) {}

		_, err := adapter.Spawn(context.Background(), harness.SpawnRequest{
			ID: "eng_abc", Dir: "/tmp/x", Name: "auth-review", Prompt: "do the thing",
			Allow: harness.AllCapabilities,
		})
		if err == nil {
			t.Fatal("Spawn() = nil error, want the busy pane reported once waiting ran out")
		}
		if !strings.Contains(err.Error(), "not an available shell") {
			t.Errorf("Spawn() = %v, want herdr's own reason kept", err)
		}
		closed := 0
		for _, call := range server.calls {
			if call.Method == "tab.close" {
				closed++
			}
		}
		if closed != 1 {
			t.Errorf("Spawn() made %d tab.close calls, want exactly 1", closed)
		}
	})
}

// herdrAgentName is herdr's own rule for an agent name, transcribed from the
// error the server returns when it rejects one: "must start with a lowercase
// letter and contain only lowercase letters, digits, '-' or '_' (1-32
// characters)". herdr checks it before it even looks the pane up, so a name
// that fails it fails the whole spawn with the tab already created.
var herdrAgentName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

func TestSpawnNamesTheAgentSomethingHerdrAccepts(t *testing.T) {
	t.Parallel()
	// A tab label and an agent name are two different things wearing one word.
	// The label is prose herdr takes as given; the name is an identifier herdr
	// validates. Sending the label as the name fails for any title with a
	// capital letter or a space in it, which is very nearly every title a
	// director writes.
	cases := []struct {
		name  string
		label string
		want  string
	}{
		{name: "a written title", label: "Fix herdr spawning", want: "fix-herdr-spawning"},
		{name: "already a slug", label: "auth-review", want: "auth-review"},
		{name: "punctuation and runs of space", label: "Decode herdr's replies:  properly", want: "decode-herdr-s-replies-properly"},
		{name: "leading digits herdr would reject", label: "2 tabs, no pane", want: "tabs-no-pane"},
		{name: "longer than herdr allows", label: "a title comfortably longer than thirty-two characters", want: "a-title-comfortably-longer-than"},
		{name: "nothing usable survives", label: "!!! ???", want: "eng_abc123"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			server := newFakeServer(t, map[string]any{
				"tab.create": tabCreatedReply("t1", "p7"),
			})

			if _, err := server.adapter().Spawn(context.Background(), harness.SpawnRequest{
				ID: "eng_abc123", Dir: "/tmp/x", Name: testCase.label,
				Allow: harness.AllCapabilities,
			}); err != nil {
				t.Fatalf("Spawn() = %v, want no error", err)
			}

			var started, created map[string]any
			for _, call := range server.calls {
				switch call.Method {
				case "agent.start":
					started, _ = call.Params.(map[string]any)
				case "tab.create":
					created, _ = call.Params.(map[string]any)
				}
			}
			if started == nil {
				t.Fatal("Spawn() made no agent.start call")
			}
			got, _ := started["name"].(string)
			if !herdrAgentName.MatchString(got) {
				t.Errorf("agent.start name = %q, which herdr rejects as invalid_agent_name", got)
			}
			if got != testCase.want {
				t.Errorf("agent.start name = %q, want %q", got, testCase.want)
			}

			// The tab keeps the prose. Slugging the label as well would take the
			// title out of the one place a person reads it.
			if label, _ := created["label"].(string); label != testCase.label {
				t.Errorf("tab.create label = %q, want the title verbatim %q", label, testCase.label)
			}
		})
	}
}

func TestSpawnWaitsForTheAgentBeforeDeliveringTheBrief(t *testing.T) {
	t.Parallel()
	// agent.start returns as soon as the process is launched, with the launch
	// still pending. herdr refuses to prompt an agent in that window, so a
	// spawn that goes straight from start to prompt loses the brief — and
	// reports a herdr error rather than the race that caused it.
	t.Run("the agent arrives", func(t *testing.T) {
		t.Parallel()
		server := newFakeServer(t, map[string]any{
			"tab.create": tabCreatedReply("t1", "p7"),
			"agent.wait": &sequence{replies: []any{
				agentWaitReply(launchingAgent("p7")),
				agentWaitReply(launchingAgent("p7")),
				agentWaitReply(promptableAgent("p7")),
			}},
		})
		adapter := server.adapter()
		waits := 0
		adapter.sleep = func(time.Duration) { waits++ }

		if _, err := adapter.Spawn(context.Background(), harness.SpawnRequest{
			ID: "eng_abc", Dir: "/tmp/x", Name: "auth-review", Prompt: "do the thing",
			Allow: harness.AllCapabilities,
		}); err != nil {
			t.Fatalf("Spawn() = %v, want the pending launch waited out", err)
		}
		if waits != 2 {
			t.Errorf("waited %d times, want 2 — one per launch_pending reply", waits)
		}

		// The brief has to go out after the wait, not before it.
		var order []string
		for _, call := range server.calls {
			if call.Method == "agent.wait" || call.Method == "agent.prompt" {
				order = append(order, call.Method)
			}
		}
		want := []string{"agent.wait", "agent.wait", "agent.wait", "agent.prompt"}
		if strings.Join(order, ",") != strings.Join(want, ",") {
			t.Errorf("Spawn() called %v, want %v", order, want)
		}
	})

	t.Run("a wait that runs out is a tick, not a failure", func(t *testing.T) {
		t.Parallel()
		// agent.wait answers "nothing changed" with a timeout error. Treating
		// that as the server failing would abandon the spawn on the one reply
		// that means the wait is doing its job.
		server := newFakeServer(t, map[string]any{
			"tab.create": tabCreatedReply("t1", "p7"),
			"agent.wait": &sequence{replies: []any{
				waitTimeoutReply(),
				agentWaitReply(promptableAgent("p7")),
			}},
		})
		adapter := server.adapter()
		adapter.sleep = func(time.Duration) {}

		if _, err := adapter.Spawn(context.Background(), harness.SpawnRequest{
			ID: "eng_abc", Dir: "/tmp/x", Name: "auth-review", Prompt: "do the thing",
			Allow: harness.AllCapabilities,
		}); err != nil {
			t.Fatalf("Spawn() = %v, want a timed-out wait retried rather than reported", err)
		}
	})

	t.Run("the agent never arrives", func(t *testing.T) {
		t.Parallel()
		// herdr is told an agent is live by its integration, reporting from
		// inside the agent's own process. With that not installed the wait can
		// only run out, so the message has to name it — otherwise the failure
		// looks like herdr misbehaving and gets investigated all over again.
		server := newFakeServer(t, map[string]any{
			"tab.create": tabCreatedReply("t1", "p7"),
			"agent.wait": agentWaitReply(launchingAgent("p7")),
		})
		adapter := server.adapter()
		adapter.sleep = func(time.Duration) {}

		_, err := adapter.Spawn(context.Background(), harness.SpawnRequest{
			ID: "eng_abc", Dir: "/tmp/x", Name: "auth-review", Prompt: "do the thing",
			Allow: harness.AllCapabilities,
		})
		if err == nil {
			t.Fatal("Spawn() = nil error, want the brief refused rather than delivered nowhere")
		}
		if !strings.Contains(err.Error(), "integration") {
			t.Errorf("Spawn() = %v, want the agent integration named as what reports readiness", err)
		}
		for _, call := range server.calls {
			if call.Method == "agent.prompt" {
				t.Error("Spawn() sent the brief to an agent herdr had not reported ready")
			}
		}
		closed := 0
		for _, call := range server.calls {
			if call.Method == "tab.close" {
				closed++
			}
		}
		if closed != 1 {
			t.Errorf("Spawn() made %d tab.close calls, want exactly 1", closed)
		}
	})
}

func TestSpawnWaitsOnTheAgentRatherThanPollingTheAgentList(t *testing.T) {
	t.Parallel()
	// The defect this exists for. herdr settles a pending launch when something
	// waits on that agent; agent.list answers from the state it has already
	// published and settles nothing. A spawn that polls agent.list therefore
	// reads launch_pending forever against a server where an agent.wait against
	// the same pane would clear it in about three seconds — which is what a
	// director sees as every herdr spawn timing out while the agent sits at its
	// prompt, visibly ready.
	//
	// So agent.list is wired here to the answer that never clears, and only
	// agent.wait to the one that does.
	server := newFakeServer(t, map[string]any{
		"tab.create": tabCreatedReply("t1", "p7"),
		"agent.list": agentListReply(launchingAgent("p7")),
		"agent.wait": agentWaitReply(promptableAgent("p7")),
	})
	adapter := server.adapter()
	adapter.sleep = func(time.Duration) {}

	if _, err := adapter.Spawn(context.Background(), harness.SpawnRequest{
		ID: "eng_abc", Dir: "/tmp/x", Name: "auth-review", Prompt: "do the thing",
		Allow: harness.AllCapabilities,
	}); err != nil {
		t.Fatalf("Spawn() = %v, want readiness taken from agent.wait", err)
	}

	prompted := false
	for _, call := range server.calls {
		if call.Method == "agent.list" {
			t.Error("Spawn() polled agent.list for readiness; that view never settles a pending launch")
		}
		if call.Method == "agent.prompt" {
			prompted = true
		}
	}
	if !prompted {
		t.Error("Spawn() never delivered the brief")
	}
}

func TestSpawnConfirmsTheBriefWasTaken(t *testing.T) {
	t.Parallel()
	// herdr accepting a prompt is not the agent having taken it. A settled
	// launch means herdr's bookkeeping is done; the agent's own input can be a
	// moment behind, swallow the paste, and leave the box empty. The engagement
	// is then recorded as running and has been told nothing, which looks exactly
	// like an agent thinking until somebody opens the pane.

	t.Run("herdr is asked to watch for the agent to act", func(t *testing.T) {
		t.Parallel()
		server := newFakeServer(t, map[string]any{
			"tab.create": tabCreatedReply("t1", "p7"),
			"agent.wait": agentWaitReply(promptableAgent("p7")),
		})

		if _, err := server.adapter().Spawn(context.Background(), harness.SpawnRequest{
			ID: "eng_abc", Dir: "/tmp/x", Name: "auth-review", Prompt: "do the thing",
			Allow: harness.AllCapabilities,
		}); err != nil {
			t.Fatalf("Spawn() = %v, want no error", err)
		}

		var prompt map[string]any
		for _, call := range server.calls {
			if call.Method == "agent.prompt" {
				prompt, _ = call.Params.(map[string]any)
			}
		}
		if prompt == nil {
			t.Fatal("Spawn() made no agent.prompt call")
		}
		wait, ok := prompt["wait"].(map[string]any)
		if !ok {
			t.Fatalf("agent.prompt wait = %v, want herdr asked to confirm the agent acted on the brief", prompt["wait"])
		}
		until, ok := wait["until"].([]any)
		if !ok || len(until) == 0 {
			t.Fatalf("agent.prompt wait.until = %v, want the statuses that mean the text arrived", wait["until"])
		}
		for _, status := range until {
			if status == "idle" {
				t.Error("agent.prompt wait.until includes idle, which is the state a swallowed prompt leaves the agent in")
			}
		}
	})

	t.Run("an agent that never acts on it fails the spawn", func(t *testing.T) {
		t.Parallel()
		// Reporting this as a success is the expensive outcome: the director
		// supervises an engagement that was never told anything, and only finds
		// out by reading the pane.
		server := newFakeServer(t, map[string]any{
			"tab.create":   tabCreatedReply("t1", "p7"),
			"agent.wait":   agentWaitReply(promptableAgent("p7")),
			"agent.prompt": &responseError{Code: "agent_prompt_stalled", Message: "no status change"},
		})

		_, err := server.adapter().Spawn(context.Background(), harness.SpawnRequest{
			ID: "eng_abc", Dir: "/tmp/x", Name: "auth-review", Prompt: "do the thing",
			Allow: harness.AllCapabilities,
		})
		if err == nil {
			t.Fatal("Spawn() = nil error, want a brief that never took reported rather than recorded as running")
		}
		closed := 0
		for _, call := range server.calls {
			if call.Method == "tab.close" {
				closed++
			}
		}
		if closed != 1 {
			t.Errorf("Spawn() made %d tab.close calls, want exactly 1", closed)
		}
	})
}

func TestALiveAgentIsNotReportedAsStillStarting(t *testing.T) {
	t.Parallel()
	// herdr omits launch_pending and interactive_ready from a live agent
	// entirely — an agent working away in a pane publishes neither. Requiring
	// interactive_ready therefore pins every herdr engagement at "starting" for
	// its whole life, and a director watching for one to begin waits forever.
	server := newFakeServer(t, map[string]any{
		"agent.list": agentListReply(map[string]any{
			"pane_id": "3", "terminal_id": "term1", "workspace_id": "w1",
			"tab_id": "t1", "focused": true, "agent": "claude",
			"agent_status": "working", "revision": 691,
		}),
	})
	observation, err := server.adapter().Get(context.Background(), "3")
	if err != nil {
		t.Fatalf("Get() = %v, want no error", err)
	}
	if observation.Lifecycle != harness.LifecycleWorking {
		t.Errorf("Lifecycle = %v, want %v for an agent herdr says is working",
			observation.Lifecycle, harness.LifecycleWorking)
	}
}

func TestAFailedSpawnDoesNotLeaveATabBehind(t *testing.T) {
	t.Parallel()
	// The tab already exists by the time anything can go wrong, and nothing
	// downstream can reach it: a spawn the adapter reports as failed leaves no
	// engagement record, and returns no pane id, so nothing anywhere else names
	// this tab. The adapter is the last thing that still knows the tab id, so if
	// it walks away the tab is in herdr's session for good.
	cases := []struct {
		name    string
		failing string
	}{
		{name: "the agent will not start", failing: "agent.start"},
		{name: "the brief cannot be delivered", failing: "agent.prompt"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			server := newFakeServer(t, map[string]any{
				"tab.create":     tabCreatedReply("t1", "p7"),
				"agent.wait":     agentWaitReply(promptableAgent("p7")),
				testCase.failing: &responseError{Code: "internal", Message: "no"},
			})

			if _, err := server.adapter().Spawn(context.Background(), harness.SpawnRequest{
				ID: "eng_abc", Dir: "/tmp/x", Name: "auth-review", Prompt: "do the thing",
				Allow: harness.AllCapabilities,
			}); err == nil {
				t.Fatalf("Spawn() = nil error, want the %s failure reported", testCase.failing)
			}

			var closed []any
			for _, call := range server.calls {
				if call.Method == "tab.close" {
					closed = append(closed, call.Params)
				}
			}
			if len(closed) != 1 {
				t.Fatalf("Spawn() made %d tab.close calls, want exactly 1", len(closed))
			}
			params, ok := closed[0].(map[string]any)
			if !ok {
				t.Fatalf("tab.close params = %T, want an object", closed[0])
			}
			if params["tab_id"] != "t1" {
				t.Errorf("tab.close tab_id = %v, want %q", params["tab_id"], "t1")
			}
		})
	}
}

func TestReadIsAScreenSnapshot(t *testing.T) {
	t.Parallel()
	// herdr keeps a terminal, not a transcript. Saying so is what stops a
	// director concluding an agent was silent when its output simply scrolled
	// away.
	server := newFakeServer(t, map[string]any{
		"pane.read": paneReadReply("p7", "some output", true),
	})
	result, err := server.adapter().Read(context.Background(), harness.ReadRequest{Ref: "p7"})
	if err != nil {
		t.Fatalf("Read() = %v, want no error", err)
	}
	// A read decoded at the wrong nesting level returns no error and an empty
	// screen, which a director cannot tell from an agent that said nothing. The
	// screen's contents are therefore the assertion that matters most here.
	if len(result.Turns) != 1 || result.Turns[0].Text != "some output" {
		t.Errorf("Turns = %v, want the pane's text; an empty screen reads as a silent agent", result.Turns)
	}
	if result.Kind != harness.ReadScreen {
		t.Errorf("Kind = %v, want %v", result.Kind, harness.ReadScreen)
	}
	if result.Complete {
		t.Error("Complete = true for a screen snapshot; scrollback is finite and the caller must be told")
	}
	if result.Cursor != "" {
		t.Errorf("Cursor = %q, want empty — there is no position to resume from", result.Cursor)
	}
}

func TestStalledPromptIsNotAFailure(t *testing.T) {
	t.Parallel()
	// A prompt that times out waiting for a status change means the agent is
	// still working and we stopped watching. Reporting it as an error would
	// have a director abandon healthy work.
	server := newFakeServer(t, map[string]any{
		"agent.prompt": &responseError{Code: "agent_prompt_stalled", Message: "still working"},
	})
	if err := server.adapter().Send(context.Background(), harness.SendRequest{Ref: "p7", Text: "hi"}); err != nil {
		t.Errorf("Send() = %v, want a stalled prompt to be treated as normal", err)
	}
}

func TestPermits(t *testing.T) {
	t.Parallel()

	t.Run("an unknown agent kind cannot enforce a restricted scope", func(t *testing.T) {
		t.Parallel()
		// herdr will happily launch an agent it knows nothing about. Claiming
		// to have constrained it would be a guess about somebody else's flags,
		// and a permission system may not guess.
		adapter := &Adapter{Kind: "some-other-agent"}
		err := adapter.Permits([]harness.Capability{harness.CapRead})
		if err == nil {
			t.Fatal("Permits() = nil error, want a refusal")
		}
		if !strings.Contains(err.Error(), "some-other-agent") {
			t.Errorf("Permits() error = %q, want it to name the kind", err)
		}
	})

	t.Run("an unknown agent kind is fine with an unrestricted scope", func(t *testing.T) {
		t.Parallel()
		adapter := &Adapter{Kind: "some-other-agent"}
		if err := adapter.Permits(harness.AllCapabilities); err != nil {
			t.Errorf("Permits(everything) = %v, want no error — nothing is being withheld", err)
		}
	})
}

func TestAgentArgsCarryTheCallback(t *testing.T) {
	t.Parallel()
	// The reporting channel is not one of the capabilities a workflow grants;
	// it is how the engagement exists at all. A read-only agent that could work
	// but not report would finish correctly and be recorded as abandoned.
	adapter := &Adapter{Kind: defaultKind}
	args := adapter.agentArgs(harness.SpawnRequest{
		Name:  "auth-review",
		Allow: []harness.Capability{harness.CapRead},
	})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "director report") || !strings.Contains(joined, "director ask") {
		t.Errorf("agentArgs() = %v, want the callback commands allowed", args)
	}
	if !strings.Contains(joined, "auth-review") {
		t.Errorf("agentArgs() = %v, want the display name passed to the agent", args)
	}
}

func TestInPane(t *testing.T) {
	// No t.Parallel: these cases set the process environment, which is the
	// thing under test.
	cases := []struct {
		name     string
		env      map[string]string
		wantPane string
		wantOK   bool
	}{
		{
			name:   "nothing set at all is not a pane",
			env:    map[string]string{},
			wantOK: false,
		},
		{
			// The trap this exists to avoid. The socket variable is set whenever
			// herdr is configured, in a pane or out of one, so treating it as a
			// presence signal would place every engagement into panes on any
			// machine that has herdr installed.
			name:   "a socket alone is not a pane",
			env:    map[string]string{EnvSocket: "/tmp/herdr.sock"},
			wantOK: false,
		},
		{
			name:   "a pane id without the pane marker is not a pane",
			env:    map[string]string{EnvPane: "w6:p1"},
			wantOK: false,
		},
		{
			name:   "any value other than 1 is not a pane",
			env:    map[string]string{EnvInPane: "0", EnvPane: "w6:p1"},
			wantOK: false,
		},
		{
			name:     "the marker and an id is a pane",
			env:      map[string]string{EnvInPane: "1", EnvPane: "w6:p1"},
			wantPane: "w6:p1",
			wantOK:   true,
		},
		{
			// Still in a pane. The id is a label for a person, and losing the
			// label must not change the answer to the question being asked.
			name:     "the marker without an id is still a pane",
			env:      map[string]string{EnvInPane: "1"},
			wantPane: "",
			wantOK:   true,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			for _, key := range []string{EnvInPane, EnvPane, EnvSocket} {
				t.Setenv(key, testCase.env[key])
			}
			pane, ok := InPane()
			if ok != testCase.wantOK {
				t.Errorf("InPane() ok = %v, want %v", ok, testCase.wantOK)
			}
			if pane != testCase.wantPane {
				t.Errorf("InPane() pane = %q, want %q", pane, testCase.wantPane)
			}
		})
	}
}

func TestSpawnDoesNotForwardAPaneIdentity(t *testing.T) {
	t.Parallel()
	// A director in a pane empties these on the way past so nothing it spawns
	// inherits its pane. herdr assigns a new pane its own identity, so passing
	// the emptied values on could overwrite what herdr sets and leave the pane
	// unable to recognise itself. The socket is not identity and must survive.
	server := newFakeServer(t, map[string]any{
		"tab.create": tabCreatedReply("t1", "p7"),
	})

	_, err := server.adapter().Spawn(context.Background(), harness.SpawnRequest{
		ID: "eng_abc", Dir: "/tmp/x", Name: "auth-review",
		Allow: harness.AllCapabilities,
		Env: map[string]string{
			"DIRECTOR_TOKEN": "t",
			EnvInPane:        "",
			EnvPane:          "",
			EnvSocket:        "/tmp/herdr.sock",
		},
	})
	if err != nil {
		t.Fatalf("Spawn() = %v, want no error", err)
	}

	params, ok := server.calls[0].Params.(map[string]any)
	if !ok {
		t.Fatalf("tab.create params = %T, want an object", server.calls[0].Params)
	}
	env, ok := params["env"].(map[string]any)
	if !ok {
		t.Fatalf("tab.create env = %T, want an object", params["env"])
	}
	for _, key := range []string{EnvInPane, EnvPane} {
		if _, present := env[key]; present {
			t.Errorf("tab.create was handed %s; herdr names its own panes", key)
		}
	}
	if env[EnvSocket] != "/tmp/herdr.sock" {
		t.Errorf("tab.create env[%s] = %v, want the socket to survive", EnvSocket, env[EnvSocket])
	}
	if env["DIRECTOR_TOKEN"] != "t" {
		t.Errorf("tab.create env[DIRECTOR_TOKEN] = %v, want the callback env untouched", env["DIRECTOR_TOKEN"])
	}
}

func TestHostsAndLocate(t *testing.T) {
	adapter := New()
	want := harness.Hosting{Background: true, Wake: true}
	if got := adapter.Hosts(); got != want {
		t.Errorf("Hosts() = %v, want %v", got, want)
	}

	// Locate is InPane under the interface name, so it must agree with it.
	for _, key := range []string{EnvInPane, EnvPane} {
		t.Setenv(key, "")
	}
	if ref, inside := adapter.Locate(); inside {
		t.Errorf("Locate() outside a pane = %q, %v, want not inside", ref, inside)
	}
	t.Setenv(EnvInPane, "1")
	t.Setenv(EnvPane, "w6:p1")
	ref, inside := adapter.Locate()
	if !inside || ref != "w6:p1" {
		t.Errorf("Locate() in a pane = %q, %v, want \"w6:p1\", true", ref, inside)
	}

	// A pane is a window onto an agent, not the agent. Declaring this is what
	// keeps herdr from being taken as the host of a Claude Code conversation it
	// is merely drawing, and being handed a wake nothing can deliver.
	if !adapter.Displays() {
		t.Error("Displays() = false, want true: a pane shows another harness's conversation")
	}
}

// TestHerdrIsNotAnInstallTarget pins the other half of the same fact.
//
// herdr reads no skills. It opens a pane and launches somebody else's agent in
// it, and that agent loads skills from its own harness — so installing for
// claude-code is what puts them in front of a claude running under herdr. A
// herdr entry in `director install` would write them where nothing reads them,
// and whoever picked it would have no way to tell that from its having worked.
func TestHerdrIsNotAnInstallTarget(t *testing.T) {
	t.Parallel()
	installer, ok := any(New()).(harness.SkillInstaller)
	if !ok {
		return
	}
	locations, err := installer.SkillLocations()
	t.Fatalf("herdr declares a skills location %+v (err %v), want none: it displays an agent rather than reading skills", locations, err)
}

func TestDisposeClosesThePane(t *testing.T) {
	t.Parallel()
	server := newFakeServer(t, map[string]any{})
	adapter := server.adapter()

	if !adapter.Disposes() {
		t.Fatal("Disposes() = false, want herdr to declare it has panes to give back")
	}
	if err := adapter.Dispose(context.Background(), harness.DisposeRequest{Ref: "p7"}); err != nil {
		t.Fatalf("Dispose() = %v, want no error", err)
	}

	if len(server.calls) != 1 {
		t.Fatalf("Dispose() made %d calls, want 1", len(server.calls))
	}
	call := server.calls[0]
	if call.Method != "pane.close" {
		t.Errorf("Dispose() called %s, want pane.close", call.Method)
	}
	params, ok := call.Params.(map[string]any)
	if !ok {
		t.Fatalf("Dispose() sent params %#v, want an object", call.Params)
	}
	if params["pane_id"] != "p7" {
		t.Errorf("Dispose() closed pane %v, want p7", params["pane_id"])
	}
}

func TestDisposeTreatsAPaneThatHasGoneAsDone(t *testing.T) {
	t.Parallel()
	// What was asked for is that the slot no longer be held, and it is not.
	// Reporting a failure here would turn tidying up into an error on the one
	// path where the record has already been removed and cannot be retried.
	server := newFakeServer(t, map[string]any{
		"pane.close": &responseError{Code: "pane_not_found", Message: "no such pane"},
	})

	if err := server.adapter().Dispose(context.Background(), harness.DisposeRequest{Ref: "gone"}); err != nil {
		t.Errorf("Dispose() = %v, want a pane that has already gone to be a success", err)
	}
}

func TestDisposeReportsARefusalItCannotExplainAway(t *testing.T) {
	t.Parallel()
	server := newFakeServer(t, map[string]any{
		"pane.close": &responseError{Code: "permission_denied", Message: "not yours to close"},
	})

	err := server.adapter().Dispose(context.Background(), harness.DisposeRequest{Ref: "p7"})
	if err == nil {
		t.Fatal("Dispose() = nil, want a refusal to be reported")
	}
	if !strings.Contains(err.Error(), "not yours to close") {
		t.Errorf("Dispose() error = %q, want it to carry what herdr said", err)
	}
}
