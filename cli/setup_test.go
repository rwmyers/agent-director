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

// runInit runs `director init` against a scratch root, as the binary would.
func runInit(t *testing.T, root string, args ...string) error {
	t.Helper()
	saved := opts
	t.Cleanup(func() { opts = saved })

	cmd := newRootCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(append([]string{"init", "--config", root}, args...))
	return cmd.Execute()
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
