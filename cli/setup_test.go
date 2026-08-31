package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rwmyers/agent-director/director"
	"github.com/rwmyers/agent-director/harness"
)

// The tests in this file do not run in parallel. The harness registry, the
// prompter `director init` asks with, and os.Stdout are all process-global.

// registerFakeHarnesses puts two drivable adapters in the registry so the
// prompt has something to offer. Registration is by name, so repeating it
// across tests is harmless.
//
// They are fakes rather than the real adapters because the point under test is
// that init offers whatever the registry holds. A test that named claude-code
// would pass just as well against the hardcoded default it replaced.
func registerFakeHarnesses() {
	harness.Register(fakeAdapter{name: "fake-alpha"})
	harness.Register(fakeAdapter{name: "fake-omega"})
}

// silenceStdout points os.Stdout at /dev/null for one test. init prints a page
// of guidance that would otherwise bury everything else.
func silenceStdout(t *testing.T) {
	t.Helper()
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open %s = %v", os.DevNull, err)
	}
	saved := os.Stdout
	os.Stdout = devnull
	t.Cleanup(func() {
		os.Stdout = saved
		_ = devnull.Close()
	})
}

// setInitPrompter makes init ask with a prompter of the test's choosing.
func setInitPrompter(t *testing.T, ask prompter) {
	t.Helper()
	saved := initPrompter
	initPrompter = func() prompter { return ask }
	t.Cleanup(func() { initPrompter = saved })
}

// answering builds a prompter that is allowed to ask and answers from a script.
//
// accessible rather than terminal-only, because huh's full TUI needs a pty and
// its line mode does not; terminal stays true because that is the thing under
// test — whether init is willing to ask at all.
func answering(script string) prompter {
	return prompter{in: strings.NewReader(script), out: io.Discard, terminal: true, accessible: true}
}

// refusingReader fails the test if a prompt ever reads from it.
type refusingReader struct{ t *testing.T }

func (r refusingReader) Read([]byte) (int, error) {
	r.t.Error("init asked a question that had already been answered by a flag")
	return 0, io.EOF
}

// optionIndex is the 1-based position huh's accessible select gives a name.
func optionIndex(t *testing.T, names []string, want string) int {
	t.Helper()
	for i, name := range names {
		if name == want {
			return i + 1
		}
	}
	t.Fatalf("harness.Names() = %v, want it to contain %q", names, want)
	return 0
}

// runDirector runs one command exactly as the binary would, with no arguments
// added on its behalf.
func runDirector(t *testing.T, args ...string) error {
	t.Helper()
	saved := opts
	t.Cleanup(func() { opts = saved })

	cmd := newRootCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	return cmd.Execute()
}

// runInit runs `director init` against a scratch root named outright.
func runInit(t *testing.T, root string, args ...string) error {
	t.Helper()
	return runDirector(t, append([]string{"init", "--config", root}, args...)...)
}

// captureStdout collects what init prints. Returns a function that stops the
// capture and hands back everything written.
func captureStdout(t *testing.T) func() string {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() = %v", err)
	}
	saved := os.Stdout
	os.Stdout = write

	done := make(chan string, 1)
	go func() {
		var buffer strings.Builder
		_, _ = io.Copy(&buffer, read)
		done <- buffer.String()
	}()

	var once bool
	stop := func() string {
		if once {
			return ""
		}
		once = true
		os.Stdout = saved
		_ = write.Close()
		out := <-done
		_ = read.Close()
		return out
	}
	t.Cleanup(func() { stop() })
	return stop
}

// establishRoot builds a root the ordinary way, so a test can start from one
// somebody already owns.
func establishRoot(t *testing.T, root, harnessName string) {
	t.Helper()
	registerFakeHarnesses()
	silenceStdout(t)
	if err := runInit(t, root, "--harness", harnessName); err != nil {
		t.Fatalf("establishing %s = %v", root, err)
	}
}

// scratchOnly makes sure a test that runs init without --config cannot reach
// the root this process was itself spawned under.
func scratchOnly(t *testing.T) {
	t.Helper()
	t.Setenv(director.EnvRoot, "")
}

// directorCount is how many directors a root holds.
func directorCount(t *testing.T, root string) int {
	t.Helper()
	states, _ := director.ListDirectors(root)
	return len(states)
}

// configuredHarness is what the written root will actually spawn on, read back
// through the same loader director uses rather than matched as text.
func configuredHarness(t *testing.T, root string) string {
	t.Helper()
	config, err := director.LoadConfig(root)
	if err != nil {
		t.Fatalf("LoadConfig(%s) = %v", root, err)
	}
	if config.Source == "" {
		t.Fatalf("no director.conf under %s", root)
	}
	return config.Harness
}

func TestInitAsksWhichHarness(t *testing.T) {
	silenceStdout(t)
	registerFakeHarnesses()

	// Two selections, because one would be satisfied by any fixed answer —
	// including the hardcoded default this replaced.
	for _, want := range []string{"fake-alpha", "fake-omega"} {
		t.Run("chooses "+want, func(t *testing.T) {
			root := t.TempDir()
			setInitPrompter(t, answering(fmt.Sprintf("%d\n", optionIndex(t, harness.Names(), want))))

			if err := runInit(t, root); err != nil {
				t.Fatalf("init = %v, want no error", err)
			}
			if got := configuredHarness(t, root); got != want {
				t.Errorf("configured harness = %q, want the one selected: %q", got, want)
			}
		})
	}
}

func TestInitHarnessFlagSkipsTheQuestion(t *testing.T) {
	silenceStdout(t)
	registerFakeHarnesses()
	root := t.TempDir()
	setInitPrompter(t, prompter{in: refusingReader{t: t}, out: io.Discard, terminal: true, accessible: true})

	if err := runInit(t, root, "--harness", "fake-omega"); err != nil {
		t.Fatalf("init --harness = %v, want no error", err)
	}
	if got := configuredHarness(t, root); got != "fake-omega" {
		t.Errorf("configured harness = %q, want the one the flag named", got)
	}
}

func TestInitWithoutTerminalOrFlagRefuses(t *testing.T) {
	silenceStdout(t)
	registerFakeHarnesses()
	root := t.TempDir()
	setInitPrompter(t, prompter{in: strings.NewReader(""), out: io.Discard})

	err := runInit(t, root)
	if err == nil {
		t.Fatal("init with no terminal and no --harness = nil, want a refusal rather than a silent default")
	}
	if code := codeFor(err); code == exitOK {
		t.Errorf("codeFor(%v) = %d, want a non-zero exit", err, code)
	}
	if !strings.Contains(err.Error(), "--harness") {
		t.Errorf("error = %q, want it to name the flag that answers the question", err)
	}
	for _, name := range harness.Names() {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error = %q, want it to name the valid choice %q", err, name)
		}
	}
	// Nothing may be left behind: a half-written root is worse than a refusal,
	// and a director.conf here would mean some harness was chosen after all.
	if _, statErr := os.Stat(filepath.Join(root, "director.conf")); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("os.Stat(director.conf) = %v, want the refusal to have written nothing", statErr)
	}
}

func TestInitRejectsAnUnknownHarness(t *testing.T) {
	silenceStdout(t)
	registerFakeHarnesses()
	root := t.TempDir()
	setInitPrompter(t, prompter{in: refusingReader{t: t}, out: io.Discard, terminal: true, accessible: true})

	err := runInit(t, root, "--harness", "no-such-harness")
	if err == nil {
		t.Fatal("init --harness no-such-harness = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "fake-alpha") {
		t.Errorf("error = %q, want it to list the harnesses that are valid", err)
	}
}

func TestInitRefusesARootFoundAboveIt(t *testing.T) {
	// The reported accident: init run in a worktree under a project that
	// already had a root registered a director in the project's fleet instead,
	// silently, and left that fleet with two directors of the same name.
	scratchOnly(t)
	project := t.TempDir()
	parentRoot := filepath.Join(project, director.ProjectDirName)
	establishRoot(t, parentRoot, "fake-alpha")
	before := directorCount(t, parentRoot)

	child := filepath.Join(project, "plants", "worktree")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) = %v", child, err)
	}
	t.Chdir(child)
	setInitPrompter(t, prompter{in: refusingReader{t: t}, out: io.Discard, terminal: true, accessible: true})

	err := runDirector(t, "init")
	if err == nil {
		t.Fatal("init = nil, want a refusal rather than adopting the root above")
	}
	for _, want := range []string{parentRoot, child, "--config"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to name %q", err, want)
		}
	}
	if got := directorCount(t, parentRoot); got != before {
		t.Errorf("directors under the root above = %d, want it untouched at %d", got, before)
	}
	if _, statErr := os.Stat(filepath.Join(child, director.ProjectDirName)); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("os.Stat(child .director) = %v, want the refusal to have created nothing", statErr)
	}
}

func TestInitHonoursDirectorRoot(t *testing.T) {
	// The inverse of what this test used to assert. DIRECTOR_ROOT names a root
	// outright — in a setup script, and in the environment director injects into
	// the agents it spawns — so init uses it, the same as every other command.
	// Only the silent walk up out of the working directory is refused; see
	// TestInitRefusesARootFoundAboveIt for the accident that is about.
	silenceStdout(t)
	project := t.TempDir()
	root := filepath.Join(project, director.ProjectDirName)
	establishRoot(t, root, "fake-alpha")
	before := directorCount(t, root)

	elsewhere := t.TempDir()
	t.Setenv(director.EnvRoot, root)
	t.Chdir(elsewhere)
	setInitPrompter(t, answering("1\n"))

	if err := runDirector(t, "init"); err != nil {
		t.Fatalf("init with $DIRECTOR_ROOT set = %v, want it honoured", err)
	}
	if got := directorCount(t, root); got != before+1 {
		t.Errorf("directors under $DIRECTOR_ROOT = %d, want %d", got, before+1)
	}
	if _, statErr := os.Stat(filepath.Join(elsewhere, director.ProjectDirName)); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("os.Stat(cwd .director) = %v, want init to have used the named root instead", statErr)
	}
}

func TestInitUsesTheRootInTheWorkingDirectory(t *testing.T) {
	// Standing in the project and adding another director to it is what init is
	// for, and the refusal above must not have cost it.
	scratchOnly(t)
	silenceStdout(t)
	project := t.TempDir()
	root := filepath.Join(project, director.ProjectDirName)
	establishRoot(t, root, "fake-alpha")
	before := directorCount(t, root)

	t.Chdir(project)
	setInitPrompter(t, answering("1\n"))

	if err := runDirector(t, "init"); err != nil {
		t.Fatalf("init in the project = %v, want no error", err)
	}
	if got := directorCount(t, root); got != before+1 {
		t.Errorf("directors = %d, want %d", got, before+1)
	}
}

func TestInitAsksOnAnEstablishedRootAndLeavesItAlone(t *testing.T) {
	// Silence was the complaint. On a root whose director.conf somebody wrote by
	// hand, init asks and then says what it did not do — rather than skipping
	// the question because there was nowhere convenient to put the answer.
	root := t.TempDir()
	establishRoot(t, root, "fake-alpha")

	stop := captureStdout(t)
	setInitPrompter(t, answering(fmt.Sprintf("%d\n", optionIndex(t, harness.Names(), "fake-omega"))))
	err := runInit(t, root)
	out := stop()
	if err != nil {
		t.Fatalf("init on an established root = %v, want no error", err)
	}

	if got := configuredHarness(t, root); got != "fake-alpha" {
		t.Errorf("configured harness = %q, want the owner's file left as it was", got)
	}
	for _, want := range []string{"fake-omega", "harness = fake-omega", filepath.Join(root, "director.conf")} {
		if !strings.Contains(out, want) {
			t.Errorf("init said %q, want it to mention %q", out, want)
		}
	}
}
