package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rwmyers/agent-director/director"
	"github.com/rwmyers/agent-director/harness"
)

// The tests in this file do not run in parallel: they share os.Stdout, the
// harness registry and the package's prompter hook. They use the helpers in
// setup_test.go.

// fleetAdapter is a harness that accepts spawns and lets a test say which of
// the resulting engagements is still alive.
//
// Lifecycle is what remove refuses on, and it is observed rather than read off
// the stored row, so a test about the guard has to be able to make one
// engagement live and leave another finished.
type fleetAdapter struct {
	fakeInstaller
	name string
	// live holds the refs the harness should report as still working.
	live map[string]bool
	// stopErr makes every stop fail, which is a different thing from a stop
	// that worked: after it the agent may still be running.
	stopErr error
	stopped []string
	// sends records what was prompted into a conversation, which is how a
	// test sees a ring that landed on the director's own host.
	sends []harness.SendRequest
}

func (a *fleetAdapter) Name() string                         { return a.name }
func (a *fleetAdapter) Enforceable() []harness.Capability    { return harness.AllCapabilities }
func (a *fleetAdapter) Permits(_ []harness.Capability) error { return nil }

func (a *fleetAdapter) Spawn(_ context.Context, req harness.SpawnRequest) (harness.SpawnResult, error) {
	return harness.SpawnResult{Ref: "ref-" + req.ID}, nil
}

func (a *fleetAdapter) Send(_ context.Context, req harness.SendRequest) error {
	a.sends = append(a.sends, req)
	return nil
}

func (a *fleetAdapter) Stop(_ context.Context, req harness.StopRequest) error {
	if a.stopErr != nil {
		return a.stopErr
	}
	a.stopped = append(a.stopped, req.Ref)
	delete(a.live, req.Ref)
	return nil
}

func (a *fleetAdapter) Get(_ context.Context, ref string) (harness.Observation, error) {
	lifecycle := harness.LifecycleDone
	if a.live[ref] {
		lifecycle = harness.LifecycleWorking
	}
	return harness.Observation{
		Ref: ref, Found: true, Lifecycle: lifecycle, LastActivityAt: time.Now(),
	}, nil
}

func (a *fleetAdapter) List(context.Context, harness.Filter) ([]harness.Observation, error) {
	return nil, nil
}

// fleetRoot establishes a scratch root that spawns onto a controllable harness.
//
// Autodetection is turned off in the written config for the same reason
// spawnRoot turns it off: it is ambient, and a suite run inside a herdr pane
// would otherwise put these engagements in real panes beside it.
func fleetRoot(t *testing.T) (string, *fleetAdapter) {
	t.Helper()
	scratchEnv(t)
	root := t.TempDir()
	adapter := &fleetAdapter{name: "fake-fleet", live: map[string]bool{}}
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
	return root, adapter
}

// spawnEngagement starts one engagement and returns its identifier.
func spawnEngagement(t *testing.T, root, title string) string {
	t.Helper()
	before := fleet(t, root)
	if err := runDirector(t, "spawn", "--config", root,
		"--task", "investigate", "--title", title, "look at it"); err != nil {
		t.Fatalf("spawn %q = %v, want no error", title, err)
	}
	for id := range fleet(t, root) {
		if _, existed := before[id]; !existed {
			return id
		}
	}
	t.Fatalf("spawn %q left no new engagement", title)
	return ""
}

// fleet is what the root's single director currently holds, by identifier.
func fleet(t *testing.T, root string) map[string]*director.Engagement {
	t.Helper()
	roots, err := director.ResolveRoots(root, root)
	if err != nil {
		t.Fatalf("ResolveRoots(%s) = %v", root, err)
	}
	d, err := director.Open(roots, "", director.SystemClock)
	if err != nil {
		t.Fatalf("Open(%s) = %v", root, err)
	}
	return d.State.Engagements
}

// observedHealth is the health `director status` would show for one engagement.
func observedHealth(t *testing.T, root, id string) director.Health {
	t.Helper()
	roots, err := director.ResolveRoots(root, root)
	if err != nil {
		t.Fatalf("ResolveRoots(%s) = %v", root, err)
	}
	d, err := director.Open(roots, "", director.SystemClock)
	if err != nil {
		t.Fatalf("Open(%s) = %v", root, err)
	}
	engagements, err := d.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() = %v", err)
	}
	for _, engagement := range engagements {
		if engagement.ID == id {
			return engagement.Health
		}
	}
	t.Fatalf("Status() does not hold %s", id)
	return ""
}

// setRemovePrompter makes the pick list ask with a prompter of the test's
// choosing.
func setRemovePrompter(t *testing.T, ask prompter) {
	t.Helper()
	saved := removePrompter
	removePrompter = func() prompter { return ask }
	t.Cleanup(func() { removePrompter = saved })
}

func TestRemoveSeveralEngagementsAtOnce(t *testing.T) {
	root, _ := fleetRoot(t)
	first := spawnEngagement(t, root, "the first to forget")
	second := spawnEngagement(t, root, "the second to forget")
	keeper := spawnEngagement(t, root, "the one to keep")

	stop := captureStdout(t)
	err := runDirector(t, "remove", "--config", root, first, second)
	out := stop()
	if err != nil {
		t.Fatalf("remove = %v, want no error", err)
	}

	remaining := fleet(t, root)
	if len(remaining) != 1 {
		t.Fatalf("state holds %d engagements, want 1", len(remaining))
	}
	if _, ok := remaining[keeper]; !ok {
		t.Errorf("remove took %s as well, want only the two named", keeper)
	}
	// Reporting one of two removals would be worse than reporting neither:
	// nothing else afterwards can say what went.
	for _, id := range []string{first, second} {
		if !strings.Contains(out, id) {
			t.Errorf("remove said %q, want it to name %s", out, id)
		}
	}
}

func TestRemoveTheSameEngagementTwiceIsOneRemoval(t *testing.T) {
	root, _ := fleetRoot(t)
	id := spawnEngagement(t, root, "named twice")

	if err := runDirector(t, "remove", "--config", root, id, id); err != nil {
		t.Errorf("remove = %v, want a repeated identifier to be one removal, not a removal and a miss", err)
	}
}

func TestRemoveKeepsGoingPastAnUnknownIdentifier(t *testing.T) {
	// Best-effort, the same semantics retire has: the point of naming a batch
	// is clearing a long list, and one identifier typed wrong should not send
	// somebody back to remove the rest one at a time.
	root, _ := fleetRoot(t)
	first := spawnEngagement(t, root, "the first to forget")
	second := spawnEngagement(t, root, "the second to forget")

	err := runDirector(t, "remove", "--config", root, first, "eng_nosuchthing", second)
	if err == nil {
		t.Fatal("remove = nil error with an unknown identifier in the list, want a refusal")
	}
	if !errors.Is(err, director.ErrNotFound) {
		t.Errorf("remove = %v, want ErrNotFound so the exit code says so", err)
	}
	if !strings.Contains(err.Error(), "eng_nosuchthing") {
		t.Errorf("remove error = %q, want it to name the identifier that missed", err)
	}
	if remaining := fleet(t, root); len(remaining) != 0 {
		t.Errorf("state holds %d engagements, want the two good ones removed anyway", len(remaining))
	}
}

func TestRemoveStillRefusesALiveEngagementInABatch(t *testing.T) {
	// Naming several must not be a way past the guard that naming one
	// enforces: every identifier is checked as if it had been given alone.
	root, adapter := fleetRoot(t)
	finished := spawnEngagement(t, root, "the finished one")
	running := spawnEngagement(t, root, "the running one")
	adapter.live["ref-"+running] = true

	err := runDirector(t, "remove", "--config", root, finished, running)
	if err == nil {
		t.Fatal("remove = nil error with a live engagement in the list, want a refusal")
	}
	if !strings.Contains(err.Error(), running) {
		t.Errorf("remove error = %q, want it to name what is still running", err)
	}
	for _, flag := range []string{"--stop", "--force"} {
		if !strings.Contains(err.Error(), flag) {
			t.Errorf("remove error = %q, want it to offer %s", err, flag)
		}
	}

	remaining := fleet(t, root)
	if _, ok := remaining[running]; !ok {
		t.Errorf("remove forgot %s despite refusing it", running)
	}
	if _, ok := remaining[finished]; ok {
		t.Errorf("remove kept %s, want the rest of the batch removed anyway", finished)
	}
}

func TestRemoveReportsHowManyOfHowManyFailed(t *testing.T) {
	root, adapter := fleetRoot(t)
	good := spawnEngagement(t, root, "the finished one")
	running := spawnEngagement(t, root, "the running one")
	adapter.live["ref-"+running] = true

	err := runDirector(t, "remove", "--config", root, good, running, "eng_nosuchthing")
	if err == nil {
		t.Fatal("remove = nil error, want a refusal naming both failures")
	}
	if !strings.Contains(err.Error(), "2 of 3") {
		t.Errorf("remove error = %q, want it to count the failures against what was asked for", err)
	}
	for _, want := range []string{running, "eng_nosuchthing"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("remove error = %q, want it to name %s", err, want)
		}
	}
}

func TestRemoveWithoutATerminalRefusesRatherThanAsking(t *testing.T) {
	// A pick list needs somebody to pick. From a script there is nobody, and a
	// command that blocks on stdin there is worse than one that fails: the
	// reader below fails the test if anything tries to read an answer.
	root, _ := fleetRoot(t)
	id := spawnEngagement(t, root, "the one to keep")
	setRemovePrompter(t, prompter{in: refusingReader{t: t}, out: io.Discard, terminal: false})

	err := runDirector(t, "remove", "--config", root)
	if err == nil {
		t.Fatal("remove with no arguments off a terminal = nil error, want a refusal")
	}
	if !strings.Contains(err.Error(), "director remove <engagement>") {
		t.Errorf("remove error = %q, want it to say which engagement to name", err)
	}
	if _, ok := fleet(t, root)[id]; !ok {
		t.Errorf("remove took %s while refusing to ask", id)
	}
}

func TestRemovePickListTakesEverythingChosen(t *testing.T) {
	root, _ := fleetRoot(t)
	spawnEngagement(t, root, "the first to forget")
	spawnEngagement(t, root, "the second to forget")

	// Both options toggled on, 0 to finish, then the confirmation.
	setRemovePrompter(t, answering("1\n2\n0\ny\n"))

	if err := runDirector(t, "remove", "--config", root); err != nil {
		t.Fatalf("remove = %v, want no error", err)
	}
	if remaining := fleet(t, root); len(remaining) != 0 {
		t.Errorf("state holds %d engagements, want both picked ones removed", len(remaining))
	}
}

func TestRemovePickListDeclinedRemovesNothing(t *testing.T) {
	root, _ := fleetRoot(t)
	spawnEngagement(t, root, "the one to keep")
	setRemovePrompter(t, answering("1\n0\nn\n"))

	if err := runDirector(t, "remove", "--config", root); err != nil {
		t.Fatalf("remove = %v, want backing out to be a normal outcome", err)
	}
	if remaining := fleet(t, root); len(remaining) != 1 {
		t.Errorf("state holds %d engagements, want the declined removal not to have happened", len(remaining))
	}
}

func TestRemovePickListShowsWhatDistinguishesTheRows(t *testing.T) {
	root, adapter := fleetRoot(t)
	id := spawnEngagement(t, root, "the running one")
	adapter.live["ref-"+id] = true

	// Accessible mode prints the options it is offering, so the rows a person
	// would be choosing between can be read back here.
	shown := &strings.Builder{}
	setRemovePrompter(t, prompter{in: strings.NewReader("0\n"), out: shown, terminal: true, accessible: true})
	if err := runDirector(t, "remove", "--config", root); err != nil {
		t.Fatalf("remove = %v, want no error", err)
	}

	engagement := fleet(t, root)[id]
	for _, want := range []string{shortID(id), "the running one", string(engagement.Health), "still running"} {
		if !strings.Contains(shown.String(), want) {
			t.Errorf("pick list showed %q, want it to include %q", shown.String(), want)
		}
	}
}

func TestRemoveJSONDescribesEveryRemoval(t *testing.T) {
	root, _ := fleetRoot(t)
	first := spawnEngagement(t, root, "the first to forget")
	second := spawnEngagement(t, root, "the second to forget")

	stop := captureStdout(t)
	err := runDirector(t, "remove", "--config", root, "--json", first, second)
	out := stop()
	if err != nil {
		t.Fatalf("remove = %v, want no error", err)
	}

	var removed []director.RemoveResult
	if err := json.Unmarshal([]byte(out), &removed); err != nil {
		t.Fatalf("parsing %q = %v, want a list of removals", out, err)
	}
	if len(removed) != 2 {
		t.Fatalf("remove --json described %d removals, want 2", len(removed))
	}
	got := map[string]string{}
	for _, result := range removed {
		got[result.EngagementID] = result.Title
	}
	if got[first] != "the first to forget" || got[second] != "the second to forget" {
		t.Errorf("remove --json = %v, want both engagements described", got)
	}
}

func TestRemoveOneEngagementReportsItTheSameWayAsBefore(t *testing.T) {
	// The single-identifier form is what everything already written uses, so
	// its one line has to stay exactly what it was.
	root, _ := fleetRoot(t)
	id := spawnEngagement(t, root, "the one to forget")
	// Health is judged when the fleet is observed, not stored on the row, so
	// the expected line has to be built from the same judgement.
	health := observedHealth(t, root, id)

	stop := captureStdout(t)
	err := runDirector(t, "remove", "--config", root, id)
	out := stop()
	if err != nil {
		t.Fatalf("remove = %v, want no error", err)
	}

	want := "removed " + id + "  the one to forget  (" + string(health) + ", -)\n"
	if out != want {
		t.Errorf("remove said %q, want %q", out, want)
	}
}

// panedAdapter is a fleet harness that also has a slot to give back, so the
// removal path can be driven end to end without going anywhere near herdr.
//
// It is a separate harness from fleetAdapter rather than a flag on it, because
// the tests above assert what a removal on a harness with no slot prints, and
// that has to keep meaning what it means.
type panedAdapter struct {
	*fleetAdapter
	// failFor names the slots this harness refuses to close, standing in for a
	// pane that has gone or a server that has stopped.
	failFor map[string]bool
	// seat is the conversation this harness reports the running process as
	// sitting in, which is how a director inside a pane finds its own.
	seat     string
	disposed []string
}

func (a *panedAdapter) Locate() (string, bool) {
	if a.seat == "" {
		return "", false
	}
	return a.seat, true
}

func (a *panedAdapter) Disposes() bool { return true }

func (a *panedAdapter) Dispose(_ context.Context, req harness.DisposeRequest) error {
	if a.failFor[req.Ref] {
		return errors.New("herdr server is not running")
	}
	a.disposed = append(a.disposed, req.Ref)
	return nil
}

// panedRoot establishes a scratch root spawning onto a harness with slots.
//
// Autodetection is turned off in the written config for the same reason
// fleetRoot turns it off, and it matters more here: this suite exercises code
// whose whole purpose is closing panes, and a run that picked up the live herdr
// session would be closing somebody's real ones.
func panedRoot(t *testing.T) (string, *panedAdapter) {
	t.Helper()
	scratchEnv(t)
	root := t.TempDir()
	adapter := &panedAdapter{
		fleetAdapter: &fleetAdapter{name: "fake-paned", live: map[string]bool{}},
		failFor:      map[string]bool{},
	}
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
	return root, adapter
}

func TestRemoveReclaimsEverySlotInTheBatch(t *testing.T) {
	root, adapter := panedRoot(t)
	first := spawnEngagement(t, root, "the first to forget")
	second := spawnEngagement(t, root, "the second to forget")

	stop := captureStdout(t)
	err := runDirector(t, "remove", "--config", root, first, second)
	out := stop()
	if err != nil {
		t.Fatalf("remove = %v, want no error", err)
	}

	want := []string{"ref-" + first, "ref-" + second}
	if len(adapter.disposed) != 2 {
		t.Fatalf("remove reclaimed %v, want %v", adapter.disposed, want)
	}
	for _, ref := range want {
		if !slices.Contains(adapter.disposed, ref) {
			t.Errorf("remove reclaimed %v, want it to include %s", adapter.disposed, ref)
		}
	}
	if got := strings.Count(out, "closed its fake-paned slot"); got != 2 {
		t.Errorf("remove said %q, want both slots reported closed", out)
	}
}

func TestRemoveKeepsGoingWhenOneSlotWillNotClose(t *testing.T) {
	// One slot that will not close must not silently skip the rest, and must
	// not fail the removal either: the rows have gone and running the command
	// again would only report that nothing matches.
	root, adapter := panedRoot(t)
	stubborn := spawnEngagement(t, root, "the one whose pane is gone")
	ordinary := spawnEngagement(t, root, "the ordinary one")
	adapter.failFor["ref-"+stubborn] = true

	stop := captureStdout(t)
	err := runDirector(t, "remove", "--config", root, stubborn, ordinary)
	out := stop()
	if err != nil {
		t.Fatalf("remove = %v, want a slot that would not close not to fail the removal", err)
	}

	if remaining := fleet(t, root); len(remaining) != 0 {
		t.Errorf("state holds %d engagements, want both rows removed", len(remaining))
	}
	if len(adapter.disposed) != 1 || adapter.disposed[0] != "ref-"+ordinary {
		t.Errorf("remove reclaimed %v, want the other slot still given back", adapter.disposed)
	}
	if !strings.Contains(out, "could not be closed") || !strings.Contains(out, "herdr server is not running") {
		t.Errorf("remove said %q, want it to report the slot it left behind and why", out)
	}
	if !strings.Contains(out, "closed its fake-paned slot") {
		t.Errorf("remove said %q, want the slot that did close reported too", out)
	}
}

func TestRemoveJSONSaysWhatHappenedToEachSlot(t *testing.T) {
	root, adapter := panedRoot(t)
	closed := spawnEngagement(t, root, "the closed one")
	failed := spawnEngagement(t, root, "the stuck one")
	adapter.failFor["ref-"+failed] = true

	stop := captureStdout(t)
	err := runDirector(t, "remove", "--config", root, "--json", closed, failed)
	out := stop()
	if err != nil {
		t.Fatalf("remove = %v, want no error", err)
	}

	var removed []director.RemoveResult
	if err := json.Unmarshal([]byte(out), &removed); err != nil {
		t.Fatalf("parsing %q = %v, want a list of removals", out, err)
	}
	got := map[string]director.RemoveResult{}
	for _, result := range removed {
		got[result.EngagementID] = result
	}
	if got[closed].Disposal != director.DisposalClosed {
		t.Errorf("remove --json reported %q for the closed one, want %q",
			got[closed].Disposal, director.DisposalClosed)
	}
	if got[failed].Disposal != director.DisposalFailed {
		t.Errorf("remove --json reported %q for the stuck one, want %q",
			got[failed].Disposal, director.DisposalFailed)
	}
	if !strings.Contains(got[failed].DisposalReason, "herdr server is not running") {
		t.Errorf("remove --json gave the reason %q, want what the harness said",
			got[failed].DisposalReason)
	}
}

func TestRemoveForceLeavesTheSlotAloneAcrossTheBatch(t *testing.T) {
	// --force forgets an engagement while deliberately leaving its agent
	// running and unreachable. Closing the pane would kill the thing the flag
	// exists to preserve, and the flag applies to everything named.
	root, adapter := panedRoot(t)
	running := spawnEngagement(t, root, "the running one")
	finished := spawnEngagement(t, root, "the finished one")
	adapter.live["ref-"+running] = true

	stop := captureStdout(t)
	err := runDirector(t, "remove", "--config", root, "--json", "--force", running, finished)
	out := stop()
	if err != nil {
		t.Fatalf("remove = %v, want no error", err)
	}

	if len(adapter.disposed) != 0 {
		t.Fatalf("--force closed %v, want every slot left alone", adapter.disposed)
	}
	var removed []director.RemoveResult
	if err := json.Unmarshal([]byte(out), &removed); err != nil {
		t.Fatalf("parsing %q = %v, want a list of removals", out, err)
	}
	if len(removed) != 2 {
		t.Fatalf("remove --json described %d removals, want 2", len(removed))
	}
	for _, result := range removed {
		if result.Disposal != director.DisposalKept {
			t.Errorf("remove --json reported %q for %s, want %q",
				result.Disposal, result.EngagementID, director.DisposalKept)
		}
		if !strings.Contains(result.DisposalReason, "--force") {
			t.Errorf("remove --json gave the reason %q, want it to name --force", result.DisposalReason)
		}
	}
}

func TestRemoveStopClosesTheSlotOnlyWhenTheStopSucceeded(t *testing.T) {
	root, adapter := panedRoot(t)
	running := spawnEngagement(t, root, "the running one")
	adapter.live["ref-"+running] = true

	if err := runDirector(t, "remove", "--config", root, "--stop", running); err != nil {
		t.Fatalf("remove = %v, want no error", err)
	}
	if len(adapter.stopped) != 1 {
		t.Fatalf("remove stopped %v, want the engagement ended first", adapter.stopped)
	}
	if len(adapter.disposed) != 1 || adapter.disposed[0] != "ref-"+running {
		t.Errorf("remove reclaimed %v, want the slot given back after a successful stop", adapter.disposed)
	}
}

func TestRemoveStopLeavesTheSlotAloneWhenTheStopFailed(t *testing.T) {
	// The agent may still be alive in there, and closing the pane would destroy
	// the work the failed stop did not end.
	root, adapter := panedRoot(t)
	running := spawnEngagement(t, root, "the unstoppable one")
	adapter.live["ref-"+running] = true
	adapter.stopErr = errors.New("the harness would not end it")

	stop := captureStdout(t)
	if err := runDirector(t, "remove", "--config", root, "--stop", running); err != nil {
		t.Fatalf("remove = %v, want no error", err)
	}
	out := stop()

	if len(adapter.disposed) != 0 {
		t.Fatalf("a failed stop closed %v, want the slot left alone", adapter.disposed)
	}
	if !strings.Contains(out, "stop did not succeed") {
		t.Errorf("remove said %q, want it to say why the slot was left alone", out)
	}
}

func TestRemoveNeverClosesTheDirectorsOwnSlot(t *testing.T) {
	// A director very often runs inside the harness it dispatches to. A removal
	// that reached its own seat would kill the session issuing the command.
	root, adapter := panedRoot(t)
	id := spawnEngagement(t, root, "the seat this director occupies")

	// The harness now reports this process as sitting in exactly the pane the
	// engagement occupies, which is the arrangement the guard exists for.
	adapter.seat = "ref-" + id

	stop := captureStdout(t)
	if err := runDirector(t, "remove", "--config", root, id); err != nil {
		t.Fatalf("remove = %v, want no error", err)
	}
	out := stop()

	if len(adapter.disposed) != 0 {
		t.Fatalf("remove closed the director's own slot: %v", adapter.disposed)
	}
	if !strings.Contains(out, "director itself") {
		t.Errorf("remove said %q, want it to say why the slot was left alone", out)
	}
	if remaining := fleet(t, root); len(remaining) != 0 {
		t.Errorf("state holds %d engagements, want the row removed regardless", len(remaining))
	}
}
