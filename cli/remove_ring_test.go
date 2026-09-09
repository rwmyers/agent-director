package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/rwmyers/agent-director/director"
	"github.com/rwmyers/agent-director/harness"
)

// The tests in this file do not run in parallel: they share os.Stdout and the
// harness registry with the rest of the package. They use the helpers in
// setup_test.go and remove_test.go.

// hostPane is the address a ring would be typed into. It is not any
// engagement's ref, so nothing here can be confused for the director closing
// its own slot.
const hostPane = "host:pane"

// attachTo writes a host onto the root's director, as `director attach` would
// have done when a conversation claimed it, and tells the harness that seat is
// a live conversation.
//
// Written straight into the state file rather than run through attach, because
// attach re-detects where THIS process is sitting — and the suite must not
// assert about the herdr pane or Claude Code conversation that happens to be
// running it.
//
// The seat is marked live because the ring path resolves the recorded address
// against the harness before it types into it. A fixture that attached to an
// address the harness reports as finished is describing a conversation that has
// ended, which is a case this file tests deliberately rather than by accident.
func attachTo(t *testing.T, root string, adapter *fleetAdapter, hosting harness.Hosting, ref string) {
	t.Helper()
	adapter.live[ref] = true
	roots, err := director.ResolveRoots(root, root)
	if err != nil {
		t.Fatalf("ResolveRoots(%s) = %v", root, err)
	}
	d, err := director.Open(roots, "", director.SystemClock)
	if err != nil {
		t.Fatalf("Open(%s) = %v", root, err)
	}
	d.State.AttachedAt = time.Now()
	d.State.Host = director.Host{
		Harness: "fake-fleet",
		Ref:     ref,
		Hosting: hosting,
		Source:  director.HostDetected,
	}
	if err := d.State.Save(); err != nil {
		t.Fatalf("Save() = %v, want no error", err)
	}
}

// ringsTo returns the prompts a removal typed at the director's own host,
// which is what separates a ring from anything else the adapter was sent.
func ringsTo(adapter *fleetAdapter, ref string) []harness.SendRequest {
	var found []harness.SendRequest
	for _, send := range adapter.sends {
		if send.Ref == ref {
			found = append(found, send)
		}
	}
	return found
}

func TestRemoveFromTheConsoleRingsTheAttachedDirector(t *testing.T) {
	root, adapter := fleetRoot(t)
	id := spawnEngagement(t, root, "the one it still believes in")
	// herdr: a pane is a shell prompt that can be typed into, so something
	// else can make this director take a turn.
	attachTo(t, root, adapter, harness.Hosting{Background: true, Wake: true}, hostPane)

	stop := captureStdout(t)
	err := runDirector(t, "remove", "--config", root, id)
	out := stop()
	if err != nil {
		t.Fatalf("remove = %v, want no error", err)
	}

	rings := ringsTo(adapter, hostPane)
	if len(rings) != 1 {
		t.Fatalf("rings = %d, want exactly one", len(rings))
	}
	if !strings.Contains(rings[0].Text, id) {
		t.Errorf("ring = %q, want it to name %s", rings[0].Text, id)
	}
	// The person at the console should know a line has just been typed into
	// somebody's live conversation.
	if !strings.Contains(out, "rang its director") {
		t.Errorf("remove said %q, want it to say the director was rung", out)
	}
}

func TestRemoveSaysWhenTheDirectorCannotBeWoken(t *testing.T) {
	root, adapter := fleetRoot(t)
	id := spawnEngagement(t, root, "the one it will go on believing in")
	// Claude Code: it can background a command and be re-entered when that
	// exits, and nothing can push a turn into it.
	attachTo(t, root, adapter, harness.Hosting{Background: true, Wake: false}, "session-1")

	stop := captureStdout(t)
	err := runDirector(t, "remove", "--config", root, id)
	out := stop()
	if err != nil {
		t.Fatalf("remove = %v, want the removal itself to succeed", err)
	}

	if sent := adapter.sends; len(sent) != 0 {
		t.Errorf("sends = %d (%v), want none: this host declares no wake", len(sent), sent)
	}
	// Silence here would be the failure the whole path exists to avoid: a
	// conversation is sitting there holding a fleet that no longer matches,
	// and the person at the console is the only one who can be told.
	for _, want := range []string{"could not tell its director", "director status"} {
		if !strings.Contains(out, want) {
			t.Errorf("remove said %q, want it to contain %q", out, want)
		}
	}
	if !strings.Contains(out, "removed "+id) {
		t.Errorf("remove said %q, want the removal itself still reported", out)
	}
}

func TestRemoveSaysNothingAboutRingingWhenNobodyIsAttached(t *testing.T) {
	root, adapter := fleetRoot(t)
	id := spawnEngagement(t, root, "nobody is watching this")

	stop := captureStdout(t)
	err := runDirector(t, "remove", "--config", root, id)
	out := stop()
	if err != nil {
		t.Fatalf("remove = %v, want no error", err)
	}

	if sent := adapter.sends; len(sent) != 0 {
		t.Errorf("sends = %d, want none", len(sent))
	}
	// No conversation is holding a stale picture, so a line under every
	// removal would be noise.
	if strings.Contains(out, "director") && strings.Contains(out, "rang") {
		t.Errorf("remove said %q, want nothing about ringing", out)
	}
}

func TestRemovingSeveralRingsOnce(t *testing.T) {
	root, adapter := fleetRoot(t)
	first := spawnEngagement(t, root, "the first to forget")
	second := spawnEngagement(t, root, "the second to forget")
	attachTo(t, root, adapter, harness.Hosting{Background: true, Wake: true}, hostPane)

	stop := captureStdout(t)
	err := runDirector(t, "remove", "--config", root, first, second)
	_ = stop()
	if err != nil {
		t.Fatalf("remove = %v, want no error", err)
	}

	// One human action, one line typed into the conversation.
	rings := ringsTo(adapter, hostPane)
	if len(rings) != 1 {
		t.Fatalf("rings = %d, want exactly one for one invocation", len(rings))
	}
	for _, id := range []string{first, second} {
		if !strings.Contains(rings[0].Text, id) {
			t.Errorf("ring = %q, want it to name %s", rings[0].Text, id)
		}
	}
}
