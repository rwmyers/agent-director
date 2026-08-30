package claudecode

import (
	"slices"
	"strings"
	"testing"

	"github.com/rwmyers/agent-director/harness"
)

var (
	readOnly   = []harness.Capability{harness.CapRead, harness.CapSearch}
	writeLocal = []harness.Capability{harness.CapRead, harness.CapSearch, harness.CapEdit, harness.CapExecute, harness.CapPush}
)

func TestPermits(t *testing.T) {
	t.Parallel()
	adapter := New()

	t.Run("a read-only scope is enforceable", func(t *testing.T) {
		t.Parallel()
		if err := adapter.Permits(readOnly); err != nil {
			t.Errorf("Permits(read-only) = %v, want no error", err)
		}
	})

	t.Run("a scope granting the shell but withholding push is refused", func(t *testing.T) {
		t.Parallel()
		// Publishing is not a distinct tool — it is git through the shell — so
		// this combination cannot be held. Accepting it and running anyway is
		// the one outcome a permission system must not have.
		scope := []harness.Capability{harness.CapRead, harness.CapExecute}
		err := adapter.Permits(scope)
		if err == nil {
			t.Fatal("Permits() = nil error, want a refusal")
		}
		if !strings.Contains(err.Error(), "push") || !strings.Contains(err.Error(), "execute") {
			t.Errorf("Permits() error = %q, want it to name both capabilities in tension", err)
		}
	})

	t.Run("a scope granting the whole shell including push is enforceable", func(t *testing.T) {
		t.Parallel()
		if err := adapter.Permits(writeLocal); err != nil {
			t.Errorf("Permits(write-local) = %v, want no error", err)
		}
	})
}

func TestAllowedTools(t *testing.T) {
	t.Parallel()

	t.Run("the callback is available even to an agent with no shell", func(t *testing.T) {
		t.Parallel()
		// The regression that matters most. A read-only agent has no Bash, and
		// when that also removed its ability to report, the most constrained
		// agents were exactly the ones the director could never hear from:
		// they did the work, finished, and were recorded as abandoned.
		tools := allowedTools(readOnly)
		for _, want := range []string{"Bash(director report:*)", "Bash(director ask:*)"} {
			if !slices.Contains(tools, want) {
				t.Errorf("allowedTools(read-only) = %v, want it to contain %q", tools, want)
			}
		}
	})

	t.Run("a read-only agent gets no general shell and no edit tools", func(t *testing.T) {
		t.Parallel()
		tools := allowedTools(readOnly)
		for _, forbidden := range []string{"Bash", "Edit", "Write", "NotebookEdit"} {
			if slices.Contains(tools, forbidden) {
				t.Errorf("allowedTools(read-only) = %v, want it not to contain %q", tools, forbidden)
			}
		}
		if !slices.Contains(tools, "Read") {
			t.Errorf("allowedTools(read-only) = %v, want it to contain Read", tools)
		}
	})

	t.Run("granting execute yields a general shell", func(t *testing.T) {
		t.Parallel()
		if tools := allowedTools(writeLocal); !slices.Contains(tools, "Bash") {
			t.Errorf("allowedTools(write-local) = %v, want it to contain Bash", tools)
		}
	})
}

func TestSpawnArgs(t *testing.T) {
	t.Parallel()
	adapter := New()
	ref := "3b80f2c1-1b4a-4f2e-9c33-8a1d5e6f7a90"
	req := harness.SpawnRequest{
		ID:     "eng_abc123",
		Prompt: "Read widget.py and report what you find.",
		Allow:  readOnly,
	}
	args := adapter.spawnArgs(ref, req)

	t.Run("the prompt is not passed as an argument", func(t *testing.T) {
		t.Parallel()
		// --allowedTools is variadic, so a trailing positional prompt is
		// silently eaten as another tool name and the agent starts with no
		// input at all. The prompt goes in on stdin instead.
		for _, arg := range args {
			if strings.Contains(arg, "Read widget.py") {
				t.Fatalf("spawnArgs() = %v, want the prompt not to appear in argv", args)
			}
		}
	})

	t.Run("the session id is the harness ref, not the engagement id", func(t *testing.T) {
		t.Parallel()
		index := slices.Index(args, "--session-id")
		if index < 0 || index+1 >= len(args) {
			t.Fatalf("spawnArgs() = %v, want a --session-id flag", args)
		}
		if args[index+1] != ref {
			t.Errorf("--session-id = %q, want %q", args[index+1], ref)
		}
		if slices.Contains(args, req.ID) {
			t.Errorf("spawnArgs() = %v, want the engagement id not to be passed to the harness", args)
		}
	})

	t.Run("a read-only agent is not put into an approval mode it cannot escape", func(t *testing.T) {
		t.Parallel()
		// With no shell and no editing there is nothing to approve, and
		// prompting would strand the agent waiting on a human who is not there.
		if slices.Contains(args, "--permission-mode") {
			t.Errorf("spawnArgs(read-only) = %v, want no --permission-mode", args)
		}
	})
}

func TestIsUUID(t *testing.T) {
	t.Parallel()
	// Claude Code rejects a session id that is not a UUID, which is why the
	// engagement id and the harness ref are separate fields in the first place.
	cases := map[string]bool{
		"3b80f2c1-1b4a-4f2e-9c33-8a1d5e6f7a90": true,
		"eng_a86dfc5a0e4232bb":                 false,
		"":                                     false,
		"3b80f2c1-1b4a-4f2e-9c33-8a1d5e6f7a9":  false,
		"3b80f2c1_1b4a_4f2e_9c33_8a1d5e6f7a90": false,
		"zzzzzzzz-1b4a-4f2e-9c33-8a1d5e6f7a90": false,
	}
	for input, want := range cases {
		if got := isUUID(input); got != want {
			t.Errorf("isUUID(%q) = %t, want %t", input, got, want)
		}
	}

	minted, err := newUUID()
	if err != nil {
		t.Fatalf("newUUID() = %v, want no error", err)
	}
	if !isUUID(minted) {
		t.Errorf("newUUID() = %q, which isUUID rejects", minted)
	}
}

func TestLifecycleFor(t *testing.T) {
	t.Parallel()
	// The observed status set already includes busy, idle and waiting, so it
	// cannot be treated as closed. Anything unrecognised has to land on
	// unknown rather than being assumed benign, because every value here feeds
	// a decision about whether to interrupt somebody.
	cases := map[string]harness.Lifecycle{
		"busy":          harness.LifecycleWorking,
		"idle":          harness.LifecycleIdle,
		"waiting":       harness.LifecycleIdle,
		"something-new": harness.LifecycleUnknown,
		"":              harness.LifecycleUnknown,
	}
	for status, want := range cases {
		if got := lifecycleFor(status); got != want {
			t.Errorf("lifecycleFor(%q) = %v, want %v", status, got, want)
		}
	}
}

func TestProjectSlug(t *testing.T) {
	t.Parallel()
	// Every separator becomes a dash, including the leading one.
	if got, want := projectSlug("/home/me/src/proj"), "-home-me-src-proj"; got != want {
		t.Errorf("projectSlug() = %q, want %q", got, want)
	}
}

func TestSpawnArgsName(t *testing.T) {
	t.Parallel()
	adapter := New()
	ref := "3b80f2c1-1b4a-4f2e-9c33-8a1d5e6f7a90"

	t.Run("a display name is passed to the harness", func(t *testing.T) {
		t.Parallel()
		// Without it Claude Code derives a name from the working directory, so
		// several engagements in one repository are indistinguishable to
		// anyone looking at the harness rather than at director.
		args := adapter.spawnArgs(ref, harness.SpawnRequest{Name: "auth-review", Allow: readOnly})
		index := slices.Index(args, "--name")
		if index < 0 || index+1 >= len(args) {
			t.Fatalf("spawnArgs() = %v, want a --name flag", args)
		}
		if args[index+1] != "auth-review" {
			t.Errorf("--name = %q, want %q", args[index+1], "auth-review")
		}
	})

	t.Run("no name means no flag rather than an empty one", func(t *testing.T) {
		t.Parallel()
		args := adapter.spawnArgs(ref, harness.SpawnRequest{Allow: readOnly})
		if slices.Contains(args, "--name") {
			t.Errorf("spawnArgs() = %v, want no --name when none was given", args)
		}
	})
}
