package director

import (
	"context"
	"fmt"
	"strings"

	"github.com/rwmyers/agent-director/harness"
)

// Read budget defaults.
//
// The budget lives in the core rather than in each adapter so that a TUI or a
// monitoring consumer gets the same protection without reimplementing it, and
// so that changing it is one edit rather than one per harness.
const (
	DefaultReadLimit    = 20
	DefaultReadMaxChars = 4000
)

// DefaultReadRoles is what a read returns when the caller does not say.
//
// Assistant turns only, because the other roles are things the director already
// knows or does not want. The user turns are its own brief and its own
// follow-ups, echoed back — and a composed brief is long enough to consume most
// of the budget before the agent has said a word. Tool calls and thinking are
// larger still and are worth their cost only when diagnosing what an agent
// actually ran, never for finding out what it concluded.
var DefaultReadRoles = []string{"assistant"}

// ReadOptions asks for an engagement's output.
type ReadOptions struct {
	// SinceLastRead uses the cursor persisted for this director, so the normal
	// read is a delta rather than the whole conversation.
	SinceLastRead bool
	Cursor        string
	Limit         int
	MaxChars      int
	// Include selects turn roles. Empty means user and assistant only.
	Include []string
}

// ReadResult is an engagement's output, budgeted.
type ReadResult struct {
	Kind harness.ReadKind `json:"kind"`
	// Cursor is where to resume from. Persisted for this director, so "what is
	// new" survives the director's own context being compacted or its process
	// restarting.
	Cursor string `json:"cursor"`
	Total  int    `json:"total"`
	// Complete is false when the record itself is lossy — a screen snapshot has
	// finite scrollback, so an absence of output is not evidence of silence.
	Complete bool `json:"complete"`
	// Omitted counts turns dropped to fit the budget, so a director is told
	// what it did not see rather than left to assume it saw everything.
	Omitted int            `json:"omitted"`
	Turns   []harness.Turn `json:"turns"`
}

// Read returns what an engagement has produced.
//
// A harness that cannot read errors rather than returning nothing. An empty
// result would read as "the agent said nothing", which is a different and much
// more dangerous claim than "I cannot see what it said" — the first invites a
// director to conclude the work failed.
func (d *Director) Read(ctx context.Context, id string, opts ReadOptions) (ReadResult, error) {
	engagement, err := d.Get(ctx, id)
	if err != nil {
		return ReadResult{}, err
	}
	adapter, err := d.lookupFor(engagement)
	if err != nil {
		return ReadResult{}, err
	}
	reader, ok := adapter.(harness.Reader)
	if !ok {
		return ReadResult{}, fmt.Errorf("harness %q cannot return an engagement's output", engagement.Harness)
	}

	cursor := opts.Cursor
	if opts.SinceLastRead && cursor == "" {
		cursor = engagement.Cursor
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultReadLimit
	}

	include := opts.Include
	if len(include) == 0 {
		include = DefaultReadRoles
	}

	raw, err := reader.Read(ctx, harness.ReadRequest{
		Ref:     engagement.Ref,
		Cursor:  cursor,
		Limit:   limit,
		Include: include,
	})
	if err != nil {
		return ReadResult{}, err
	}

	maxChars := opts.MaxChars
	if maxChars <= 0 {
		maxChars = DefaultReadMaxChars
	}
	turns, omitted := budget(raw.Turns, maxChars)

	// Advance the cursor only for this director. Two directors reading one
	// engagement must not consume each other's position, or the second reads
	// nothing at all and has no way to notice.
	if raw.Cursor != "" && raw.Cursor != engagement.Cursor {
		if err := d.mutate(func(state *State) error {
			if held, ok := state.Engagements[id]; ok {
				held.Cursor = raw.Cursor
			}
			return nil
		}); err != nil {
			return ReadResult{}, err
		}
	}

	return ReadResult{
		Kind:     raw.Kind,
		Cursor:   raw.Cursor,
		Total:    raw.Total,
		Complete: raw.Complete,
		Omitted:  omitted,
		Turns:    turns,
	}, nil
}

// budget trims turns to fit a character allowance, dropping the oldest first.
//
// Oldest-first because a director asking what happened wants the end of the
// story. Dropping the newest would hand back the opening of a conversation and
// silently withhold its conclusion, which is the part that changes what the
// director does next.
func budget(turns []harness.Turn, maxChars int) ([]harness.Turn, int) {
	total := 0
	for _, turn := range turns {
		total += len(turn.Text)
	}
	if total <= maxChars {
		return turns, 0
	}

	kept := make([]harness.Turn, 0, len(turns))
	used := 0
	for i := len(turns) - 1; i >= 0; i-- {
		used += len(turns[i].Text)
		if used > maxChars && len(kept) > 0 {
			break
		}
		kept = append(kept, turns[i])
	}
	// Reverse back into conversation order.
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}
	return kept, len(turns) - len(kept)
}

// ResumeOptions reopens a finished engagement.
type ResumeOptions struct {
	Prompt string
	// Fork branches a new engagement from this one's context, leaving the
	// original untouched.
	Fork bool
}

// Resume reopens a finished conversation.
//
// A harness that cannot resume errors rather than falling back to a fresh
// spawn. Spawning would create a second conversation with the same title and
// the same brief, and the director would then be talking to whichever one it
// happened to resolve — with nothing in the output to show that had happened.
func (d *Director) Resume(ctx context.Context, id string, opts ResumeOptions) (*Engagement, error) {
	engagement, err := d.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	adapter, err := d.lookupFor(engagement)
	if err != nil {
		return nil, err
	}
	resumer, ok := adapter.(harness.Resumer)
	if !ok {
		return nil, fmt.Errorf("harness %q cannot resume a finished conversation", engagement.Harness)
	}
	task, err := d.Workflow.Task(engagement.Task)
	if err != nil {
		return nil, err
	}

	prompt := opts.Prompt
	if strings.TrimSpace(prompt) == "" {
		prompt = "Pick this engagement back up. Report your progress when you have re-oriented."
	}

	if !opts.Fork {
		result, err := resumer.Resume(ctx, harness.ResumeRequest{
			Ref: engagement.Ref, Dir: engagement.Dir, Prompt: prompt,
			Env: d.agentEnv(engagement, task),
		})
		if err != nil {
			return nil, err
		}
		if err := d.mutate(func(state *State) error {
			if held, ok := state.Engagements[id]; ok {
				held.Ref = result.Ref
				for key, value := range result.Detail {
					held.Detail[key] = value
				}
			}
			return nil
		}); err != nil {
			return nil, err
		}
		return d.Get(ctx, id)
	}

	// A fork is a new engagement, not a mutation of an old one. It gets its own
	// identity, its own token and its own cursor, and records where it came
	// from — otherwise two live conversations would share one record and the
	// director could not address them separately.
	newID, err := newID(engagementPrefix)
	if err != nil {
		return nil, err
	}
	token, err := newToken()
	if err != nil {
		return nil, err
	}
	forked := &Engagement{
		ID:        newID,
		Director:  d.State.DirectorID,
		Harness:   engagement.Harness,
		Task:      engagement.Task,
		Title:     engagement.Title,
		Name:      engagement.Name,
		Dir:       engagement.Dir,
		StartedAt: d.now(),
		Token:     token,
		Detail:    map[string]string{"forked_from": engagement.ID},
	}
	if err := d.mutate(func(state *State) error {
		state.Engagements[newID] = forked
		return nil
	}); err != nil {
		return nil, err
	}

	result, err := resumer.Resume(ctx, harness.ResumeRequest{
		Ref: engagement.Ref, Dir: engagement.Dir, Prompt: prompt, Fork: true,
		Env: d.agentEnv(forked, task),
	})
	if err != nil {
		return nil, fmt.Errorf("forking %s: %w", engagement.ID, err)
	}
	if err := d.mutate(func(state *State) error {
		if held, ok := state.Engagements[newID]; ok {
			held.Ref = result.Ref
			for key, value := range result.Detail {
				held.Detail[key] = value
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return d.Get(ctx, newID)
}
