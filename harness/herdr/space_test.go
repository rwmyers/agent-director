package herdr

import (
	"context"
	"strings"
	"testing"

	"github.com/rwmyers/agent-director/harness"
)

// spawnRequest is a spawn with nothing interesting in it, for the tests that
// are about where the agent lands rather than what it is.
func spawnRequest() harness.SpawnRequest {
	return harness.SpawnRequest{
		ID: "eng_abc", Dir: "/tmp/x", Name: "auth-review",
		Allow: harness.AllCapabilities,
	}
}

func TestSpawnLandsInTheDirectorsOwnWindow(t *testing.T) {
	t.Parallel()
	// The bug this exists for: with no workspace_id, herdr creates the tab in
	// whatever window is focused, so a director's fleet scattered across the
	// session according to where the person happened to be looking when each
	// spawn fired.
	server := newFakeServer(t, map[string]any{
		"pane.get":   paneInfoReply("w8:p1", "w8"),
		"tab.create": tabCreatedReply("w8:t2", "w8:pF"),
	})

	result, err := server.adapterInPane("w8:p1", true).Spawn(context.Background(), spawnRequest())
	if err != nil {
		t.Fatalf("Spawn() = %v, want no error", err)
	}
	if got := server.params("tab.create")["workspace_id"]; got != "w8" {
		t.Errorf("tab.create workspace_id = %v, want the director's own window %q", got, "w8")
	}
	if result.Detail["workspace_id"] != "w8" {
		t.Errorf("Detail[workspace_id] = %q, want %q recorded on the engagement",
			result.Detail["workspace_id"], "w8")
	}
	// The window is asked for, not assumed: nothing here may read a window out
	// of the shape of a pane id.
	if got := server.params("pane.get")["pane_id"]; got != "w8:p1" {
		t.Errorf("pane.get pane_id = %v, want the director's own pane", got)
	}
}

func TestPlacementIgnoresWhereTheSessionIsFocused(t *testing.T) {
	t.Parallel()
	// The person is in w9 and stays there. Two spawns a minute apart, with the
	// focus somewhere else the whole time, have to land in the same window —
	// the director's — rather than following the cursor.
	server := newFakeServer(t, map[string]any{
		"pane.get":   paneInfoReply("w8:p1", "w8"),
		"tab.create": tabCreatedReply("w8:t2", "w8:pF"),
		"workspace.list": workspaceListReply(
			workspaceReply("w8", "agent-director"),
			focused(workspaceReply("w9", "orchard")),
		),
	})
	adapter := server.adapterInPane("w8:p1", true)

	for attempt := 1; attempt <= 2; attempt++ {
		if _, err := adapter.Spawn(context.Background(), spawnRequest()); err != nil {
			t.Fatalf("spawn %d = %v, want no error", attempt, err)
		}
	}
	for _, call := range server.calls {
		if call.Method != "tab.create" {
			continue
		}
		params, _ := call.Params.(map[string]any)
		if params["workspace_id"] != "w8" {
			t.Errorf("tab.create workspace_id = %v, want %q every time", params["workspace_id"], "w8")
		}
	}
}

// focused marks a workspace fixture as the one herdr is showing.
func focused(workspace map[string]any) map[string]any {
	workspace["focused"] = true
	return workspace
}

func TestSpawnDoesNotTakeTheFocus(t *testing.T) {
	t.Parallel()
	// Putting an agent in the right window must not drag the person to it. They
	// may be reading something in another space, and a spawn is not a reason to
	// move them.
	t.Run("into the director's own window", func(t *testing.T) {
		t.Parallel()
		server := newFakeServer(t, map[string]any{
			"pane.get":   paneInfoReply("w8:p1", "w8"),
			"tab.create": tabCreatedReply("w8:t2", "w8:pF"),
		})
		if _, err := server.adapterInPane("w8:p1", true).Spawn(context.Background(), spawnRequest()); err != nil {
			t.Fatalf("Spawn() = %v, want no error", err)
		}
		if got := server.params("tab.create")["focus"]; got != false {
			t.Errorf("tab.create focus = %v, want false", got)
		}
	})

	t.Run("into a window it had to make", func(t *testing.T) {
		t.Parallel()
		server := newFakeServer(t, map[string]any{
			"workspace.list":   workspaceListReply(focused(workspaceReply("w9", "orchard"))),
			"workspace.create": workspaceCreatedReply("w12"),
			"tab.create":       tabCreatedReply("w12:t2", "w12:pF"),
		})
		if _, err := server.adapter().Spawn(context.Background(), spawnRequest()); err != nil {
			t.Fatalf("Spawn() = %v, want no error", err)
		}
		if got := server.params("workspace.create")["focus"]; got != false {
			t.Errorf("workspace.create focus = %v, want false", got)
		}
		if got := server.params("tab.create")["focus"]; got != false {
			t.Errorf("tab.create focus = %v, want false", got)
		}
	})
}

func TestADirectorWithNoPaneUsesDirectorsOwnWindow(t *testing.T) {
	t.Parallel()
	// A headless or claude-code-hosted director has no pane to sit beside. It
	// places by a rule rather than by what is on screen.

	t.Run("reusing the window when it is already there", func(t *testing.T) {
		t.Parallel()
		server := newFakeServer(t, map[string]any{
			"workspace.list": workspaceListReply(
				focused(workspaceReply("w9", "orchard")),
				workspaceReply("w4", fleetWorkspaceLabel),
			),
			"tab.create": tabCreatedReply("w4:t2", "w4:pF"),
		})
		if _, err := server.adapter().Spawn(context.Background(), spawnRequest()); err != nil {
			t.Fatalf("Spawn() = %v, want no error", err)
		}
		if got := server.params("tab.create")["workspace_id"]; got != "w4" {
			t.Errorf("tab.create workspace_id = %v, want the existing %q window %q", got, fleetWorkspaceLabel, "w4")
		}
		for _, method := range server.methods() {
			if method == "workspace.create" {
				t.Error("Spawn() made a second window rather than reusing the one already labelled for director")
			}
		}
	})

	t.Run("making the window when it is not", func(t *testing.T) {
		t.Parallel()
		server := newFakeServer(t, map[string]any{
			"workspace.list":   workspaceListReply(focused(workspaceReply("w9", "orchard"))),
			"workspace.create": workspaceCreatedReply("w12"),
			"tab.create":       tabCreatedReply("w12:t2", "w12:pF"),
		})
		if _, err := server.adapter().Spawn(context.Background(), spawnRequest()); err != nil {
			t.Fatalf("Spawn() = %v, want no error", err)
		}
		if got := server.params("workspace.create")["label"]; got != fleetWorkspaceLabel {
			t.Errorf("workspace.create label = %v, want %q", got, fleetWorkspaceLabel)
		}
		if got := server.params("tab.create")["workspace_id"]; got != "w12" {
			t.Errorf("tab.create workspace_id = %v, want the window just made %q", got, "w12")
		}
	})
}

func TestAWindowThatHasGoneFailsTheSpawnLegibly(t *testing.T) {
	t.Parallel()
	// The director outlived the pane its environment says it occupies. Falling
	// back to some other window would be the scattering this exists to remove,
	// unexplained as well as wrong — so the spawn is refused, and the refusal
	// names the pane.
	server := newFakeServer(t, map[string]any{
		"pane.get": &responseError{Code: "pane_not_found", Message: "no pane w8:p1"},
	})

	_, err := server.adapterInPane("w8:p1", true).Spawn(context.Background(), spawnRequest())
	if err == nil {
		t.Fatal("Spawn() = nil error with the director's window gone, want a refusal")
	}
	if !strings.Contains(err.Error(), "w8:p1") {
		t.Errorf("Spawn() error = %q, want it to name the pane that has gone", err)
	}
	for _, method := range server.methods() {
		if method == "tab.create" {
			t.Error("Spawn() created a tab anyway; a failed anchor must not scatter the agent")
		}
	}
}

func TestAPaneWithNoIdFallsBackToDirectorsOwnWindow(t *testing.T) {
	t.Parallel()
	// herdr can say a process is in a pane without naming it. There is then no
	// anchor rather than a lost one, so the rule for a director with no pane
	// applies — and it must not be mistaken for the window having vanished.
	server := newFakeServer(t, map[string]any{
		"workspace.list": workspaceListReply(workspaceReply("w4", fleetWorkspaceLabel)),
		"tab.create":     tabCreatedReply("w4:t2", "w4:pF"),
	})

	if _, err := server.adapterInPane("", true).Spawn(context.Background(), spawnRequest()); err != nil {
		t.Fatalf("Spawn() = %v, want no error", err)
	}
	if got := server.params("tab.create")["workspace_id"]; got != "w4" {
		t.Errorf("tab.create workspace_id = %v, want %q", got, "w4")
	}
}

func TestAnchorUsesTheSameSelfLocatorAsRemoval(t *testing.T) {
	// No t.Parallel: this sets the process environment.
	//
	// `director remove` refuses to close the director's own pane by asking
	// Locate. Placement asks the same question, and a second way of asking it
	// would eventually answer differently — an engagement in the wrong window,
	// or a removal closing the pane the director is sitting in.
	server := newFakeServer(t, map[string]any{
		"pane.get":   paneInfoReply("w6:p1", "w6"),
		"tab.create": tabCreatedReply("w6:t2", "w6:pF"),
	})
	adapter := &Adapter{Socket: server.path, Kind: defaultKind}
	t.Setenv(EnvInPane, "1")
	t.Setenv(EnvPane, "w6:p1")

	if _, err := adapter.Spawn(context.Background(), spawnRequest()); err != nil {
		t.Fatalf("Spawn() = %v, want no error", err)
	}
	if got := server.params("pane.get")["pane_id"]; got != "w6:p1" {
		t.Errorf("pane.get pane_id = %v, want the pane Locate reports: %q", got, "w6:p1")
	}
	if got := server.params("tab.create")["workspace_id"]; got != "w6" {
		t.Errorf("tab.create workspace_id = %v, want %q", got, "w6")
	}
}
