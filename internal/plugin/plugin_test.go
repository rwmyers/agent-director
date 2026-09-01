package plugin

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
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

	t.Run("one directory listed twice still yields one plugin", func(t *testing.T) {
		t.Parallel()
		// The binary's own directory is usually also a $PATH entry, and a
		// harness listed twice would be registered twice.
		dir := t.TempDir()
		writePlugin(t, dir, "dup", "echo '{}'")

		if found := DiscoverIn([]string{dir, dir}); len(found) != 1 {
			t.Errorf("DiscoverIn() = %+v, want the plugin once", found)
		}
	})
}

// locating returns a stand-in for os.Executable, so these tests never depend on
// where the test binary itself was built.
func locating(path string) func() (string, error) {
	return func() (string, error) { return path, nil }
}

func TestSearchDirs(t *testing.T) {
	t.Parallel()

	t.Run("the binary's own directory is searched even when $PATH omits it", func(t *testing.T) {
		t.Parallel()
		// The bug this exists to fix: `go install` puts director and its
		// plugins in ~/go/bin together, and somebody whose $PATH is missing
		// that directory has the plugins on disk and invisible.
		dirs := searchDirs([]string{"/usr/bin"}, locating("/home/someone/go/bin/director"))

		if !slices.Contains(dirs, "/home/someone/go/bin") {
			t.Errorf("searchDirs() = %q, want the binary's own directory included", dirs)
		}
	})

	t.Run("$PATH is searched before the binary's own directory", func(t *testing.T) {
		t.Parallel()
		// Ordering decides which copy wins, and a deliberately shadowed plugin
		// earlier on $PATH must keep winning.
		dirs := searchDirs([]string{"/first", "/second"}, locating("/home/someone/go/bin/director"))

		want := []string{"/first", "/second", "/home/someone/go/bin"}
		if !slices.Equal(dirs, want) {
			t.Errorf("searchDirs() = %q, want %q", dirs, want)
		}
	})

	t.Run("a directory already on $PATH is not searched twice", func(t *testing.T) {
		t.Parallel()
		dirs := searchDirs([]string{"/home/someone/go/bin", "/usr/bin"}, locating("/home/someone/go/bin/director"))

		want := []string{"/home/someone/go/bin", "/usr/bin"}
		if !slices.Equal(dirs, want) {
			t.Errorf("searchDirs() = %q, want %q", dirs, want)
		}
	})

	t.Run("differently spelled duplicates collapse too", func(t *testing.T) {
		t.Parallel()
		dirs := searchDirs([]string{"/usr/local/../bin/"}, locating("/usr/bin/director"))

		if len(dirs) != 1 {
			t.Errorf("searchDirs() = %q, want the one directory named twice", dirs)
		}
	})

	t.Run("a symlinked binary contributes the invoked and the real directory", func(t *testing.T) {
		t.Parallel()
		// Two different arrangements, not two degrees of correctness: plugins
		// may sit beside the name that was invoked, or beside the real binary.
		root := t.TempDir()
		real, link := filepath.Join(root, "real"), filepath.Join(root, "link")
		for _, dir := range []string{real, link} {
			if err := os.Mkdir(dir, 0o750); err != nil {
				t.Fatal(err)
			}
		}
		binary := filepath.Join(real, "director")
		if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o700); err != nil { // #nosec G306 -- a test fixture that must be executable
			t.Fatal(err)
		}
		invoked := filepath.Join(link, "director")
		if err := os.Symlink(binary, invoked); err != nil {
			t.Fatal(err)
		}

		dirs := searchDirs(nil, locating(invoked))

		resolved, err := filepath.EvalSymlinks(real)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{link, resolved} {
			if !slices.Contains(dirs, want) {
				t.Errorf("searchDirs() = %q, want it to include %q", dirs, want)
			}
		}
	})

	t.Run("an unlocatable binary leaves $PATH working", func(t *testing.T) {
		t.Parallel()
		// Asking what harnesses exist must never fail because os.Executable
		// could not answer.
		dirs := searchDirs([]string{"/usr/bin"}, func() (string, error) { return "", errors.New("no") })

		if !slices.Equal(dirs, []string{"/usr/bin"}) {
			t.Errorf("searchDirs() = %q, want $PATH alone", dirs)
		}
	})
}

func TestDiscoverBesideTheBinary(t *testing.T) {
	t.Parallel()

	t.Run("a plugin beside the binary is found when that directory is not on $PATH", func(t *testing.T) {
		t.Parallel()
		beside, elsewhere := t.TempDir(), t.TempDir()
		writePlugin(t, beside, "neighbour", "echo '{}'")

		found := DiscoverIn(searchDirs([]string{elsewhere}, locating(filepath.Join(beside, "director"))))

		if len(found) != 1 || found[0].Name != "neighbour" {
			t.Errorf("DiscoverIn(searchDirs(...)) = %+v, want the plugin beside the binary", found)
		}
	})

	t.Run("that plugin is found once when the directory is also on $PATH", func(t *testing.T) {
		t.Parallel()
		beside := t.TempDir()
		writePlugin(t, beside, "neighbour", "echo '{}'")

		found := DiscoverIn(searchDirs([]string{beside}, locating(filepath.Join(beside, "director"))))

		if len(found) != 1 {
			t.Errorf("DiscoverIn(searchDirs(...)) = %+v, want the plugin once, not once per directory", found)
		}
	})

	t.Run("a copy earlier on $PATH still shadows the one beside the binary", func(t *testing.T) {
		t.Parallel()
		shadow, beside := t.TempDir(), t.TempDir()
		wanted := writePlugin(t, shadow, "neighbour", "echo '{}'")
		writePlugin(t, beside, "neighbour", "echo '{}'")

		found := DiscoverIn(searchDirs([]string{shadow}, locating(filepath.Join(beside, "director"))))

		if len(found) != 1 || found[0].Path != wanted {
			t.Errorf("DiscoverIn(searchDirs(...)) = %+v, want the copy the user put on $PATH", found)
		}
	})
}

// busy is the failure the kernel reports for an executable that something still
// holds open for writing, shaped the way exec.Cmd.Start reports it.
func busy(path string) error {
	return &fs.PathError{Op: "fork/exec", Path: path, Err: syscall.ETXTBSY}
}

func TestRetryWhileBusy(t *testing.T) {
	t.Parallel()
	// The failure is injected rather than raced for: what is under test is the
	// policy — what is retried, how often, and what stops it — and that answer
	// has to be the same every run.

	t.Run("an executable that stops being written is started", func(t *testing.T) {
		t.Parallel()
		attempts := 0
		err := retryWhileBusy(context.Background(), func() error {
			attempts++
			if attempts < 3 {
				return busy("/somewhere/" + Prefix + "installing")
			}
			return nil
		})

		if err != nil {
			t.Fatalf("retryWhileBusy() = %v, want the start that eventually succeeded", err)
		}
		if attempts != 3 {
			t.Errorf("attempts = %d, want it to keep trying until the writer let go", attempts)
		}
	})

	t.Run("any other failure to start is returned at once", func(t *testing.T) {
		t.Parallel()
		// A plugin that is missing, unreadable or the wrong architecture is
		// broken now and will be broken in a second, and must say so now.
		refused := errors.New("permission denied")
		attempts := 0
		began := time.Now()

		err := retryWhileBusy(context.Background(), func() error {
			attempts++
			return refused
		})

		if !errors.Is(err, refused) {
			t.Fatalf("retryWhileBusy() = %v, want the underlying failure unchanged", err)
		}
		if attempts != 1 {
			t.Errorf("attempts = %d, want exactly one — only ETXTBSY is worth another try", attempts)
		}
		if waited := time.Since(began); waited > busyDelay {
			t.Errorf("returned after %s, want no backoff at all for a permanent failure", waited)
		}
	})

	t.Run("an executable that never frees up gives up, naming the condition", func(t *testing.T) {
		t.Parallel()
		attempts := 0
		began := time.Now()

		err := retryWhileBusy(context.Background(), func() error {
			attempts++
			return busy("/somewhere/" + Prefix + "wedged")
		})

		if attempts != busyAttempts {
			t.Fatalf("attempts = %d, want the loop to stop after %d", attempts, busyAttempts)
		}
		if !errors.Is(err, errBusyExecutable) {
			t.Errorf("retryWhileBusy() = %v, want it to name the condition", err)
		}
		if !errors.Is(err, syscall.ETXTBSY) {
			t.Errorf("retryWhileBusy() = %v, want the underlying syscall kept as well", err)
		}
		if waited := time.Since(began); waited > DescribeTimeout {
			t.Errorf("gave up after %s, want it inside the first call's own budget", waited)
		}
	})

	t.Run("a finished context outranks the backoff", func(t *testing.T) {
		t.Parallel()
		// Somebody who has already stopped caring must not be held for the rest
		// of the schedule.
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		attempts := 0
		began := time.Now()

		err := retryWhileBusy(ctx, func() error {
			attempts++
			return busy("/somewhere/" + Prefix + "wedged")
		})

		if attempts != 1 {
			t.Errorf("attempts = %d, want the cancelled context to end it immediately", attempts)
		}
		if !errors.Is(err, syscall.ETXTBSY) {
			t.Errorf("retryWhileBusy() = %v, want the failure it stopped on", err)
		}
		if waited := time.Since(began); waited > busyDelay {
			t.Errorf("returned after %s, want no waiting once the context is done", waited)
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

	t.Run("a plugin still being written is run once the writer lets go", func(t *testing.T) {
		t.Parallel()
		// What a person meets during `go install` or an upgrade: the file is
		// there and correct, and for a moment the kernel will not execute it
		// because somebody still holds it open for writing.
		dir := t.TempDir()
		path := writePlugin(t, dir, "installing", `echo '{"ref":"r1"}'`)

		holder, err := os.OpenFile(path, os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		var once sync.Once
		release := func() { once.Do(func() { _ = holder.Close() }) }
		t.Cleanup(release)
		time.AfterFunc(20*time.Millisecond, release)

		var result struct {
			Ref string `json:"ref"`
		}
		if err := New(Found{Name: "installing", Path: path}).Call(context.Background(), "get", nil, &result); err != nil {
			t.Fatalf("Call() = %v, want the call to wait the writer out and succeed", err)
		}
		if result.Ref != "r1" {
			t.Errorf("result = %+v, want the plugin's reply", result)
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
