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
// prompter `director setup` asks with, and os.Stdout are all process-global.

// registerFakeHarnesses puts two drivable adapters in the registry so the
// prompt has something to offer. Registration is by name, so repeating it
// across tests is harmless.
//
// They are fakes rather than the real adapters because the point under test is
// that setup offers whatever the registry holds. A test that named claude-code
// would pass just as well against the hardcoded default it replaced.
func registerFakeHarnesses() {
	harness.Register(fakeAdapter{name: "fake-alpha"})
	harness.Register(fakeAdapter{name: "fake-omega"})
}

// scratchSkillsHost is the skills target every workflow-half test installs
// into, so that the skills half has a flag answer and writes only into scratch.
const scratchSkillsHost = "fake-skills"

// scratchSkillsDir is where scratchSkillsHost puts skills. Each run of setup
// points it at a fresh directory; the installer reads it when enumerated, which
// is once per command.
var scratchSkillsDir string

type scratchInstaller struct{}

func (scratchInstaller) SkillLocations() (harness.SkillLocations, error) {
	return harness.SkillLocations{Description: "Scratch Harness", GlobalDir: scratchSkillsDir, Verified: true}, nil
}

// silenceStdout points os.Stdout at /dev/null for one test. setup prints a page
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

// setSetupPrompter makes setup ask with a prompter of the test's choosing.
func setSetupPrompter(t *testing.T, ask prompter) {
	t.Helper()
	saved := setupPrompter
	setupPrompter = func() prompter { return ask }
	t.Cleanup(func() { setupPrompter = saved })
}

// answering builds a prompter that is allowed to ask and answers from a script.
//
// accessible rather than terminal-only, because huh's full TUI needs a pty and
// its line mode does not; terminal stays true because that is the thing under
// test — whether setup is willing to ask at all.
func answering(script string) prompter {
	return prompter{in: strings.NewReader(script), out: io.Discard, terminal: true, accessible: true}
}

// transcribing is answering, keeping what the prompts rendered so a test can
// assert on what the person was shown.
func transcribing(script string) (prompter, *strings.Builder) {
	var shown strings.Builder
	return prompter{in: strings.NewReader(script), out: &shown, terminal: true, accessible: true}, &shown
}

// refusingReader fails the test if a prompt ever reads from it.
type refusingReader struct{ t *testing.T }

func (r refusingReader) Read([]byte) (int, error) {
	r.t.Error("setup asked a question that had already been answered by a flag")
	return 0, io.EOF
}

// notAsking is a prompter with a terminal that must never be read from: every
// answer is expected to come from a flag.
func notAsking(t *testing.T) prompter {
	return prompter{in: refusingReader{t: t}, out: io.Discard, terminal: true, accessible: true}
}

// noTerminal is a prompter with nobody at the other end.
func noTerminal() prompter {
	return prompter{in: strings.NewReader(""), out: io.Discard}
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

// skillsFlags answer the skills half's questions with the scratch target, and
// point that target at a fresh directory.
func skillsFlags(t *testing.T) []string {
	t.Helper()
	harness.RegisterSkillInstaller(scratchSkillsHost, scratchInstaller{})
	scratchSkillsDir = t.TempDir()
	return []string{"--host", scratchSkillsHost, "--scope", string(ScopeGlobal)}
}

// runSetup runs `director setup` against a scratch root named outright, with
// the skills half answered by flags so that only the workflow half is under
// test.
func runSetup(t *testing.T, root string, args ...string) error {
	t.Helper()
	all := append([]string{"setup", "--config", root}, skillsFlags(t)...)
	return runDirector(t, append(all, args...)...)
}

// runSetupHere runs `director setup` with the skills half answered by flags and
// the location left to be asked, or found from the environment.
func runSetupHere(t *testing.T, args ...string) error {
	t.Helper()
	all := append([]string{"setup"}, skillsFlags(t)...)
	return runDirector(t, append(all, args...)...)
}

// captureStdout collects what setup prints. Returns a function that stops the
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
	if err := runSetup(t, root, "--harness", harnessName); err != nil {
		t.Fatalf("establishing %s = %v", root, err)
	}
}

// scratchOnly makes sure a test that runs setup without --config cannot reach
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

// onlyDirector is the id of the single director a root holds, for tests that
// assert which root a command reached by the director it named.
func onlyDirector(t *testing.T, root string) string {
	t.Helper()
	states, _ := director.ListDirectors(root)
	if len(states) != 1 {
		t.Fatalf("directors under %s = %d, want exactly 1", root, len(states))
	}
	return states[0].DirectorID
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

// exists reports whether a path is there at all.
func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestSetupAsksWhichHarness(t *testing.T) {
	silenceStdout(t)
	registerFakeHarnesses()

	// Two selections, because one would be satisfied by any fixed answer —
	// including the hardcoded default this replaced.
	for _, want := range []string{"fake-alpha", "fake-omega"} {
		t.Run("chooses "+want, func(t *testing.T) {
			root := t.TempDir()
			setSetupPrompter(t, answering(fmt.Sprintf("%d\n", optionIndex(t, harness.Names(), want))))

			if err := runSetup(t, root); err != nil {
				t.Fatalf("setup = %v, want no error", err)
			}
			if got := configuredHarness(t, root); got != want {
				t.Errorf("configured harness = %q, want the one selected: %q", got, want)
			}
		})
	}
}

func TestSetupHarnessFlagSkipsTheQuestion(t *testing.T) {
	silenceStdout(t)
	registerFakeHarnesses()
	root := t.TempDir()
	setSetupPrompter(t, notAsking(t))

	if err := runSetup(t, root, "--harness", "fake-omega"); err != nil {
		t.Fatalf("setup --harness = %v, want no error", err)
	}
	if got := configuredHarness(t, root); got != "fake-omega" {
		t.Errorf("configured harness = %q, want the one the flag named", got)
	}
}

func TestSetupWithoutTerminalOrFlagRefuses(t *testing.T) {
	silenceStdout(t)
	registerFakeHarnesses()
	root := t.TempDir()
	setSetupPrompter(t, noTerminal())

	err := runSetup(t, root)
	if err == nil {
		t.Fatal("setup with no terminal and no --harness = nil, want a refusal rather than a silent default")
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
	// And the skills half, which ran first in the old two-command world, must
	// not have run at all: a refusal that has already installed skills is a
	// refusal somebody has to undo.
	if entries, _ := os.ReadDir(scratchSkillsDir); len(entries) != 0 {
		t.Errorf("skills directory holds %d entries, want the refusal to have installed nothing", len(entries))
	}
}

func TestSetupNonInteractiveNamesEveryMissingFlag(t *testing.T) {
	// The contract off a terminal is that every answer is a flag. Being told
	// about them one run at a time is being told the rules one at a time, so
	// a refusal names all of them together — including --harness, which is
	// only needed for a root that does not exist yet.
	silenceStdout(t)
	registerFakeHarnesses()
	scratchOnly(t)
	t.Chdir(t.TempDir())

	for name, ask := range map[string]prompter{"no terminal": noTerminal(), "--json": notAsking(t)} {
		t.Run(name, func(t *testing.T) {
			setSetupPrompter(t, ask)
			args := []string{"setup"}
			if name == "--json" {
				args = append(args, "--json")
			}
			err := runDirector(t, args...)
			if err == nil {
				t.Fatal("setup with nothing to go on = nil, want a refusal")
			}
			for _, want := range []string{"--scope", "--host", "--dir", "--global", "--config", "--harness"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, want it to name %q", err, want)
				}
			}
		})
	}
}

func TestSetupNonInteractiveOnAnEstablishedRootNeedsNoHarness(t *testing.T) {
	// The harness is only written into a director.conf that does not exist
	// yet, so a script re-running setup against a root it already made is not
	// asked for a flag that has nowhere to go.
	root := t.TempDir()
	establishRoot(t, root, "fake-alpha")
	setSetupPrompter(t, noTerminal())

	if err := runSetup(t, root); err != nil {
		t.Fatalf("second setup off a terminal, no --harness = %v, want it to adopt quietly", err)
	}
	if got := directorCount(t, root); got != 1 {
		t.Errorf("directors = %d, want the one already there", got)
	}
}

func TestSetupRejectsAnUnknownHarness(t *testing.T) {
	silenceStdout(t)
	registerFakeHarnesses()
	root := t.TempDir()
	setSetupPrompter(t, notAsking(t))

	err := runSetup(t, root, "--harness", "no-such-harness")
	if err == nil {
		t.Fatal("setup --harness no-such-harness = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "fake-alpha") {
		t.Errorf("error = %q, want it to list the harnesses that are valid", err)
	}
}

func TestSetupInstallsSkillsAndWorkflowTogether(t *testing.T) {
	// The whole point of the merge: one command, and afterwards both halves
	// are there. --dir is the scripted answer to the location question, and
	// the root lands under it as .director.
	silenceStdout(t)
	registerFakeHarnesses()
	scratchOnly(t)
	project := t.TempDir()
	setSetupPrompter(t, noTerminal())

	if err := runSetupHere(t, "--dir", project, "--harness", "fake-alpha"); err != nil {
		t.Fatalf("setup --dir = %v, want no error", err)
	}
	root := filepath.Join(project, director.ProjectDirName)
	for _, want := range []string{
		filepath.Join(root, "director.conf"),
		filepath.Join(root, "workflows", "default.conf"),
		filepath.Join(scratchSkillsDir, "director", "SKILL.md"),
	} {
		if !exists(want) {
			t.Errorf("%s is missing, want setup to have written it", want)
		}
	}
	if got := directorCount(t, root); got != 1 {
		t.Errorf("directors under %s = %d, want 1", root, got)
	}
}

func TestSetupDirAcceptsARelativePath(t *testing.T) {
	silenceStdout(t)
	registerFakeHarnesses()
	scratchOnly(t)
	parent := t.TempDir()
	t.Chdir(parent)
	setSetupPrompter(t, noTerminal())

	if err := runSetupHere(t, "--dir", filepath.Join("sub", "project"), "--harness", "fake-alpha"); err != nil {
		t.Fatalf("setup --dir sub/project = %v, want no error", err)
	}
	root := filepath.Join(parent, "sub", "project", director.ProjectDirName)
	if got := directorCount(t, root); got != 1 {
		t.Errorf("directors under %s = %d, want the relative path resolved from the working directory", root, got)
	}
}

func TestSetupGlobalUsesTheUserRoot(t *testing.T) {
	// --global answers the location question with the user root. It says
	// nothing about where the skills go; that is --scope's question.
	silenceStdout(t)
	registerFakeHarnesses()
	scratchOnly(t)
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Chdir(t.TempDir())
	setSetupPrompter(t, noTerminal())

	if err := runSetupHere(t, "--global", "--harness", "fake-alpha"); err != nil {
		t.Fatalf("setup --global = %v, want no error", err)
	}
	root := filepath.Join(home, "director")
	if got := directorCount(t, root); got != 1 {
		t.Errorf("directors under the user root %s = %d, want 1", root, got)
	}
}

func TestSetupRefusesTwoAnswersToTheLocation(t *testing.T) {
	silenceStdout(t)
	registerFakeHarnesses()
	setSetupPrompter(t, notAsking(t))

	err := runSetupHere(t, "--global", "--dir", t.TempDir(), "--harness", "fake-alpha")
	if err == nil {
		t.Fatal("setup --global --dir = nil, want a refusal to pick between them")
	}
	for _, want := range []string{"--global", "--dir"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to name %q", err, want)
		}
	}
}

func TestSetupExplainsAndSuggestsTheWorkingDirectory(t *testing.T) {
	// The interactive path: before the location is asked, the person is told
	// what a workflow is; the question itself offers where they are standing,
	// and Enter takes it.
	silenceStdout(t)
	registerFakeHarnesses()
	scratchOnly(t)
	project := t.TempDir()
	t.Chdir(project)
	ask, shown := transcribing("\n" + fmt.Sprintf("%d\n", optionIndex(t, harness.Names(), "fake-omega")))
	setSetupPrompter(t, ask)

	if err := runSetupHere(t); err != nil {
		t.Fatalf("setup = %v, want no error", err)
	}
	for _, want := range []string{"task types", "permissions", "progress", "bound to one workflow permanently", project} {
		if !strings.Contains(shown.String(), want) {
			t.Errorf("setup showed %q, want it to include %q", shown.String(), want)
		}
	}
	root := filepath.Join(project, director.ProjectDirName)
	if got := configuredHarness(t, root); got != "fake-omega" {
		t.Errorf("configured harness = %q, want the one selected after the location", got)
	}
	if got := directorCount(t, root); got != 1 {
		t.Errorf("directors under %s = %d, want Enter to have accepted the working directory", root, got)
	}
}

func TestSetupTypedPathIsConfirmedBeforeItIsCreated(t *testing.T) {
	// A path typed in is a path that can be mistyped. The directory is said
	// back before it is created; declining backs out with nothing written.
	silenceStdout(t)
	registerFakeHarnesses()
	scratchOnly(t)
	parent := t.TempDir()
	t.Chdir(parent)
	typed := filepath.Join("elsewhere", "proj")
	root := filepath.Join(parent, typed, director.ProjectDirName)

	t.Run("declined", func(t *testing.T) {
		ask, shown := transcribing(typed + "\nn\n")
		setSetupPrompter(t, ask)
		stop := captureStdout(t)
		err := runSetupHere(t)
		out := stop()
		if err != nil {
			t.Fatalf("setup, declining the path = %v, want backing out to be a normal outcome", err)
		}
		if !strings.Contains(shown.String(), filepath.Join(parent, typed)) {
			t.Errorf("setup showed %q, want it to say back the absolute path it would create", shown.String())
		}
		if exists(filepath.Join(parent, typed)) {
			t.Errorf("%s exists, want nothing created after the path was declined", filepath.Join(parent, typed))
		}
		if !strings.Contains(out, "nothing was set up") {
			t.Errorf("setup printed %q, want it to say nothing was set up", out)
		}
	})

	t.Run("confirmed", func(t *testing.T) {
		setSetupPrompter(t, answering(typed+"\ny\n"+fmt.Sprintf("%d\n", optionIndex(t, harness.Names(), "fake-alpha"))))
		if err := runSetupHere(t); err != nil {
			t.Fatalf("setup, confirming the path = %v, want no error", err)
		}
		if got := directorCount(t, root); got != 1 {
			t.Errorf("directors under %s = %d, want the confirmed relative path resolved from the working directory", root, got)
		}
	})
}

func TestSetupPointsOutARootAboveBeforeCreatingBeneathIt(t *testing.T) {
	// The accident the old command refused outright: setup run in a worktree
	// under a project that already had a root registered a director in the
	// project's fleet instead, silently, and left that fleet with two directors
	// of the same name. Now the location is a question, and a root above the
	// answer is pointed out before a separate one is created — so the default
	// answer cannot land anywhere by accident.
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

	t.Run("declined", func(t *testing.T) {
		ask, shown := transcribing("\nn\n")
		setSetupPrompter(t, ask)
		if err := runSetupHere(t); err != nil {
			t.Fatalf("setup, declining = %v, want backing out to be a normal outcome", err)
		}
		if !strings.Contains(shown.String(), parentRoot) {
			t.Errorf("setup showed %q, want it to name the root above at %s", shown.String(), parentRoot)
		}
		if got := directorCount(t, parentRoot); got != before {
			t.Errorf("directors under the root above = %d, want it untouched at %d", got, before)
		}
		if exists(filepath.Join(child, director.ProjectDirName)) {
			t.Error("a root was created in the worktree after the person declined")
		}
	})

	t.Run("confirmed", func(t *testing.T) {
		setSetupPrompter(t, answering("\ny\n"+fmt.Sprintf("%d\n", optionIndex(t, harness.Names(), "fake-alpha"))))
		if err := runSetupHere(t); err != nil {
			t.Fatalf("setup, confirming = %v, want no error", err)
		}
		if got := directorCount(t, filepath.Join(child, director.ProjectDirName)); got != 1 {
			t.Errorf("directors in the worktree's own root = %d, want 1", got)
		}
		if got := directorCount(t, parentRoot); got != before {
			t.Errorf("directors under the root above = %d, want it untouched at %d", got, before)
		}
	})
}

func TestSetupHonoursDirectorRoot(t *testing.T) {
	// DIRECTOR_ROOT names a root outright — in a setup script, and in the
	// environment director injects into the agents it spawns — so setup uses
	// it the same as every other command, and does not ask where.
	//
	// The observable is which root setup resolved to, asserted from what it
	// reports: setup adopts the director already there, so a count would say
	// nothing either way.
	project := t.TempDir()
	root := filepath.Join(project, director.ProjectDirName)
	establishRoot(t, root, "fake-alpha")
	resident := onlyDirector(t, root)

	elsewhere := t.TempDir()
	t.Setenv(director.EnvRoot, root)
	t.Chdir(elsewhere)
	setSetupPrompter(t, answering("1\n"))

	stop := captureStdout(t)
	err := runSetupHere(t)
	out := stop()
	if err != nil {
		t.Fatalf("setup with $DIRECTOR_ROOT set = %v, want it honoured", err)
	}
	for _, want := range []string{root, resident} {
		if !strings.Contains(out, want) {
			t.Errorf("setup said %q, want it to name %q — the root $%s points at",
				out, want, director.EnvRoot)
		}
	}
	if _, statErr := os.Stat(filepath.Join(elsewhere, director.ProjectDirName)); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("os.Stat(cwd .director) = %v, want setup to have used the named root instead", statErr)
	}
}

func TestSetupUsesTheRootInTheWorkingDirectory(t *testing.T) {
	// Standing in the project, accepting the suggested location, and finding
	// the root already there is the ordinary re-run. Setup adopts the director
	// registered in it; what must not happen is a second root nested inside
	// the first.
	scratchOnly(t)
	project := t.TempDir()
	root := filepath.Join(project, director.ProjectDirName)
	establishRoot(t, root, "fake-alpha")
	resident := onlyDirector(t, root)

	t.Chdir(project)
	setSetupPrompter(t, answering("\n1\n"))

	stop := captureStdout(t)
	err := runSetupHere(t)
	out := stop()
	if err != nil {
		t.Fatalf("setup in the project = %v, want no error", err)
	}
	for _, want := range []string{root, resident} {
		if !strings.Contains(out, want) {
			t.Errorf("setup said %q, want it to name %q — the root in the working directory", out, want)
		}
	}
	if _, statErr := os.Stat(filepath.Join(root, director.ProjectDirName)); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("os.Stat(nested .director) = %v, want no root nested inside the one that was used", statErr)
	}
}

func TestSetupAsksOnAnEstablishedRootAndLeavesItAlone(t *testing.T) {
	// Silence was the complaint. On a root whose director.conf somebody wrote by
	// hand, setup asks and then says what it did not do — rather than skipping
	// the question because there was nowhere convenient to put the answer.
	root := t.TempDir()
	establishRoot(t, root, "fake-alpha")

	stop := captureStdout(t)
	setSetupPrompter(t, answering(fmt.Sprintf("%d\n", optionIndex(t, harness.Names(), "fake-omega"))))
	err := runSetup(t, root)
	out := stop()
	if err != nil {
		t.Fatalf("setup on an established root = %v, want no error", err)
	}

	if got := configuredHarness(t, root); got != "fake-alpha" {
		t.Errorf("configured harness = %q, want the owner's file left as it was", got)
	}
	for _, want := range []string{"fake-omega", "harness = fake-omega", filepath.Join(root, "director.conf")} {
		if !strings.Contains(out, want) {
			t.Errorf("setup said %q, want it to mention %q", out, want)
		}
	}
}

func TestSetupDryRunWritesNothing(t *testing.T) {
	// --dry-run covers both halves: it says where the skills and the starters
	// would go and what would happen to the director, and touches none of it.
	registerFakeHarnesses()
	scratchOnly(t)
	project := t.TempDir()
	setSetupPrompter(t, noTerminal())

	stop := captureStdout(t)
	err := runSetupHere(t, "--dry-run", "--dir", project, "--harness", "fake-alpha")
	out := stop()
	if err != nil {
		t.Fatalf("setup --dry-run = %v, want no error", err)
	}
	root := filepath.Join(project, director.ProjectDirName)
	for _, want := range []string{
		"would write " + filepath.Join(root, "director.conf"),
		"would write " + filepath.Join(scratchSkillsDir, "director", "SKILL.md"),
		"would register a director",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("setup --dry-run printed %q, want it to contain %q", out, want)
		}
	}
	if exists(root) {
		t.Errorf("%s exists, want --dry-run to have created nothing", root)
	}
	if entries, _ := os.ReadDir(scratchSkillsDir); len(entries) != 0 {
		t.Errorf("skills directory holds %d entries, want --dry-run to have installed nothing", len(entries))
	}
}
