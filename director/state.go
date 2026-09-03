package director

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rwmyers/agent-director/harness"
	"github.com/rwmyers/agent-director/internal/conf"
)

// Engagement is one agent conversation a director has a name for.
//
// Its identity is two fields. ID is minted here, before any process exists, and
// never changes; Ref is the harness's own handle and is opaque to everything
// outside an adapter. Nothing user-facing prints Ref.
type Engagement struct {
	ID       string `json:"id"`
	Director string `json:"director"`
	Harness  string `json:"harness"`
	Ref      string `json:"ref"`
	Task     string `json:"task"`
	Title    string `json:"title"`
	// Name is what the harness was asked to display for this conversation.
	// Stored so it is possible to tell what a person sees in the harness
	// without asking the harness.
	Name string `json:"name,omitempty"`
	Dir  string `json:"dir"`

	// Lifecycle and Health are re-derived on every read and never stored.
	Lifecycle harness.Lifecycle `json:"lifecycle"`
	Health    Health            `json:"health"`
	// Progress is stored, because the agent is its only source and there is no
	// way to re-derive it from anything the harness can see.
	Progress string `json:"progress,omitempty"`

	StartedAt      time.Time `json:"started_at"`
	LastReportAt   time.Time `json:"last_report_at,omitempty"`
	LastActivityAt time.Time `json:"last_activity_at,omitempty"`
	NudgedAt       time.Time `json:"nudged_at,omitempty"`

	Note   string `json:"note,omitempty"`
	Cursor string `json:"cursor,omitempty"`

	// Materials is where this engagement's work can be found — a branch, a
	// pull request, a path, a document. The engagement is its only source:
	// it knows where its work landed, and a director reading it out of prose
	// is guessing. Replaced wholesale by `director report --materials`, so it
	// always names the current set rather than everything ever mentioned.
	Materials []string `json:"materials,omitempty"`

	// Token is the engagement's callback secret. It is never rendered: JSON
	// output of a fleet would otherwise hand every agent's credentials to
	// whatever is reading, including another agent.
	Token string `json:"-"`

	Detail map[string]string `json:"detail,omitempty"`

	// PendingAsk is the oldest question this engagement is waiting on. It is
	// what health reads to decide "blocked", and it is deliberately not
	// rendered: a question is as long as the agent made it, `status` is the
	// command a director is told to run every turn, and an unanswered question
	// reproduces its whole text on every one of those turns until somebody
	// answers. OpenAsks carries the identifiers instead; `director status
	// asks` fetches the text, once, when it is actually wanted.
	PendingAsk *Ask `json:"-"`
	// OpenAsks lists the identifiers of this engagement's unanswered
	// questions, oldest first.
	OpenAsks []string `json:"open_asks,omitempty"`

	// LastMessage is whatever the agent last said alongside a report, kept so
	// `status` can show something human without a transcript read.
	LastMessage string `json:"last_message,omitempty"`
}

// SilentFor is how long since the agent last reported, measured from spawn when
// it has never reported at all.
func (e *Engagement) SilentFor(now time.Time) time.Duration {
	since := e.LastReportAt
	if since.IsZero() {
		since = e.StartedAt
	}
	if since.IsZero() {
		return 0
	}
	return now.Sub(since)
}

// Ask is a question an agent raised and is waiting on.
//
// It is what makes "blocked" honest. No harness reliably reports that an agent
// is waiting on a human — Claude Code's liveness file has no such field — so
// before this existed the state was either guessed or missed. An agent that
// asks is unambiguously blocked, on every harness, with the question text
// available to whoever has to answer it.
type Ask struct {
	ID         string    `json:"id"`
	Engagement string    `json:"engagement"`
	Question   string    `json:"question"`
	AskedAt    time.Time `json:"asked_at"`
	Answer     string    `json:"answer,omitempty"`
	AnsweredAt time.Time `json:"answered_at,omitempty"`
}

// Answered reports whether the question has been dealt with.
func (a *Ask) Answered() bool { return !a.AnsweredAt.IsZero() }

// State is one director's persisted memory: the bindings, the cursors, the
// notes, the reported progress, and the outstanding questions.
//
// What it deliberately does not hold is anything the harness is authoritative
// for. Lifecycle, liveness and activity are re-derived on every read, because a
// cached lifecycle is wrong the moment a process exits and a director acting on
// a stale "working" waits forever.
type State struct {
	Path       string
	DirectorID string
	Name       string
	Workflow   string
	CreatedAt  time.Time
	// AttachedAt is when a conversation last claimed this director. It is how
	// a second conversation can tell somebody is already driving this fleet.
	AttachedAt time.Time
	// Host is where the conversation driving this director is running. It is
	// written by attach and re-detected every time, because it describes the
	// conversation that is here now rather than the state of the fleet.
	Host        Host
	Engagements map[string]*Engagement
	Asks        map[string]*Ask
}

// EngagementIDs returns the engagement identifiers in a stable order.
func (s *State) EngagementIDs() []string {
	ids := make([]string, 0, len(s.Engagements))
	for id := range s.Engagements {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// PendingAskFor returns the oldest unanswered question for an engagement.
func (s *State) PendingAskFor(engagementID string) *Ask {
	var oldest *Ask
	for _, ask := range s.Asks {
		if ask.Engagement != engagementID || ask.Answered() {
			continue
		}
		if oldest == nil || ask.AskedAt.Before(oldest.AskedAt) {
			oldest = ask
		}
	}
	return oldest
}

// OpenAsks returns every unanswered question, oldest first.
func (s *State) OpenAsks() []*Ask {
	open := make([]*Ask, 0, len(s.Asks))
	for _, ask := range s.Asks {
		if !ask.Answered() {
			open = append(open, ask)
		}
	}
	sortAsks(open)
	return open
}

// OpenAskIDsFor returns the identifiers of one engagement's unanswered
// questions, oldest first.
func (s *State) OpenAskIDsFor(engagementID string) []string {
	var open []*Ask
	for _, ask := range s.Asks {
		if ask.Engagement == engagementID && !ask.Answered() {
			open = append(open, ask)
		}
	}
	sortAsks(open)
	ids := make([]string, 0, len(open))
	for _, ask := range open {
		ids = append(ids, ask.ID)
	}
	return ids
}

// sortAsks puts questions in the order they were asked, breaking ties on the
// identifier so that two questions asked in the same clock tick still come back
// in a stable order rather than whatever the map iterated.
func sortAsks(asks []*Ask) {
	sort.Slice(asks, func(i, j int) bool {
		if !asks[i].AskedAt.Equal(asks[j].AskedAt) {
			return asks[i].AskedAt.Before(asks[j].AskedAt)
		}
		return asks[i].ID < asks[j].ID
	})
}

// StateDir is where a root keeps its per-director state files.
func StateDir(root string) string { return filepath.Join(root, "state") }

func statePath(root, directorID string) string {
	return filepath.Join(StateDir(root), directorID+".state")
}

// ListDirectors returns every director registered in a root, sorted by name.
//
// State is one file per director rather than one shared file so that two
// directors on the same machine contend for nothing: a write by one never
// blocks a read by the other, and a corrupt file costs one director's fleet
// rather than all of them.
//
// That last promise only holds if one unreadable file does not fail the whole
// listing, so it does not: an unreadable state file costs its own director and
// is reported alongside the ones that loaded, exactly as a broken workflow
// costs that workflow. Refusing to list anything is how a single bad line takes
// out every director on the machine, including the ones that are perfectly
// fine.
func ListDirectors(root string) ([]*State, error) {
	entries, err := os.ReadDir(StateDir(root))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var states []*State
	var problems []error
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".state") {
			continue
		}
		state, err := LoadState(filepath.Join(StateDir(root), entry.Name()))
		if err != nil {
			problems = append(problems, err)
			continue
		}
		states = append(states, state)
	}
	sort.Slice(states, func(i, j int) bool {
		if states[i].Name != states[j].Name {
			return states[i].Name < states[j].Name
		}
		return states[i].DirectorID < states[j].DirectorID
	})
	return states, errors.Join(problems...)
}

// RepairStates joins values that were written across several lines back
// together in every state file under a root.
//
// It is the way back from a state file nothing can read, without a text editor
// and without losing what is in it. With apply false it says what it would do
// and changes nothing. A file that already reads is never rewritten.
//
// It takes each file's write lock, because a director whose state is broken may
// still have agents reporting into it, and two writers racing on the same file
// is how a repair turns into a second corruption.
func RepairStates(root string, apply bool) ([]*conf.RepairReport, error) {
	entries, err := os.ReadDir(StateDir(root))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var reports []*conf.RepairReport
	var problems []error
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".state") {
			continue
		}
		path := filepath.Join(StateDir(root), entry.Name())

		var report *conf.RepairReport
		err := withStateLock(path, func() error {
			var err error
			report, err = conf.RepairFile(path, apply)
			return err
		})
		if err != nil {
			problems = append(problems, err)
			continue
		}
		reports = append(reports, report)
	}
	sort.Slice(reports, func(i, j int) bool { return reports[i].Path < reports[j].Path })
	return reports, errors.Join(problems...)
}

// LoadState reads a director's state file.
func LoadState(path string) (*State, error) {
	file, err := conf.ParseFile(path)
	if err != nil {
		return nil, err
	}

	state := &State{
		Path:        path,
		DirectorID:  file.Global.Get("director"),
		Name:        file.Global.Get("name"),
		Workflow:    file.Global.Get("workflow"),
		CreatedAt:   parseTime(file.Global.Get("created_at")),
		AttachedAt:  parseTime(file.Global.Get("attached_at")),
		Engagements: map[string]*Engagement{},
		Asks:        map[string]*Ask{},
	}
	state.Host = Host{
		Harness: file.Global.Get("host_harness"),
		Ref:     file.Global.Get("host_ref"),
		Hosting: harness.Hosting{
			// Only the literal "true" is a claim. Anything else — absent,
			// misspelt, a state file written by an older director — leaves the
			// conservative answer in place rather than granting a capability
			// nothing verified.
			Background: file.Global.Get("host_background") == "true",
			Wake:       file.Global.Get("host_wake") == "true",
		},
		Source:      HostSource(file.Global.Get("host_source")),
		LastWokenAt: parseTime(file.Global.Get("host_last_woken_at")),
	}
	if state.Host.Source == "" {
		state.Host.Source = HostUnknown
	}

	for _, section := range file.SectionsWithPrefix("engagement.") {
		engagement := &Engagement{
			ID:           strings.TrimPrefix(section.Name, "engagement."),
			Director:     state.DirectorID,
			Harness:      section.Entries.Get("harness"),
			Ref:          section.Entries.Get("ref"),
			Task:         section.Entries.Get("task"),
			Title:        section.Entries.Get("title"),
			Name:         section.Entries.Get("name"),
			Dir:          section.Entries.Get("dir"),
			Progress:     section.Entries.Get("progress"),
			StartedAt:    parseTime(section.Entries.Get("started_at")),
			LastReportAt: parseTime(section.Entries.Get("last_report_at")),
			NudgedAt:     parseTime(section.Entries.Get("nudged_at")),
			Note:         section.Entries.Get("note"),
			Cursor:       section.Entries.Get("cursor"),
			Token:        section.Entries.Get("token"),
			LastMessage:  section.Entries.Get("last_message"),
			Detail:       map[string]string{},
		}
		for _, entry := range section.Entries {
			if key, ok := strings.CutPrefix(entry.Key, "detail."); ok {
				engagement.Detail[key] = entry.Value
			}
		}
		// A repeated key is how this format stores a list, so the order the
		// engagement reported its materials in is the order they come back.
		engagement.Materials = section.Entries.All("material")
		state.Engagements[engagement.ID] = engagement
	}

	for _, section := range file.SectionsWithPrefix("ask.") {
		ask := &Ask{
			ID:         strings.TrimPrefix(section.Name, "ask."),
			Engagement: section.Entries.Get("engagement"),
			Question:   section.Entries.Get("question"),
			AskedAt:    parseTime(section.Entries.Get("asked_at")),
			Answer:     section.Entries.Get("answer"),
			AnsweredAt: parseTime(section.Entries.Get("answered_at")),
		}
		state.Asks[ask.ID] = ask
	}

	return state, nil
}

// Save writes the state file atomically.
func (s *State) Save() error {
	file := &conf.File{}
	file.Global.Set("version", "1")
	file.Global.Set("director", s.DirectorID)
	file.Global.Set("name", s.Name)
	file.Global.Set("workflow", s.Workflow)
	file.Global.Set("created_at", formatTime(s.CreatedAt))
	if !s.AttachedAt.IsZero() {
		file.Global.Set("attached_at", formatTime(s.AttachedAt))
	}
	if s.Host.Known() {
		file.Global.Set("host_harness", s.Host.Harness)
		if s.Host.Ref != "" {
			file.Global.Set("host_ref", s.Host.Ref)
		}
		file.Global.Set("host_background", formatBool(s.Host.Hosting.Background))
		file.Global.Set("host_wake", formatBool(s.Host.Hosting.Wake))
		file.Global.Set("host_source", string(s.Host.Source))
		if !s.Host.LastWokenAt.IsZero() {
			file.Global.Set("host_last_woken_at", formatTime(s.Host.LastWokenAt))
		}
	}

	for _, id := range s.EngagementIDs() {
		engagement := s.Engagements[id]
		var entries conf.Entries
		set := func(key, value string) {
			if value != "" {
				entries.Set(key, value)
			}
		}
		set("harness", engagement.Harness)
		set("ref", engagement.Ref)
		set("task", engagement.Task)
		set("title", engagement.Title)
		set("name", engagement.Name)
		set("dir", engagement.Dir)
		set("progress", engagement.Progress)
		set("started_at", formatTime(engagement.StartedAt))
		set("last_report_at", formatTime(engagement.LastReportAt))
		set("nudged_at", formatTime(engagement.NudgedAt))
		set("note", engagement.Note)
		set("cursor", engagement.Cursor)
		set("token", engagement.Token)
		set("last_message", engagement.LastMessage)
		for _, material := range engagement.Materials {
			entries = append(entries, conf.Entry{Key: "material", Value: material})
		}

		detailKeys := make([]string, 0, len(engagement.Detail))
		for key := range engagement.Detail {
			detailKeys = append(detailKeys, key)
		}
		sort.Strings(detailKeys)
		for _, key := range detailKeys {
			set("detail."+key, engagement.Detail[key])
		}

		file.SetSection("engagement."+id, entries)
	}

	askIDs := make([]string, 0, len(s.Asks))
	for id := range s.Asks {
		askIDs = append(askIDs, id)
	}
	sort.Strings(askIDs)
	for _, id := range askIDs {
		ask := s.Asks[id]
		var entries conf.Entries
		entries.Set("engagement", ask.Engagement)
		entries.Set("question", ask.Question)
		entries.Set("asked_at", formatTime(ask.AskedAt))
		if ask.Answered() {
			entries.Set("answer", ask.Answer)
			entries.Set("answered_at", formatTime(ask.AnsweredAt))
		}
		file.SetSection("ask."+id, entries)
	}

	return conf.WriteFile(s.Path, file)
}

// lockStale is how long a lock file may exist before it is assumed to belong to
// a process that died. A director command is short; a minute is generous.
const lockStale = time.Minute

// withStateLock serialises a read-modify-write of one director's state.
//
// Concurrency here is the normal case rather than an edge case: every
// invocation is a separate process, and an agent's `report` can land while the
// director is running `status` and a monitoring `watch` is tailing. The lock
// covers writers only. Readers take nothing, because the write is a rename and
// a rename is atomic — a reader sees the old file or the new one, never a torn
// one.
func withStateLock(path string, fn func() error) error {
	lockPath := path + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return err
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		handle, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_ = handle.Close()
			break
		}
		if !errors.Is(err, os.ErrExist) {
			return err
		}

		// Somebody holds it. If the lock is older than any plausible command,
		// the holder is gone and leaving the file there would wedge every
		// future write.
		if info, statErr := os.Stat(lockPath); statErr == nil && time.Since(info.ModTime()) > lockStale {
			_ = os.Remove(lockPath)
			continue
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %s; remove it if no director is running", lockPath)
		}
		time.Sleep(20 * time.Millisecond)
	}
	defer func() { _ = os.Remove(lockPath) }()

	return fn()
}
