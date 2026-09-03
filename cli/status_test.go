package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rwmyers/agent-director/director"
)

// The tests in this file do not run in parallel: they set the environment an
// agent reports through, and os.Stdout and the harness registry are
// process-wide. They share the helpers in setup_test.go and spawn_test.go.

// oneEngagement establishes a scratch root with one engagement in it, and returns the
// root and the engagement's identity.
//
// Autodetection is off in the root spawnRoot writes, which is what keeps a
// suite run inside a herdr pane from spawning into somebody's real workspace.
func oneEngagement(t *testing.T, title string) (root, id, token string) {
	t.Helper()
	scratchEnv(t)
	root = t.TempDir()
	spawnRoot(t, root)
	t.Chdir(t.TempDir())

	silenceStdout(t)
	if err := runDirector(t, "spawn", "--config", root, "--task", "investigate", "--title", title, "look at it"); err != nil {
		t.Fatalf("spawn = %v, want no error", err)
	}

	states, err := director.ListDirectors(root)
	if err != nil || len(states) != 1 {
		t.Fatalf("ListDirectors(%s) = %d states, %v; want exactly 1", root, len(states), err)
	}
	for _, engagement := range states[0].Engagements {
		return root, engagement.ID, engagement.Token
	}
	t.Fatalf("no engagement under %s", root)
	return "", "", ""
}

// asAgent puts the caller in the shoes of the engagement, which is the only
// thing allowed to report for it.
func asAgent(t *testing.T, id, token string) {
	t.Helper()
	t.Setenv(director.EnvEngagement, id)
	t.Setenv(director.EnvToken, token)
}

// statusText runs `director status` and returns what it printed.
func statusText(t *testing.T, root string, args ...string) string {
	t.Helper()
	stop := captureStdout(t)
	err := runDirector(t, append([]string{"status", "--config", root}, args...)...)
	out := stop()
	if err != nil {
		t.Fatalf("status = %v, want no error", err)
	}
	return out
}

func TestStatusDoesNotReproduceQuestions(t *testing.T) {
	// The failure this exists for: five agents blocked on large plans, and
	// every `director status` — the command a director is told to run every
	// turn — reproducing all five until somebody answers.
	const question = "Here is my entire plan, all four hundred words of it. May I proceed?"

	root, id, token := oneEngagement(t, "look at it")
	asAgent(t, id, token)

	stop := captureStdout(t)
	err := runDirector(t, "ask", "--config", root, question)
	askOut := stop()
	if err != nil {
		t.Fatalf("ask = %v, want no error", err)
	}
	askID := strings.TrimSpace(askOut)

	out := statusText(t, root)

	if strings.Contains(out, question) {
		t.Errorf("status printed the question text:\n%s", out)
	}
	if !strings.Contains(out, askID) {
		t.Errorf("status = %q, want it to name the open question %q", out, askID)
	}
	if !strings.Contains(out, id) {
		t.Errorf("status = %q, want it to name the engagement waiting", out)
	}
	// Both jobs the old output did have to survive: saying something is
	// waiting, and giving the command that resolves it.
	if !strings.Contains(out, "1 open question") {
		t.Errorf("status = %q, want it to say how many questions are waiting", out)
	}
	if !strings.Contains(out, "director answer "+askID) {
		t.Errorf("status = %q, want the exact answer command", out)
	}
	if !strings.Contains(out, "director status asks "+askID) {
		t.Errorf("status = %q, want it to say how to get the text", out)
	}
}

func TestStatusJSONEnumeratesOpenAsks(t *testing.T) {
	root, id, token := oneEngagement(t, "look at it")
	asAgent(t, id, token)

	stop := captureStdout(t)
	err := runDirector(t, "ask", "--config", root, "May I force-push?")
	askOut := stop()
	if err != nil {
		t.Fatalf("ask = %v, want no error", err)
	}
	askID := strings.TrimSpace(askOut)

	stop = captureStdout(t)
	err = runDirector(t, "status", "--config", root, "--json")
	out := stop()
	if err != nil {
		t.Fatalf("status --json = %v, want no error", err)
	}

	var engagements []struct {
		OpenAsks []string `json:"open_asks"`
	}
	if err := json.Unmarshal([]byte(out), &engagements); err != nil {
		t.Fatalf("unmarshalling %q = %v", out, err)
	}
	if len(engagements) != 1 {
		t.Fatalf("status --json = %d engagements, want 1", len(engagements))
	}
	if len(engagements[0].OpenAsks) != 1 || engagements[0].OpenAsks[0] != askID {
		t.Errorf("open_asks = %v, want [%s]", engagements[0].OpenAsks, askID)
	}
	if strings.Contains(out, "May I force-push?") {
		t.Errorf("status --json carried the question text:\n%s", out)
	}
}

func TestStatusAsks(t *testing.T) {
	const question = "May I force-push to feat/auth?"

	root, id, token := oneEngagement(t, "look at it")
	asAgent(t, id, token)

	stop := captureStdout(t)
	if err := runDirector(t, "ask", "--config", root, question); err != nil {
		stop()
		t.Fatalf("ask = %v, want no error", err)
	}
	askID := strings.TrimSpace(stop())

	t.Run("naming a question prints its text", func(t *testing.T) {
		stop := captureStdout(t)
		err := runDirector(t, "status", "asks", "--config", root, askID)
		out := stop()
		if err != nil {
			t.Fatalf("status asks = %v, want no error", err)
		}
		if !strings.Contains(out, question) {
			t.Errorf("status asks = %q, want the full question", out)
		}
		if !strings.Contains(out, "director answer "+askID) {
			t.Errorf("status asks = %q, want the answer command", out)
		}
	})

	t.Run("--json returns the question as data", func(t *testing.T) {
		stop := captureStdout(t)
		err := runDirector(t, "status", "asks", "--config", root, "--json", askID)
		out := stop()
		if err != nil {
			t.Fatalf("status asks --json = %v, want no error", err)
		}
		var asks []director.Ask
		if err := json.Unmarshal([]byte(out), &asks); err != nil {
			t.Fatalf("unmarshalling %q = %v", out, err)
		}
		if len(asks) != 1 || asks[0].Question != question {
			t.Errorf("status asks --json = %+v, want the named question", asks)
		}
	})

	t.Run("naming nothing lists identifiers and no text", func(t *testing.T) {
		// Listing every open question in full would recreate exactly the
		// problem taking them out of the table solved.
		stop := captureStdout(t)
		err := runDirector(t, "status", "asks", "--config", root)
		out := stop()
		if err != nil {
			t.Fatalf("status asks = %v, want no error rather than a refusal", err)
		}
		if strings.Contains(out, question) {
			t.Errorf("status asks with no arguments dumped the text:\n%s", out)
		}
		if !strings.Contains(out, askID) {
			t.Errorf("status asks = %q, want the identifier", out)
		}
	})

	t.Run("naming nothing under --json is a list of identifiers", func(t *testing.T) {
		stop := captureStdout(t)
		err := runDirector(t, "status", "asks", "--config", root, "--json")
		out := stop()
		if err != nil {
			t.Fatalf("status asks --json = %v, want no error", err)
		}
		var ids []string
		if err := json.Unmarshal([]byte(out), &ids); err != nil {
			t.Fatalf("unmarshalling %q = %v", out, err)
		}
		if len(ids) != 1 || ids[0] != askID {
			t.Errorf("status asks --json = %v, want [%s]", ids, askID)
		}
	})

	t.Run("an unknown identifier exits not-found", func(t *testing.T) {
		silenceStdout(t)
		err := runDirector(t, "status", "asks", "--config", root, "ask_nonesuch")
		if err == nil {
			t.Fatal("status asks ask_nonesuch = nil, want an error")
		}
		if code := codeFor(err); code != exitNotFound {
			t.Errorf("codeFor(%v) = %d, want %d", err, code, exitNotFound)
		}
	})

	_ = id
}

func TestStatusShowsMaterials(t *testing.T) {
	root, id, token := oneEngagement(t, "look at it")

	t.Run("an engagement that reported none renders empty", func(t *testing.T) {
		out := statusText(t, root)
		if !strings.Contains(out, "MATERIALS") {
			t.Errorf("status = %q, want a MATERIALS column", out)
		}
		// Nothing may stand in for "nothing yet". A dash or a zero makes an
		// engagement that produced nothing look like one that produced
		// something.
		for _, row := range strings.Split(out, "\n") {
			if !strings.Contains(row, id) {
				continue
			}
			if strings.Contains(row, "-") && !strings.Contains(row, "look at it") {
				t.Errorf("row = %q, want no placeholder in the materials cell", row)
			}
		}
	})

	t.Run("reported materials appear, shortened", func(t *testing.T) {
		asAgent(t, id, token)
		long := "https://example.invalid/some/rather/long/pull/request/url/that/runs/on/7"
		silenceStdout(t)
		if err := runDirector(t, "report", "--config", root,
			"--materials", long, "--materials", "feat/auth", "--materials", "internal/auth/token.go"); err != nil {
			t.Fatalf("report --materials = %v, want no error", err)
		}

		out := statusText(t, root)
		if strings.Contains(out, long) {
			t.Errorf("status printed a material in full; the column must stay short:\n%s", out)
		}
		if !strings.Contains(out, "+2") {
			t.Errorf("status = %q, want it to say how many other materials there are", out)
		}
		for _, row := range strings.Split(out, "\n") {
			if strings.Contains(row, id) && len(row) > 200 {
				t.Errorf("status row is %d characters; the materials column is not bounded:\n%s", len(row), row)
			}
		}
	})

	t.Run("--json carries the whole list", func(t *testing.T) {
		stop := captureStdout(t)
		err := runDirector(t, "status", "--config", root, "--json")
		out := stop()
		if err != nil {
			t.Fatalf("status --json = %v, want no error", err)
		}
		var engagements []struct {
			Materials []string `json:"materials"`
		}
		if err := json.Unmarshal([]byte(out), &engagements); err != nil {
			t.Fatalf("unmarshalling %q = %v", out, err)
		}
		if len(engagements) != 1 || len(engagements[0].Materials) != 3 {
			t.Fatalf("materials = %v, want all three", engagements)
		}
		if !strings.HasPrefix(engagements[0].Materials[0], "https://example.invalid/") {
			t.Errorf("materials[0] = %q, want it unshortened", engagements[0].Materials[0])
		}
	})
}
