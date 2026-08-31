package herdr

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"strings"
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
		"tab.create": map[string]any{"tab_id": "t1", "pane_id": "p7"},
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

	methods := make([]string, len(server.calls))
	for i, call := range server.calls {
		methods[i] = call.Method
	}
	want := []string{"tab.create", "agent.start", "agent.prompt"}
	if strings.Join(methods, ",") != strings.Join(want, ",") {
		t.Errorf("Spawn() called %v, want %v", methods, want)
	}
}

func TestReadIsAScreenSnapshot(t *testing.T) {
	t.Parallel()
	// herdr keeps a terminal, not a transcript. Saying so is what stops a
	// director concluding an agent was silent when its output simply scrolled
	// away.
	server := newFakeServer(t, map[string]any{
		"pane.read": map[string]any{"pane_id": "p7", "text": "some output", "truncated": true},
	})
	result, err := server.adapter().Read(context.Background(), harness.ReadRequest{Ref: "p7"})
	if err != nil {
		t.Fatalf("Read() = %v, want no error", err)
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
		"tab.create": map[string]any{"tab_id": "t1", "pane_id": "p7"},
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
