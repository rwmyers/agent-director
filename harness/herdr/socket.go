// Package herdr drives agents running inside herdr, a terminal multiplexer
// built to supervise AI coding agents.
//
// # Why the socket and not the CLI
//
// herdr's CLI mirrors its API verb for verb, but prints text for humans — a
// failed `herdr agent list` reports `Error: Os { code: 111 }` rather than
// anything parseable. Only `herdr api schema` and `herdr api snapshot` emit
// JSON. So this adapter speaks the socket directly.
//
// # The shape herdr imposes
//
// herdr assigns identity itself, after a pane exists, which is the opposite of
// Claude Code accepting a caller-assigned session id. That is why an engagement
// has both an ID minted by director and a Ref belonging to the harness.
//
// Starting an agent is therefore two calls: create a pane, then start an agent
// in it. `agent.start` requires the pane to be sitting at an interactive shell
// prompt, and takes trailing arguments for the agent binary — which is what
// lets a herdr-hosted Claude Code conversation still be given director's own
// session id.
//
// # Not running is normal
//
// The herdr server is frequently not running. That is a first-class,
// non-fatal state: it produces an error naming the socket, never a fleet of
// engagements reported as "unknown". A stopped server must not look like a
// crowd of confused agents.
package herdr

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

// ErrServerNotRunning means the socket could not be reached at all.
var ErrServerNotRunning = errors.New("herdr server is not running")

// dialTimeout bounds a connection attempt. The socket is local, so a slow
// connect means something is wrong rather than merely busy.
const dialTimeout = 3 * time.Second

// callTimeout bounds one request. Deliberately generous: agent.start waits for
// an agent to reach interactive readiness, which herdr itself allows 30s for.
const callTimeout = 45 * time.Second

// client speaks herdr's newline-delimited JSON-RPC over a Unix socket.
//
// A connection per call rather than a persistent one. Every director command is
// a separate short-lived process, so a pooled connection would have nothing to
// pool across, and a stale socket file — which is what a crashed server leaves
// behind — is then discovered at the moment it matters rather than cached.
type client struct {
	path string
	seq  atomic.Uint64
}

// socketPath resolves where the API socket lives: explicit configuration, then
// herdr's own environment variable, then the default location.
func socketPath(configured string) string {
	if configured != "" {
		return configured
	}
	if fromEnv := os.Getenv("HERDR_SOCKET_PATH"); fromEnv != "" {
		return fromEnv
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "herdr", "herdr.sock")
}

type request struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params,omitempty"`
}

type response struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *responseError  `json:"error"`
}

type responseError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *responseError) Error() string {
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

// call sends one request and decodes the reply into result.
func (c *client) call(method string, params, result any) error {
	if c.path == "" {
		return fmt.Errorf("%w: no socket path could be determined", ErrServerNotRunning)
	}

	conn, err := net.DialTimeout("unix", c.path, dialTimeout)
	if err != nil {
		// The distinction that matters: this is not an engagement being
		// unknowable, it is the harness being absent. Say so, and say what to
		// do about it.
		return fmt.Errorf("%w at %s: start it, or set `socket` in the [harness.herdr] section: %w",
			ErrServerNotRunning, c.path, err)
	}
	defer func() { _ = conn.Close() }()

	if err := conn.SetDeadline(time.Now().Add(callTimeout)); err != nil {
		return err
	}

	id := fmt.Sprintf("director:%d", c.seq.Add(1))
	body, err := json.Marshal(request{ID: id, Method: method, Params: params})
	if err != nil {
		return err
	}
	if _, err := conn.Write(append(body, '\n')); err != nil {
		return fmt.Errorf("herdr %s: %w", method, err)
	}

	reader := bufio.NewReaderSize(conn, 1<<20)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("herdr %s: reading reply: %w", method, err)
	}

	var reply response
	if err := json.Unmarshal(line, &reply); err != nil {
		return fmt.Errorf("herdr %s: decoding reply: %w", method, err)
	}
	if reply.Error != nil {
		return fmt.Errorf("herdr %s: %w", method, reply.Error)
	}
	if result == nil || len(reply.Result) == 0 {
		return nil
	}
	return json.Unmarshal(reply.Result, result)
}

// notFound reports whether an error means herdr has no such pane or agent,
// which is an ordinary result rather than a failure — panes get closed.
func notFound(err error) bool {
	var replyErr *responseError
	if errors.As(err, &replyErr) {
		return replyErr.Code == "agent_not_found" || replyErr.Code == "pane_not_found"
	}
	return false
}

// stalled reports whether a prompt timed out waiting for a status change. That
// means the agent is still working and we stopped watching, not that anything
// went wrong.
func stalled(err error) bool {
	var replyErr *responseError
	if errors.As(err, &replyErr) {
		return replyErr.Code == "agent_prompt_stalled"
	}
	return false
}
