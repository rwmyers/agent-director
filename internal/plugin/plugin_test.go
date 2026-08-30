package plugin

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writePlugin writes an executable shell-script plugin into dir.
func writePlugin(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, Prefix+name)
	script := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil { // #nosec G306 -- a test fixture that must be executable
		t.Fatal(err)
	}
	return path
}

func TestDiscoverIn(t *testing.T) {
	t.Parallel()
	// DiscoverIn rather than $PATH, so every test here can run in parallel:
	// t.Setenv is process-global and would serialise the whole package.

	t.Run("only executables matching the prefix are found", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writePlugin(t, dir, "good", "echo '{}'")
		if err := os.WriteFile(filepath.Join(dir, Prefix+"data"), []byte("not executable"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "unrelated-tool"), []byte("#!/bin/sh\n"), 0o700); err != nil { // #nosec G306
			t.Fatal(err)
		}

		found := DiscoverIn([]string{dir})
		if len(found) != 1 {
			t.Fatalf("DiscoverIn() = %+v, want exactly the one executable plugin", found)
		}
		if found[0].Name != "good" {
			t.Errorf("Name = %q, want %q", found[0].Name, "good")
		}
	})

	t.Run("the earlier directory wins, as a shell would resolve it", func(t *testing.T) {
		t.Parallel()
		first, second := t.TempDir(), t.TempDir()
		wanted := writePlugin(t, first, "dup", "echo '{}'")
		writePlugin(t, second, "dup", "echo '{}'")

		found := DiscoverIn([]string{first, second})
		if len(found) != 1 || found[0].Path != wanted {
			t.Errorf("DiscoverIn() = %+v, want the copy from the earlier directory", found)
		}
	})

	t.Run("a missing directory is skipped rather than failing discovery", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writePlugin(t, dir, "good", "echo '{}'")
		if found := DiscoverIn([]string{"/nonexistent", dir, ""}); len(found) != 1 {
			t.Errorf("DiscoverIn() = %+v, want the one real plugin", found)
		}
	})
}

func TestDescribe(t *testing.T) {
	t.Parallel()

	t.Run("a matching api version is accepted", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := writePlugin(t, dir, "ok", `echo '{"api_version":1,"name":"ok","version":"0.1.0","enforces":["read"]}'`)

		description, err := New(Found{Name: "ok", Path: path}).Describe(context.Background())
		if err != nil {
			t.Fatalf("Describe() = %v, want no error", err)
		}
		if description.Name != "ok" || len(description.Enforces) != 1 {
			t.Errorf("Describe() = %+v, want the plugin's own declaration", description)
		}
	})

	t.Run("a mismatched api version is refused, naming both numbers", func(t *testing.T) {
		t.Parallel()
		// Refusing here rather than at the first real verb means the complaint
		// is about the version, not about whatever that verb happened to do.
		dir := t.TempDir()
		path := writePlugin(t, dir, "future", `echo '{"api_version":99,"name":"future"}'`)

		_, err := New(Found{Name: "future", Path: path}).Describe(context.Background())
		if !errors.Is(err, ErrUnsupportedAPI) {
			t.Fatalf("Describe() = %v, want ErrUnsupportedAPI", err)
		}
		if !strings.Contains(err.Error(), "99") || !strings.Contains(err.Error(), "1") {
			t.Errorf("Describe() error = %q, want both version numbers", err)
		}
	})
}

func TestCall(t *testing.T) {
	t.Parallel()

	t.Run("the request envelope carries the api version, config and params", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := writePlugin(t, dir, "echoer", `cat`)

		client := New(Found{Name: "echoer", Path: path})
		client.Config = map[string]string{"socket": "/tmp/x"}

		var got envelope
		if err := client.Call(context.Background(), "get", map[string]string{"ref": "r1"}, &got); err != nil {
			t.Fatalf("Call() = %v, want no error", err)
		}
		if got.APIVersion != APIVersion {
			t.Errorf("api_version = %d, want %d", got.APIVersion, APIVersion)
		}
		if got.Config["socket"] != "/tmp/x" {
			t.Errorf("config = %+v, want the plugin's own section passed through verbatim", got.Config)
		}
	})

	t.Run("an error reply is quoted even when the plugin exits zero", func(t *testing.T) {
		t.Parallel()
		// A plugin that reports what went wrong is more useful than an exit
		// status, so the message is believed either way.
		dir := t.TempDir()
		path := writePlugin(t, dir, "polite", `echo '{"error":"no such pane"}'`)

		err := New(Found{Name: "polite", Path: path}).Call(context.Background(), "get", nil, nil)
		if err == nil || !strings.Contains(err.Error(), "no such pane") {
			t.Errorf("Call() = %v, want the plugin's own message", err)
		}
	})

	t.Run("stderr is logged and never parsed", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := writePlugin(t, dir, "chatty", `echo 'connecting...' >&2; echo '{"ref":"r1"}'`)

		var logged bytes.Buffer
		client := New(Found{Name: "chatty", Path: path})
		client.Stderr = &logged

		var result struct {
			Ref string `json:"ref"`
		}
		if err := client.Call(context.Background(), "get", nil, &result); err != nil {
			t.Fatalf("Call() = %v, want no error — stderr is diagnostics, not failure", err)
		}
		if result.Ref != "r1" {
			t.Errorf("result = %+v, want the stdout reply", result)
		}
		if !strings.Contains(logged.String(), "connecting...") {
			t.Errorf("stderr log = %q, want the plugin's diagnostics tagged and kept", logged.String())
		}
	})

	t.Run("a wedged plugin does not wedge the director", func(t *testing.T) {
		t.Parallel()
		// The regression that keeps being rediscovered. CommandContext alone
		// does not enforce the timeout: a plugin that leaves a child holding
		// the output pipe keeps Wait blocked long after the plugin is killed,
		// so the timeout does not actually bound anything. cmd.WaitDelay is
		// what makes it real, and this is the test that proves it.
		dir := t.TempDir()
		path := writePlugin(t, dir, "slow", `sleep 30 & echo waiting; wait`)

		client := New(Found{Name: "slow", Path: path})
		client.Timeout = 100 * time.Millisecond

		done := make(chan error, 1)
		go func() { done <- client.Call(context.Background(), "get", nil, nil) }()

		select {
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), "timed out") {
				t.Errorf("Call() = %v, want a timeout error", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("Call() never returned; the timeout is not being enforced")
		}
	})

	t.Run("a plugin that prints nothing is not an error", func(t *testing.T) {
		t.Parallel()
		// Some verbs have nothing useful to say.
		dir := t.TempDir()
		path := writePlugin(t, dir, "quiet", `true`)
		if err := New(Found{Name: "quiet", Path: path}).Call(context.Background(), "stop", nil, nil); err != nil {
			t.Errorf("Call() = %v, want no error", err)
		}
	})
}
