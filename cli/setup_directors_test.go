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

func TestSetupTwiceLeavesOneDirector(t *testing.T) {
	// The reported failure. `director setup` run a second time in a root
	// registered another director with the same auto-generated name and the
	// same workflow, said so only afterwards and only in prose, and left the
	// root holding two identical-looking directors — after which every command
	// there failed with "more than one director under <root>".
	scratchEnv(t)
	root := t.TempDir()
	establishRoot(t, root, "fake-alpha")

	if err := runSetup(t, root, "--harness", "fake-alpha"); err != nil {
		t.Fatalf("second setup = %v, want it to adopt rather than fail", err)
	}

	if got := directorCount(t, root); got != 1 {
		t.Errorf("directors after two setups = %d, want 1", got)
	}
	// The consequence that actually bit: a command with nothing to go on has to
	// be able to resolve a director in this root.
	roots, err := director.ResolveRoots(root, root)
	if err != nil {
		t.Fatalf("ResolveRoots(%s) = %v", root, err)
	}
	if _, err := director.Open(roots, "", director.SystemClock); err != nil {
		t.Errorf("Open() with no id = %v, want the root to be unambiguous after two setups", err)
	}
}

func TestSetupSaysItAdoptedInProse(t *testing.T) {
	// Reporting after the fact is what let this pass unnoticed. Whatever setup
	// decided has to be the first thing it says about the director.
	scratchEnv(t)
	root := t.TempDir()
	establishRoot(t, root, "fake-alpha")

	stop := captureStdout(t)
	err := runSetup(t, root, "--harness", "fake-alpha")
	out := stop()
	if err != nil {
		t.Fatalf("second setup = %v", err)
	}

	for _, want := range []string{"already registered here", "Nothing was created", "--new"} {
		if !strings.Contains(out, want) {
			t.Errorf("setup printed %q, want it to contain %q", out, want)
		}
	}
	if strings.Contains(out, "initialised for workflow") {
		t.Errorf("setup printed %q, want it not to claim it initialised anything", out)
	}
}

func TestSetupSaysItAdoptedInJSON(t *testing.T) {
	// A setup script reads --json and nothing else. Saying it in prose alone is
	// saying it to nobody.
	scratchEnv(t)
	root := t.TempDir()
	establishRoot(t, root, "fake-alpha")

	report := setupJSON(t, root)
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

func TestSetupNewAddsADistinguishableDirectorAndSaysSo(t *testing.T) {
	// A second director in one root is a real thing to want. What it must not
	// be is indistinguishable, or silent about what it has just done to every
	// other command in the root.
	scratchEnv(t)
	root := t.TempDir()
	establishRoot(t, root, "fake-alpha")

	stop := captureStdout(t)
	err := runSetup(t, root, "--harness", "fake-alpha", "--new")
	out := stop()
	if err != nil {
		t.Fatalf("setup --new = %v", err)
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
			t.Errorf("setup --new printed %q, want it to contain %q", out, want)
		}
	}
}

func TestSetupNewReportsAmbiguityInJSON(t *testing.T) {
	scratchEnv(t)
	root := t.TempDir()
	establishRoot(t, root, "fake-alpha")

	report := setupJSON(t, root, "--new")
	if report.Action != "created" || !report.Created {
		t.Errorf("action = %q, created = %v; want created/true", report.Action, report.Created)
	}
	if report.Directors != 2 || !report.Ambiguous {
		t.Errorf("directors = %d, ambiguous = %v; want 2/true", report.Directors, report.Ambiguous)
	}
}

func TestSetupJSONIsParseableOnAFreshRoot(t *testing.T) {
	// Everything setup writes has to be inside the object. Prose ahead of it —
	// the `wrote <path>` lines, or a harness prompt rendering onto the stdout
	// the caller is parsing — makes --json output that no consumer can read,
	// which is worse than no --json at all because it looks supported.
	scratchEnv(t)
	registerFakeHarnesses()
	root := t.TempDir()

	report := setupJSON(t, root, "--harness", "fake-omega")
	if report.Action != "created" || report.Directors != 1 {
		t.Errorf("action = %q, directors = %d; want created/1", report.Action, report.Directors)
	}
	if report.Harness != "fake-omega" {
		t.Errorf("harness = %q, want the one the flag named", report.Harness)
	}
	// The starters are still reported, in the object rather than ahead of it.
	if len(report.Wrote) == 0 {
		t.Error("wrote = [], want the starter files this setup created")
	}
	var sawConfig bool
	for _, path := range report.Wrote {
		if strings.HasSuffix(path, "director.conf") {
			sawConfig = true
		}
	}
	if !sawConfig {
		t.Errorf("wrote = %v, want it to include the director.conf that was written", report.Wrote)
	}
	// And the skills half is in the same object: one command, one report.
	if len(report.Skills) != 1 || report.Skills[0].Host != scratchSkillsHost || len(report.Skills[0].Wrote) == 0 {
		t.Errorf("skills = %+v, want the one host installed for and the files written", report.Skills)
	}
}

func TestSetupJSONWithoutAHarnessRefusesRatherThanAsking(t *testing.T) {
	// A caller parsing JSON cannot answer a question, and the prompt would
	// render onto the stdout it is reading. So --json needs --harness, the same
	// as a pipe does.
	scratchEnv(t)
	registerFakeHarnesses()
	root := t.TempDir()
	setSetupPrompter(t, prompter{in: refusingReader{t: t}, out: refusingWriter{t: t}, terminal: true, accessible: true})

	err := runSetup(t, root, "--json")
	if err == nil {
		t.Fatal("setup --json with no --harness = nil, want a refusal rather than a prompt")
	}
	if !strings.Contains(err.Error(), "--harness") {
		t.Errorf("error = %q, want it to name the flag that answers the question", err)
	}
}

// setupReport is what `director setup --json` emits.
type setupReport struct {
	Director  string   `json:"director"`
	Name      string   `json:"name"`
	Workflow  string   `json:"workflow"`
	Harness   string   `json:"harness"`
	Action    string   `json:"action"`
	Created   bool     `json:"created"`
	Directors int      `json:"directors"`
	Ambiguous bool     `json:"ambiguous"`
	Wrote     []string `json:"wrote"`
	Skills    []struct {
		Host  string   `json:"host"`
		Wrote []string `json:"wrote"`
	} `json:"skills"`
}

// setupJSON runs `director setup --json` and parses what it printed, failing the
// test if a single byte of it was not the object.
func setupJSON(t *testing.T, root string, args ...string) setupReport {
	t.Helper()
	stop := captureStdout(t)
	err := runSetup(t, root, append([]string{"--json"}, args...)...)
	out := stop()
	if err != nil {
		t.Fatalf("setup --json = %v, want no error; it printed %q", err, out)
	}

	var report setupReport
	decoder := json.NewDecoder(strings.NewReader(out))
	if err := decoder.Decode(&report); err != nil {
		t.Fatalf("json.Decode(%q) = %v, want --json to emit nothing but the object", out, err)
	}
	if rest := strings.TrimSpace(out[decoder.InputOffset():]); rest != "" {
		t.Errorf("setup --json printed %q after the object, want nothing", rest)
	}
	return report
}

// refusingWriter fails the test if a prompt ever renders through it.
type refusingWriter struct{ t *testing.T }

func (w refusingWriter) Write(p []byte) (int, error) {
	w.t.Errorf("setup rendered a prompt under --json: %q", p)
	return len(p), nil
}
