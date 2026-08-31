// Package plugin is the transport for harness adapters that live outside this
// binary.
//
// A plugin is an executable named director-harness-<name> on $PATH — the
// convention git, kubectl and docker all use. Anything matching is loadable;
// there is no allowlist. director runs it once per call with the verb as its
// only argument, writes a JSON request on stdin, and reads a JSON result from
// stdout:
//
//		{"api_version": 1, "config": {"socket": "/tmp/x"}, "params": {...}}
//
//	  - exit 0, with the result object on stdout
//	  - non-zero exit, with {"error": "..."} on stdout
//
// Anything on stderr is logged and never parsed, so a plugin is free to write
// progress there. director owns the timeout.
//
// Every plugin must answer the describe verb, and it is the first thing called.
// It reports the protocol version the plugin speaks — director refuses to load
// one it does not implement rather than guessing — along with the plugin's own
// name and version.
//
// # Why exec and JSON
//
// Not Go's plugin package: it requires an identical toolchain and identical
// versions of every shared dependency, which makes third-party builds
// impossible. Not gRPC: the data flow is a handful of one-shot verbs, and the
// code generation is not repaid. Not WASM: a harness adapter's whole job is
// reading paths under $HOME and talking to local sockets, so sandboxing it
// means punching enough holes that the sandbox stops meaning anything.
package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// APIVersion is the protocol version this director speaks. A plugin reporting
// anything else from describe is refused rather than guessed at.
const APIVersion = 1

// Prefix is what a harness plugin executable is called.
const Prefix = "director-harness-"

// DescribeVerb is the one verb every plugin must answer.
const DescribeVerb = "describe"

// Timeouts. Describe is called on a plugin before it is used at all, so it is
// held to a short one; the rest may be starting a process or waiting on a
// socket.
const (
	DescribeTimeout = 10 * time.Second
	DefaultTimeout  = 2 * time.Minute
	// orphanGrace is how long a killed plugin's descendants are given to
	// release its output pipe before director stops waiting on them.
	orphanGrace = 2 * time.Second
)

// ErrUnsupportedAPI is returned when a plugin speaks a protocol version this
// director does not.
var ErrUnsupportedAPI = errors.New("unsupported plugin api version")

// Found is a discovered executable that has not been run yet. Discovery reads
// directory entries only, so finding plugins costs nothing until one is used —
// which is why `director --help` never executes anything.
type Found struct {
	Name string
	Path string
}

// Discover returns the harness plugins on $PATH.
func Discover() []Found { return DiscoverIn(filepath.SplitList(os.Getenv("PATH"))) }

// DiscoverIn is Discover over an explicit set of directories.
//
// It exists so tests can exercise discovery without t.Setenv("PATH", ...),
// which is process-global and rules out t.Parallel. Where two directories hold
// the same plugin the earlier wins, matching how a shell would resolve it.
func DiscoverIn(dirs []string) []Found {
	seen := map[string]Found{}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			name, ok := strings.CutPrefix(entry.Name(), Prefix)
			if !ok || name == "" {
				continue
			}
			if _, taken := seen[name]; taken {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			if !executable(path) {
				continue
			}
			seen[name] = Found{Name: name, Path: path}
		}
	}

	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)

	found := make([]Found, 0, len(names))
	for _, name := range names {
		found = append(found, seen[name])
	}
	return found
}

// executable reports whether path is a regular file anyone may run. A directory
// or a stray data file matching the naming pattern is skipped rather than
// treated as a broken plugin.
func executable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode()&0o111 != 0
}

// Description is the reply to describe.
type Description struct {
	APIVersion int      `json:"api_version"`
	Name       string   `json:"name"`
	Version    string   `json:"version"`
	Enforces   []string `json:"enforces"`
	// Skills is where director's skills belong for this harness. Omitting it
	// says the harness has no place for them, and `director install` passes
	// over the plugin rather than offering a target it cannot write.
	Skills *Skills `json:"skills,omitempty"`
}

// Skills is the optional skills block of a describe reply.
//
// A pointer on Description rather than a set of bare fields, because "no skills
// location" and "a skills location whose fields are all empty" are different
// answers and the installer acts differently on each.
type Skills struct {
	// Description is the harness's name as a person would write it.
	Description string `json:"description"`
	// GlobalDir is an absolute directory covering every project; the plugin
	// expands its own home. Empty means the harness has no global scope.
	GlobalDir string `json:"global_dir"`
	// ProjectDir is relative to a repository root. Empty means the harness has
	// no project scope. Relative because the root is director's to choose.
	ProjectDir string `json:"project_dir"`
	// Verified says these paths were confirmed against a real installation
	// rather than read off documentation.
	Verified bool `json:"verified"`
	// Present says the harness looks installed on this machine. It only
	// pre-selects a checkbox, so a plugin that cannot tell says false and the
	// target is still offered.
	Present bool `json:"present"`
}

// Client runs one plugin. A call is a fresh process, so a Client holds no
// connection and is cheap to keep around.
type Client struct {
	Name string
	Path string
	// Config is the plugin's own [harness.<name>] section, passed through
	// verbatim on every call. director does not look inside it — validating it
	// would mean knowing about every harness that will ever exist.
	Config map[string]string
	// Timeout bounds a single call. Zero means DefaultTimeout.
	Timeout time.Duration
	// Stderr receives whatever the plugin writes there, tagged with its name.
	// Nil discards it.
	Stderr io.Writer
}

// New builds a client for a discovered plugin.
func New(found Found) *Client { return &Client{Name: found.Name, Path: found.Path} }

// String names the plugin the way error messages should.
func (c *Client) String() string { return Prefix + c.Name }

type envelope struct {
	APIVersion int               `json:"api_version"`
	Config     map[string]string `json:"config,omitempty"`
	Params     any               `json:"params,omitempty"`
}

type errorReply struct {
	Error string `json:"error"`
}

// Describe runs the mandatory describe verb and checks the protocol version.
// Everything else about a plugin follows from what this returns, so a plugin
// that fails here is not used at all — and it fails with that complaint rather
// than with whatever its other verbs happen to do.
func (c *Client) Describe(ctx context.Context) (*Description, error) {
	var description Description
	if err := c.call(ctx, DescribeTimeout, DescribeVerb, nil, &description); err != nil {
		return nil, err
	}
	if description.APIVersion != APIVersion {
		return nil, fmt.Errorf("%s: %w: plugin speaks %d, director speaks %d",
			c, ErrUnsupportedAPI, description.APIVersion, APIVersion)
	}
	return &description, nil
}

// Call runs a verb.
func (c *Client) Call(ctx context.Context, verb string, params, result any) error {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return c.call(ctx, timeout, verb, params, result)
}

func (c *Client) call(ctx context.Context, timeout time.Duration, verb string, params, result any) error {
	body, err := json.Marshal(envelope{APIVersion: APIVersion, Config: c.Config, Params: params})
	if err != nil {
		return fmt.Errorf("%s %s: encoding request: %w", c, verb, err)
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, c.Path, verb) // #nosec G204 -- path came from $PATH discovery
	cmd.Stdin = bytes.NewReader(body)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// Killing the plugin on timeout is not enough on its own. Anything it
	// spawned inherits the output pipe and can hold it open after its parent is
	// gone, and Wait does not return until that pipe closes — so without a
	// WaitDelay a plugin that leaves a child behind wedges director anyway,
	// which is the one thing the timeout exists to prevent. After the delay the
	// pipes are closed regardless and whatever has been read so far is what we
	// get.
	cmd.WaitDelay = orphanGrace
	runErr := cmd.Run()

	c.logStderr(verb, stderr.Bytes())

	// A plugin that says what went wrong is quoted rather than reduced to its
	// exit status, whether or not it also exited non-zero.
	if message := decodeError(stdout.Bytes()); message != "" {
		return fmt.Errorf("%s %s: %s", c, verb, message)
	}
	if ctx.Err() != nil {
		return fmt.Errorf("%s %s: timed out after %s", c, verb, timeout)
	}
	if runErr != nil {
		return fmt.Errorf("%s %s: %w", c, verb, runErr)
	}

	if result == nil || len(bytes.TrimSpace(stdout.Bytes())) == 0 {
		return nil
	}
	if err := json.Unmarshal(stdout.Bytes(), result); err != nil {
		return fmt.Errorf("%s %s: decoding reply: %w", c, verb, err)
	}
	return nil
}

// decodeError returns the message from an {"error": "..."} reply, or "" when
// the output is not one.
func decodeError(out []byte) string {
	if len(bytes.TrimSpace(out)) == 0 {
		return ""
	}
	var reply errorReply
	if err := json.Unmarshal(out, &reply); err != nil {
		return ""
	}
	return reply.Error
}

// logStderr forwards a plugin's diagnostics, tagged so it is obvious they are
// not director's own.
func (c *Client) logStderr(verb string, out []byte) {
	if c.Stderr == nil || len(bytes.TrimSpace(out)) == 0 {
		return
	}
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		_, _ = fmt.Fprintf(c.Stderr, "%s %s: %s\n", c, verb, line)
	}
}
