package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rwmyers/agent-director/director"
)

// The tests in this file do not run in parallel: os.Stdout, the harness
// registry and the global flag options are all process-wide. They share the
// helpers in setup_test.go.

// scratchEnv makes sure a test cannot reach the configuration root this process
// was itself spawned under, and cannot inherit its director id either. Every
// root here is a temporary directory named outright with --config.
//
// scratchOnly covers the root; DIRECTOR_ID matters separately because these
// tests go on to resolve a director with nothing else to go on, which is the
// property under test.
func scratchEnv(t *testing.T) {
	t.Helper()
	scratchOnly(t)
	t.Setenv(director.EnvID, "")
}

func TestInitTwiceLeavesOneDirector(t *testing.T) {
	// The reported failure. `director init` run a second time in a root
	// registered another director with the same auto-generated name and the
	// same workflow, said so only afterwards and only in prose, and left the
	// root holding two identical-looking directors — after which every command
	// there failed with "more than one director under <root>".
	scratchEnv(t)
	root := t.TempDir()
	establishRoot(t, root, "fake-alpha")

	if err := runInit(t, root, "--harness", "fake-alpha"); err != nil {
		t.Fatalf("second init = %v, want it to adopt rather than fail", err)
	}

	if got := directorCount(t, root); got != 1 {
		t.Errorf("directors after two inits = %d, want 1", got)
	}
	// The consequence that actually bit: a command with nothing to go on has to
	// be able to resolve a director in this root.
	roots, err := director.ResolveRoots(root, root)
	if err != nil {
		t.Fatalf("ResolveRoots(%s) = %v", root, err)
	}
	if _, err := director.Open(roots, "", director.SystemClock); err != nil {
		t.Errorf("Open() with no id = %v, want the root to be unambiguous after two inits", err)
	}
}

func TestInitSaysItAdoptedInProse(t *testing.T) {
	// Reporting after the fact is what let this pass unnoticed. Whatever init
	// decided has to be the first thing it says about the director.
	scratchEnv(t)
	root := t.TempDir()
	establishRoot(t, root, "fake-alpha")

	stop := captureStdout(t)
	err := runInit(t, root, "--harness", "fake-alpha")
	out := stop()
	if err != nil {
		t.Fatalf("second init = %v", err)
	}

	for _, want := range []string{"already registered here", "Nothing was created", "--new"} {
		if !strings.Contains(out, want) {
			t.Errorf("init printed %q, want it to contain %q", out, want)
		}
	}
	if strings.Contains(out, "initialised for workflow") {
		t.Errorf("init printed %q, want it not to claim it initialised anything", out)
	}
}

func TestInitSaysItAdoptedInJSON(t *testing.T) {
	// A setup script reads --json and nothing else. Saying it in prose alone is
	// saying it to nobody.
	scratchEnv(t)
	root := t.TempDir()
	establishRoot(t, root, "fake-alpha")

	report := initJSON(t, root)
	if report.Action != "adopted" || report.Created {
		t.Errorf("action = %q, created = %v; want adopted/false", report.Action, report.Created)
	}
	if report.Directors != 1 || report.Ambiguous {
		t.Errorf("directors = %d, ambiguous = %v; want 1/false", report.Directors, report.Ambiguous)
	}
	if report.Director == "" {
		t.Error("no director id in the JSON, want the one that was adopted")
	}
}

func TestInitNewAddsADistinguishableDirectorAndSaysSo(t *testing.T) {
	// A second director in one root is a real thing to want. What it must not
	// be is indistinguishable, or silent about what it has just done to every
	// other command in the root.
	scratchEnv(t)
	root := t.TempDir()
	establishRoot(t, root, "fake-alpha")

	stop := captureStdout(t)
	err := runInit(t, root, "--harness", "fake-alpha", "--new")
	out := stop()
	if err != nil {
		t.Fatalf("init --new = %v", err)
	}

	if got := directorCount(t, root); got != 2 {
		t.Fatalf("directors = %d, want 2", got)
	}
	states, _ := director.ListDirectors(root)
	if states[0].Name == states[1].Name {
		t.Errorf("both directors are named %q, want them tellable apart", states[0].Name)
	}
	for _, want := range []string{"now holds 2 directors", "export " + director.EnvID} {
		if !strings.Contains(out, want) {
			t.Errorf("init --new printed %q, want it to contain %q", out, want)
		}
	}
}

func TestInitNewReportsAmbiguityInJSON(t *testing.T) {
	scratchEnv(t)
	root := t.TempDir()
	establishRoot(t, root, "fake-alpha")

	report := initJSON(t, root, "--new")
	if report.Action != "created" || !report.Created {
		t.Errorf("action = %q, created = %v; want created/true", report.Action, report.Created)
	}
	if report.Directors != 2 || !report.Ambiguous {
		t.Errorf("directors = %d, ambiguous = %v; want 2/true", report.Directors, report.Ambiguous)
	}
}

// initReport is what `director init --json` emits.
type initReport struct {
	Director  string `json:"director"`
	Name      string `json:"name"`
	Workflow  string `json:"workflow"`
	Action    string `json:"action"`
	Created   bool   `json:"created"`
	Directors int    `json:"directors"`
	Ambiguous bool   `json:"ambiguous"`
}

// initJSON runs `director init --json` and parses what it printed, failing the
// test if a single byte of it was not the object.
func initJSON(t *testing.T, root string, args ...string) initReport {
	t.Helper()
	stop := captureStdout(t)
	err := runInit(t, root, append([]string{"--json"}, args...)...)
	out := stop()
	if err != nil {
		t.Fatalf("init --json = %v, want no error; it printed %q", err, out)
	}

	var report initReport
	decoder := json.NewDecoder(strings.NewReader(out))
	if err := decoder.Decode(&report); err != nil {
		t.Fatalf("json.Decode(%q) = %v, want --json to emit nothing but the object", out, err)
	}
	if rest := strings.TrimSpace(out[decoder.InputOffset():]); rest != "" {
		t.Errorf("init --json printed %q after the object, want nothing", rest)
	}
	return report
}
