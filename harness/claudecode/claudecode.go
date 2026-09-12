// Package claudecode drives Claude Code conversations.
//
// It is a built-in adapter, but it has no privileges: it registers through the
// same harness.Register as any third-party adapter and is resolved the same
// way. If it could reach past the public contract, that contract would rot
// unnoticed until somebody outside this repository tried to use it.
//
// # What this adapter relies on
//
// Claude Code lets the caller assign a session identifier before the process
// exists (--session-id), which is what lets director mint an engagement's
// identity up front and record the binding before spawning. It keeps one file
// per live process under ~/.claude/sessions, which is the liveness oracle: a
// session with no file there has no process attached. And it appends a JSONL
// transcript per conversation, whose modification time is a cheap activity
// clock that does not depend on the agent cooperating.
package claudecode

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/rwmyers/agent-director/harness"
)

func init() { harness.Register(New()) }

// Name is the registry name, the [harness.claude-code] config section, and what
// a workflow pins.
const Name = "claude-code"

// Adapter drives Claude Code.
type Adapter struct {
	// Binary is the executable to run. Overridable so a test can point at a
	// stub and so a user can run a wrapper.
	Binary string
	// Home is ~/.claude. Overridable for tests.
	Home string
}

// New builds an adapter with the defaults.
func New() *Adapter {
	home, _ := os.UserHomeDir()
	return &Adapter{
		Binary: "claude",
		Home:   filepath.Join(home, ".claude"),
	}
}

// Name identifies the adapter.
func (a *Adapter) Name() string { return Name }

// SkillLocations tells `director setup` where Claude Code looks for skills.
//
// It goes through harness.SkillInstaller like anything else. Being built in
// buys no shortcut: if this adapter could hand the installer a path the public
// contract cannot carry, the contract would rot until somebody outside this
// repository needed it.
//
// Presence is the existence of the home directory rather than the binary on
// $PATH, because the directory is what makes an installed skill reachable — a
// claude on $PATH with no ~/.claude has nothing to read the skills out of.
func (a *Adapter) SkillLocations() (harness.SkillLocations, error) {
	_, err := os.Stat(a.Home)
	return harness.SkillLocations{
		Description: "Claude Code",
		GlobalDir:   filepath.Join(a.Home, "skills"),
		ProjectDir:  filepath.Join(".claude", "skills"),
		Verified:    true,
		Present:     err == nil,
	}, nil
}

// The environment Claude Code sets in a conversation's own process. EnvInside
// is what says we are in one at all; EnvSession names which conversation, and
// may be absent, which is reported as being inside regardless.
const (
	EnvInside  = "CLAUDECODE"
	EnvSession = "CLAUDE_CODE_SESSION_ID"
)

// Hosts declares what Claude Code offers a director running as one of its
// conversations.
//
// Background only. A Claude Code conversation can run a command in the
// background and be handed control when it exits. It cannot be woken: Send here
// is `claude --print --resume <ref>`, a fresh headless process against the
// transcript, which produces a turn nobody is looking at rather than a turn in
// the conversation the person has open. Declaring wake would have the director
// hand back promising a watcher that does not exist.
func (a *Adapter) Hosts() harness.Hosting {
	return harness.Hosting{Background: true, Wake: false}
}

// Locate reports whether this process is running inside a Claude Code
// conversation, and which one.
//
// The session id is a label here and nothing dials it, because this adapter
// declares no wake — so an empty one costs nothing and is still reported as
// being inside, rather than turning a correct answer into a wrong one.
func (a *Adapter) Locate() (string, bool) {
	if os.Getenv(EnvInside) != "1" {
		return "", false
	}
	return os.Getenv(EnvSession), true
}

// Enforceable lists what this adapter can control. All six appear because
// Claude Code's tool allowlist can reach every one of them; whether a given
// combination is enforceable is Permits' business.
func (a *Adapter) Enforceable() []harness.Capability { return harness.AllCapabilities }

// Permits applies the fail-closed rule to a specific allow-set.
//
// The awkward case is push. Publishing is not a distinct tool — it is `git
// push` through the shell — so it can be denied only by denying the shell.
// When execute is withheld there is nothing to push from and the denial holds;
// when execute is granted it does not, and this refuses rather than pretending.
func (a *Adapter) Permits(allow []harness.Capability) error {
	if !harness.Allows(allow, harness.CapPush) && harness.Allows(allow, harness.CapExecute) {
		return fmt.Errorf(
			"%s cannot withhold %q while granting %q: publishing goes through the shell, so denying it means denying commands entirely. "+
				"Either allow push, or drop execute from the scope",
			Name, harness.CapPush, harness.CapExecute)
	}
	if !harness.Allows(allow, harness.CapSearch) && harness.Allows(allow, harness.CapNetwork) {
		return fmt.Errorf(
			"%s cannot withhold %q while granting %q: both are served by the same web tools",
			Name, harness.CapSearch, harness.CapNetwork)
	}
	if len(allow) == 0 {
		return fmt.Errorf("%s cannot run an agent with no capabilities at all", Name)
	}
	return nil
}

// allowedTools maps capabilities onto Claude Code's tool allowlist.
//
// An allowlist rather than a denylist, because a denylist is wrong by default
// the moment a new tool ships: an agent would silently gain it. Anything not
// named here is unavailable.
func allowedTools(allow []harness.Capability) []string {
	var tools []string
	add := func(names ...string) { tools = append(tools, names...) }

	// The callback channel is always available, whatever the scope withholds.
	//
	// It is not one of the capabilities a workflow grants — it is how the
	// engagement exists at all. A read-only agent has no shell, and if that
	// also took away its ability to report progress or ask a question, then the
	// most constrained agents would be exactly the ones the director could
	// never hear from: they would work correctly, finish, and be recorded as
	// abandoned. Narrowed to the director command itself, so this grants no
	// general shell.
	add("Bash(director report:*)", "Bash(director ask:*)")

	if harness.Allows(allow, harness.CapRead) {
		add("Read", "Glob", "Grep", "NotebookRead")
	}
	if harness.Allows(allow, harness.CapSearch) || harness.Allows(allow, harness.CapNetwork) {
		add("WebSearch", "WebFetch")
	}
	if harness.Allows(allow, harness.CapEdit) {
		add("Edit", "Write", "NotebookEdit")
	}
	if harness.Allows(allow, harness.CapExecute) {
		add("Bash", "BashOutput", "KillShell")
	}
	sort.Strings(tools)
	return tools
}

// Spawn starts a background conversation and returns as soon as it is
// addressable.
//
// The process is started detached, in its own process group, with output to a
// log file. Detaching matters because director is a short-lived command: if the
// agent were a child in this process group it would die with the command that
// launched it, which is the opposite of delegating work.
func (a *Adapter) Spawn(ctx context.Context, req harness.SpawnRequest) (harness.SpawnResult, error) {
	if _, err := exec.LookPath(a.Binary); err != nil {
		return harness.SpawnResult{}, fmt.Errorf("%s is not on $PATH: %w", a.Binary, err)
	}

	// Claude Code requires its session id to be a UUID, and director's
	// engagement ids are deliberately not — they are prefixed and short so a
	// human can read one in a log. This is the whole reason identity is two
	// fields: the engagement keeps the id director minted, and the harness gets
	// a ref in the shape it insists on. Nothing outside this adapter sees it.
	ref := req.ID
	if !isUUID(ref) {
		minted, err := newUUID()
		if err != nil {
			return harness.SpawnResult{}, err
		}
		ref = minted
	}

	args := a.spawnArgs(ref, req)
	return a.start(ctx, ref, args, req)
}

// start launches the harness process and watches briefly for an immediate
// failure. Shared by Spawn and Resume, which differ only in the argv they
// build — duplicating the launch would mean a resumed engagement could quietly
// lose the detaching, the logging, or the failure probe.
func (a *Adapter) start(_ context.Context, ref string, args []string, req harness.SpawnRequest) (harness.SpawnResult, error) {
	workDir := filepath.Join(a.Home, "director")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return harness.SpawnResult{}, err
	}
	logPath := filepath.Join(workDir, ref+".log")
	promptPath := filepath.Join(workDir, ref+".prompt")

	// The prompt goes in on stdin rather than as an argument, for two reasons.
	// Claude Code's --allowedTools is variadic, so a trailing positional prompt
	// is silently consumed as another tool name and the agent then starts with
	// no input at all. And a composed prompt — task instructions, the reporting
	// contract, and the brief — has no small upper bound, while an argument
	// list does. Keeping the file also means the exact text an agent was given
	// can be read back afterwards, which is the first thing anyone wants when
	// an engagement did something surprising.
	if err := os.WriteFile(promptPath, []byte(req.Prompt), 0o600); err != nil {
		return harness.SpawnResult{}, err
	}
	promptFile, err := os.Open(promptPath) // #nosec G304 -- path composed from a minted ref
	if err != nil {
		return harness.SpawnResult{}, err
	}
	defer func() { _ = promptFile.Close() }()

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304 -- path composed from a minted ref
	if err != nil {
		return harness.SpawnResult{}, err
	}
	defer func() { _ = logFile.Close() }()

	cmd := exec.Command(a.Binary, args...) // #nosec G204 -- binary is configured, args are composed here
	cmd.Dir = req.Dir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = promptFile
	cmd.Env = append(os.Environ(), envList(req.Env)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		return harness.SpawnResult{}, err
	}

	// Reap in the background so the child does not become a zombie for the
	// lifetime of this process. The command exits long before the agent does,
	// at which point the agent is reparented to init.
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	// Watch briefly for an immediate failure.
	//
	// Detaching means nothing waits for the agent, so without this a spawn that
	// died on its first line — a rejected flag, a bad session id, a missing
	// permission — would be reported as a successful spawn and only surface
	// much later as an engagement that never produced anything. That is a slow,
	// confusing failure for something the harness already knew instantly.
	select {
	case err := <-exited:
		if err != nil {
			return harness.SpawnResult{}, fmt.Errorf("%s exited immediately: %w: %s",
				a.Binary, err, strings.TrimSpace(tailFile(logPath, 400)))
		}
	case <-time.After(startupProbe):
		// Still alive, which is all we can usefully learn without blocking the
		// director for the length of the agent's first turn.
	}

	return harness.SpawnResult{
		Ref: ref,
		Detail: map[string]string{
			"pid":        fmt.Sprint(cmd.Process.Pid),
			"log":        logPath,
			"prompt":     promptPath,
			"transcript": a.transcriptPath(req.Dir, ref),
		},
	}, nil
}

// startupProbe is how long Spawn watches for an immediate exit.
//
// It has to outlast the harness binary's own startup, or it never sees the
// failure it exists to catch: a node process rejecting an argument still takes
// most of a second to get there. Long enough to catch that, short enough that
// dispatching several engagements in a row still feels immediate.
const startupProbe = 2 * time.Second

// tailFile returns the last n bytes of a file, for quoting a failure back.
func tailFile(path string, n int64) string {
	handle, err := os.Open(path) // #nosec G304 -- path composed from a minted ref
	if err != nil {
		return ""
	}
	defer func() { _ = handle.Close() }()

	info, err := handle.Stat()
	if err != nil {
		return ""
	}
	if info.Size() > n {
		if _, err := handle.Seek(-n, io.SeekEnd); err != nil {
			return ""
		}
	}
	body, err := io.ReadAll(handle)
	if err != nil {
		return ""
	}
	return string(body)
}

// newUUID mints a v4 UUID, which is the only session identifier Claude Code
// accepts.
func newUUID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	raw[6] = (raw[6] & 0x0f) | 0x40 // version 4
	raw[8] = (raw[8] & 0x3f) | 0x80 // variant 10
	hexed := hex.EncodeToString(raw)
	return fmt.Sprintf("%s-%s-%s-%s-%s", hexed[0:8], hexed[8:12], hexed[12:16], hexed[16:20], hexed[20:32]), nil
}

// isUUID reports whether a string is shaped like a UUID, so that an id which
// already qualifies is used as-is rather than replaced.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
			if !isHex {
				return false
			}
		}
	}
	return true
}

// spawnArgs builds the argv. Kept separate and pure so tests can assert the
// exact command line without executing anything.
func (a *Adapter) spawnArgs(ref string, req harness.SpawnRequest) []string {
	args := []string{
		"--print",
		"--session-id", ref,
		"--output-format", "text",
	}
	if req.Name != "" {
		// Shown in the prompt box, the /resume picker and the terminal title,
		// so a person looking at Claude Code directly can tell which of several
		// engagements in one repository they are looking at. Without it the
		// name is derived from the working directory and siblings collide.
		args = append(args, "--name", req.Name)
	}
	if tools := allowedTools(req.Allow); len(tools) > 0 {
		args = append(args, "--allowedTools", strings.Join(tools, " "))
	}
	// With no shell and no editing there is nothing to approve, so prompting
	// would only strand the agent waiting on a human who is not watching.
	if harness.Allows(req.Allow, harness.CapEdit) || harness.Allows(req.Allow, harness.CapExecute) {
		args = append(args, "--permission-mode", "acceptEdits")
	}
	return args
}

func envList(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+env[key])
	}
	return out
}

// Send resumes a conversation with new text.
func (a *Adapter) Send(ctx context.Context, req harness.SendRequest) error {
	cmd := exec.CommandContext(ctx, a.Binary, // #nosec G204 -- binary is configured
		"--print", "--resume", req.Ref, "--output-format", "text", req.Text)
	cmd.WaitDelay = 2 * time.Second
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("resuming %s: %w: %s", req.Ref, err, strings.TrimSpace(string(output)))
	}
	return nil
}

// Get observes one conversation.
func (a *Adapter) Get(ctx context.Context, ref string) (harness.Observation, error) {
	sessions, err := a.liveSessions()
	if err != nil {
		return harness.Observation{}, err
	}
	return a.observe(ref, sessions), nil
}

// List observes every conversation this adapter can see. It reports only live
// ones, because a finished conversation is only discoverable through the
// director's own records — Claude Code keeps no index of them that maps back to
// an engagement.
func (a *Adapter) List(ctx context.Context, filter harness.Filter) ([]harness.Observation, error) {
	sessions, err := a.liveSessions()
	if err != nil {
		return nil, err
	}
	var found []harness.Observation
	for ref, session := range sessions {
		if filter.Dir != "" && session.CWD != filter.Dir {
			continue
		}
		found = append(found, a.observe(ref, sessions))
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Ref < found[j].Ref })
	return found, nil
}

// Stop ends a conversation by signalling its process.
//
// There is no clean interrupt for a detached agent, so the interrupt mode is
// refused rather than implemented as a kill under a gentler name. A director
// told it interrupted a turn, when in fact it terminated the process, would
// draw the wrong conclusion about what it could resume.
func (a *Adapter) Stop(ctx context.Context, req harness.StopRequest) error {
	if req.Mode == harness.StopInterrupt {
		return fmt.Errorf("%s cannot interrupt a single turn of a detached agent; use --mode end", Name)
	}

	sessions, err := a.liveSessions()
	if err != nil {
		return err
	}
	session, ok := sessions[req.Ref]
	if !ok {
		return nil // already gone; stopping is idempotent
	}
	process, err := os.FindProcess(session.PID)
	if err != nil {
		return nil
	}
	if err := process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("stopping pid %d: %w", session.PID, err)
	}
	return nil
}

// sessionFile is one entry under ~/.claude/sessions — one file per live
// process, which is what makes it a liveness oracle rather than a history.
type sessionFile struct {
	PID       int    `json:"pid"`
	SessionID string `json:"sessionId"`
	CWD       string `json:"cwd"`
	Status    string `json:"status"`
	UpdatedAt int64  `json:"updatedAt"`
	StartedAt int64  `json:"startedAt"`
}

func (a *Adapter) liveSessions() (map[string]sessionFile, error) {
	dir := filepath.Join(a.Home, "sessions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]sessionFile{}, nil
		}
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}

	sessions := map[string]sessionFile{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name())) // #nosec G304 -- listing our own home dir
		if err != nil {
			continue // a session file can vanish mid-read; that just means not live
		}
		var session sessionFile
		if err := json.Unmarshal(raw, &session); err != nil {
			continue
		}
		if session.SessionID != "" {
			sessions[session.SessionID] = session
		}
	}
	return sessions, nil
}

// observe derives lifecycle for one ref.
func (a *Adapter) observe(ref string, sessions map[string]sessionFile) harness.Observation {
	observation := harness.Observation{Ref: ref, Detail: map[string]string{}}

	session, live := sessions[ref]
	if live {
		observation.Found = true
		observation.Lifecycle = lifecycleFor(session.Status)
		observation.Detail["pid"] = fmt.Sprint(session.PID)
		observation.Detail["claude_status"] = session.Status
		if session.UpdatedAt > 0 {
			observation.LastActivityAt = time.UnixMilli(session.UpdatedAt)
		}
		if transcript, at, ok := a.transcriptActivity(session.CWD, ref); ok {
			observation.Detail["transcript"] = transcript
			if at.After(observation.LastActivityAt) {
				observation.LastActivityAt = at
			}
		}
		return observation
	}

	// No live process. Either it ran and finished, or it never came up.
	if transcript, at, ok := a.findTranscript(ref); ok {
		observation.Found = true
		observation.Lifecycle = harness.LifecycleDone
		observation.LastActivityAt = at
		observation.Detail["transcript"] = transcript
		return observation
	}

	// Nothing anywhere. Report not-found and let the core decide: it knows when
	// the engagement was spawned and this adapter does not, so only the core
	// can tell "still starting" from "never started".
	observation.Found = false
	return observation
}

// lifecycleFor maps Claude Code's own status string.
//
// The mapping is deliberately conservative about values it has not seen. The
// observed set already includes at least busy, idle and waiting, so treating
// the field as a closed enum would be wrong; anything unrecognised becomes
// unknown rather than being assumed benign.
func lifecycleFor(status string) harness.Lifecycle {
	switch status {
	case "busy", "running", "working":
		return harness.LifecycleWorking
	case "idle", "waiting", "ready":
		return harness.LifecycleIdle
	default:
		return harness.LifecycleUnknown
	}
}

// projectSlug is how Claude Code names a working directory's transcript
// directory: every path separator becomes a dash, including the leading one.
func projectSlug(dir string) string {
	return strings.ReplaceAll(dir, string(filepath.Separator), "-")
}

func (a *Adapter) transcriptPath(dir, ref string) string {
	return filepath.Join(a.Home, "projects", projectSlug(dir), ref+".jsonl")
}

func (a *Adapter) transcriptActivity(dir, ref string) (string, time.Time, bool) {
	path := a.transcriptPath(dir, ref)
	info, err := os.Stat(path)
	if err != nil {
		return "", time.Time{}, false
	}
	return path, info.ModTime(), true
}

// findTranscript locates a conversation's transcript without knowing which
// working directory it ran in.
//
// The modification time is the activity clock. It is one stat per candidate
// directory, which is cheap enough to call on every status — and, crucially, it
// moves only when the agent actually does something, so it detects a wedged
// agent that has stopped reporting but claims to be working.
func (a *Adapter) findTranscript(ref string) (string, time.Time, bool) {
	root := filepath.Join(a.Home, "projects")
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", time.Time{}, false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(root, entry.Name(), ref+".jsonl")
		if info, err := os.Stat(path); err == nil {
			return path, info.ModTime(), true
		}
	}
	return "", time.Time{}, false
}
