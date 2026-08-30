package claudecode

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/rwmyers/agent-director/harness"
)

// Read returns an engagement's output since a cursor.
//
// Claude Code's transcript is append-only JSONL, so the cursor is simply the
// line ordinal already consumed. That makes it stable, free to compute, and
// meaningful across processes — a director that restarts can still ask "what is
// new" and get an answer.
func (a *Adapter) Read(_ context.Context, req harness.ReadRequest) (harness.ReadResult, error) {
	path, _, ok := a.findTranscript(req.Ref)
	if !ok {
		return harness.ReadResult{}, fmt.Errorf("no transcript for %s; the conversation may not have started", req.Ref)
	}

	from := 0
	if req.Cursor != "" {
		parsed, err := strconv.Atoi(req.Cursor)
		if err != nil {
			return harness.ReadResult{}, fmt.Errorf("cursor %q is not a transcript position", req.Cursor)
		}
		from = parsed
	}

	turns, total, err := a.projectTranscript(path, from, req.Include)
	if err != nil {
		return harness.ReadResult{}, err
	}

	if req.Limit > 0 && len(turns) > req.Limit {
		// Keep the newest, because a director asking what happened wants the
		// end of the story, not the start of it.
		turns = turns[len(turns)-req.Limit:]
	}

	return harness.ReadResult{
		Kind:     harness.ReadTurns,
		Cursor:   strconv.Itoa(total),
		Total:    total,
		Complete: true,
		Turns:    turns,
	}, nil
}

// transcriptLine is the subset of a transcript record this adapter reads.
//
// The file carries a dozen record types — mode changes, permission-mode
// changes, AI titles, bridge-session bookkeeping, file-history snapshots — and
// most of them are not conversation. Decoding only what is needed means a new
// record type appearing upstream is ignored rather than misread.
type transcriptLine struct {
	Type        string          `json:"type"`
	Timestamp   string          `json:"timestamp"`
	IsSidechain bool            `json:"isSidechain"`
	IsMeta      bool            `json:"isMeta"`
	Message     json.RawMessage `json:"message"`
}

type transcriptMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type contentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// Turn roles this adapter can produce. They are the vocabulary a caller filters
// on, and are deliberately coarser than the transcript's own block types.
const (
	roleUser      = "user"
	roleAssistant = "assistant"
	roleToolUse   = "tool_use"
	roleThinking  = "thinking"
	roleSystem    = "system"
)

// projectTranscript flattens the transcript into turns.
//
// Returns the turns at or after `from`, and the total number of lines seen, so
// the caller can hand back a cursor that means "everything up to here". The
// count is of lines rather than of turns because a filtered read must not
// advance the cursor differently from an unfiltered one — otherwise changing
// --include between two reads would silently skip or repeat material.
func (a *Adapter) projectTranscript(path string, from int, include []string) ([]harness.Turn, int, error) {
	handle, err := os.Open(path) // #nosec G304 -- path located under our own home dir
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = handle.Close() }()

	wanted := map[string]bool{}
	for _, role := range include {
		wanted[role] = true
	}
	if len(wanted) == 0 {
		wanted[roleUser] = true
		wanted[roleAssistant] = true
	}

	var turns []harness.Turn
	scanner := bufio.NewScanner(handle)
	// Transcript lines carry whole tool results and can be very large; the
	// default 64KB limit would truncate one mid-record and desynchronise the
	// parse for everything after it.
	scanner.Buffer(make([]byte, 0, 256*1024), 8*1024*1024)

	position := 0
	for scanner.Scan() {
		position++
		raw := scanner.Bytes()
		if len(raw) == 0 {
			continue
		}

		var line transcriptLine
		if err := json.Unmarshal(raw, &line); err != nil {
			continue // a malformed line costs that line, not the read
		}
		if line.Type != roleUser && line.Type != roleAssistant {
			continue
		}
		// A sidechain is the agent's own subagent. A director must not see it:
		// it did not commission that work, and reading it would present
		// somebody else's reasoning as the engagement's own.
		if line.IsSidechain || line.IsMeta {
			continue
		}
		if position <= from {
			continue
		}

		at := parseTranscriptTime(line.Timestamp)
		for _, turn := range projectMessage(line, at, position) {
			if wanted[turn.Role] {
				turns = append(turns, turn)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, fmt.Errorf("reading %s: %w", path, err)
	}
	return turns, position, nil
}

// projectMessage turns one transcript record into zero or more turns.
func projectMessage(line transcriptLine, at time.Time, position int) []harness.Turn {
	if len(line.Message) == 0 {
		return nil
	}
	var message transcriptMessage
	if err := json.Unmarshal(line.Message, &message); err != nil {
		return nil
	}

	seq := strconv.Itoa(position)

	// A user message's content is sometimes a plain string — a typed prompt —
	// and sometimes a list of blocks, which is how tool results arrive.
	var text string
	if err := json.Unmarshal(message.Content, &text); err == nil {
		if text == "" {
			return nil
		}
		return []harness.Turn{{Seq: seq, Role: roleUser, At: at, Text: text}}
	}

	var blocks []contentBlock
	if err := json.Unmarshal(message.Content, &blocks); err != nil {
		return nil
	}

	var turns []harness.Turn
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if block.Text == "" {
				continue
			}
			role := roleAssistant
			if line.Type == roleUser {
				role = roleUser
			}
			turns = append(turns, harness.Turn{Seq: seq, Role: role, At: at, Text: block.Text})
		case "thinking":
			if block.Text != "" {
				turns = append(turns, harness.Turn{Seq: seq, Role: roleThinking, At: at, Text: block.Text})
			}
		case "tool_use":
			turns = append(turns, harness.Turn{
				Seq: seq, Role: roleToolUse, At: at,
				Text: block.Name + " " + truncate(string(block.Input), 200),
			})
		default:
			// tool_result and everything else is skipped. A tool result is
			// almost always larger than the reasoning it supports and is the
			// single biggest way a read blows a director's context.
		}
	}
	return turns
}

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}

// parseTranscriptTime reads a record's timestamp.
//
// A third of the lines in a real transcript carry no timestamp at all, so an
// absent or unparseable one is the zero time rather than an error.
func parseTranscriptTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

// Resume reopens a finished conversation.
//
// A fork gets a new session id, which the core then binds to a new engagement:
// branching produces a second conversation, and pretending otherwise would
// leave the director talking to whichever of the two it happened to resolve.
func (a *Adapter) Resume(ctx context.Context, req harness.ResumeRequest) (harness.SpawnResult, error) {
	ref := req.Ref
	args := []string{"--print", "--resume", req.Ref, "--output-format", "text"}

	if req.Fork {
		minted, err := newUUID()
		if err != nil {
			return harness.SpawnResult{}, err
		}
		ref = minted
		args = append(args, "--fork-session", "--session-id", ref)
	}

	spawn := harness.SpawnRequest{
		ID:     ref,
		Dir:    req.Dir,
		Prompt: req.Prompt,
		Env:    req.Env,
		// A resume inherits whatever the conversation already had; re-deriving
		// a tool allowlist here would silently widen or narrow it relative to
		// the original spawn.
	}
	return a.start(ctx, ref, args, spawn)
}

// ErrNoTranscript is returned when a conversation has left no record.
var ErrNoTranscript = errors.New("no transcript")
