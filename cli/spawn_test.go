package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rwmyers/agent-director/harness"
)

// The tests in this file do not run in parallel: they change the process
// working directory, and os.Stdout and the harness registry are process-wide.
// They share the helpers in setup_test.go.

// spawnableAdapter is a harness that will actually accept a spawn, and records
// the directory it was told to start the agent in. The other fake in this
// package embeds harness.Adapter and panics on anything it does not stub, which
// is right for the installer tests and useless here.
type spawnableAdapter struct {
	fakeInstaller
	name   string
	spawns []harness.SpawnRequest
}

func (a *spawnableAdapter) Name() string                                    { return a.name }
func (a *spawnableAdapter) Enforceable() []harness.Capability               { return harness.AllCapabilities }
func (a *spawnableAdapter) Permits(_ []harness.Capability) error            { return nil }
func (a *spawnableAdapter) Stop(context.Context, harness.StopRequest) error { return nil }
func (a *spawnableAdapter) Send(context.Context, harness.SendRequest) error { return nil }

func (a *spawnableAdapter) Spawn(_ context.Context, req harness.SpawnRequest) (harness.SpawnResult, error) {
	a.spawns = append(a.spawns, req)
	return harness.SpawnResult{Ref: "ref-" + req.ID}, nil
}

func (a *spawnableAdapter) Get(context.Context, string) (harness.Observation, error) {
	return harness.Observation{Lifecycle: harness.LifecycleWorking}, nil
}

func (a *spawnableAdapter) List(context.Context, harness.Filter) ([]harness.Observation, error) {
	return nil, nil
}

// spawnRoot establishes a scratch root spawning on an adapter that records
// what it was asked for.
//
// Autodetection is turned off in the written config, because it is ambient: a
// suite run inside a herdr pane would otherwise place these engagements in real
// panes beside it, and the test would be asserting about somebody's terminal.
func spawnRoot(t *testing.T, root string) *spawnableAdapter {
	t.Helper()
	adapter := &spawnableAdapter{name: "fake-spawnable"}
	harness.Register(adapter)
	establishRoot(t, root, adapter.name)

	config := filepath.Join(root, "director.conf")
	body, err := os.ReadFile(config)
	if err != nil {
		t.Fatalf("reading %s = %v", config, err)
	}
	if err := os.WriteFile(config, append(body, "\nherdr_autodetect = false\n"...), 0o600); err != nil {
		t.Fatalf("writing %s = %v", config, err)
	}
	return adapter
}

func TestSpawnStartsTheAgentWhereItRuns(t *testing.T) {
	scratchEnv(t)
	root := t.TempDir()
	adapter := spawnRoot(t, root)

	// Somewhere other than the root, so the assertion cannot be satisfied by
	// the configuration rather than by the working directory.
	where := t.TempDir()
	t.Chdir(where)

	if err := runDirector(t, "spawn", "--config", root, "--task", "investigate", "--title", "look", "look at it"); err != nil {
		t.Fatalf("spawn = %v, want no error", err)
	}
	if len(adapter.spawns) != 1 {
		t.Fatalf("spawns = %d, want 1", len(adapter.spawns))
	}
	if got := adapter.spawns[0].Dir; got != where {
		t.Errorf("agent started in %q, want the directory the spawn ran from: %q", got, where)
	}
}

func TestSpawnSaysWhereItPutTheAgent(t *testing.T) {
	// Nothing in the command names the directory any more, so a working
	// directory that drifted a level down would move every agent silently. The
	// output is the only place anybody can see it without reasoning about their
	// shell.
	scratchEnv(t)
	root := t.TempDir()
	spawnRoot(t, root)

	where := t.TempDir()
	t.Chdir(where)

	stop := captureStdout(t)
	err := runDirector(t, "spawn", "--config", root, "--task", "investigate", "--title", "look", "look at it")
	out := stop()
	if err != nil {
		t.Fatalf("spawn = %v, want no error", err)
	}
	if !strings.Contains(out, where) {
		t.Errorf("spawn said %q, want it to name the directory it started the agent in: %q", out, where)
	}
}
