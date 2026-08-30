package director

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rwmyers/agent-director/harness"
	"github.com/rwmyers/agent-director/internal/conf"
)

// Capability is one thing an agent may be permitted to do.
//
// It is defined in the harness package rather than here because adapters are
// what enforce it, and the type has to be reachable from the adapter contract.
// Aliased so that reading a workflow file's parser does not require thinking
// about which package a permission scope's words come from.
type Capability = harness.Capability

// The closed capability set, aliased from the harness contract.
const (
	CapRead    = harness.CapRead
	CapSearch  = harness.CapSearch
	CapEdit    = harness.CapEdit
	CapExecute = harness.CapExecute
	CapNetwork = harness.CapNetwork
	CapPush    = harness.CapPush
)

// AllCapabilities is the closed set, in a stable order for display.
var AllCapabilities = harness.AllCapabilities

// ParseCapability validates a capability name.
func ParseCapability(name string) (Capability, error) { return harness.ParseCapability(name) }

// Permission is a named set of allowed capabilities. Anything not allowed is
// denied, which is the only ordering that fails safe when a workflow author
// forgets to think about a capability.
type Permission struct {
	Name  string
	Allow []Capability
}

// Allows reports whether the scope permits a capability.
func (p Permission) Allows(capability Capability) bool {
	for _, allowed := range p.Allow {
		if allowed == capability {
			return true
		}
	}
	return false
}

// Denied returns every capability the scope withholds. This is what an adapter
// is actually asked to enforce, and what it must refuse the spawn over when it
// cannot.
func (p Permission) Denied() []Capability {
	var denied []Capability
	for _, capability := range AllCapabilities {
		if !p.Allows(capability) {
			denied = append(denied, capability)
		}
	}
	return denied
}

// ReportOn is a task type's communication contract: how often the agent is
// expected to speak. It is read by three parties, which is why it lives on the
// task type and not in global configuration.
//
// The agent reads it, rendered into prose in its system prompt at spawn — this
// is the only reason a spawned agent knows the callback API exists at all. The
// core reads it as the baseline for silence, since "has not reported" only
// means something relative to a declared expectation. The director reads it in
// status, so it knows what silence is supposed to look like before deciding
// whether to care.
type ReportOn struct {
	OnProgressChange bool
	Every            time.Duration // zero means no heartbeat is expected
	Never            bool
}

// String renders the contract for display and for the agent's prompt.
func (r ReportOn) String() string {
	if r.Never {
		return "never"
	}
	var parts []string
	if r.OnProgressChange {
		parts = append(parts, "progress-change")
	}
	if r.Every > 0 {
		parts = append(parts, r.Every.String())
	}
	if len(parts) == 0 {
		return "never"
	}
	return strings.Join(parts, ", ")
}

func parseReportOn(value string) (ReportOn, error) {
	items := conf.List(value)
	if len(items) == 0 {
		return ReportOn{OnProgressChange: true}, nil
	}

	var contract ReportOn
	for _, item := range items {
		switch item {
		case "never":
			contract.Never = true
		case "progress-change":
			contract.OnProgressChange = true
		default:
			every, err := time.ParseDuration(item)
			if err != nil {
				return ReportOn{}, fmt.Errorf("report_on: %q is neither \"progress-change\", \"never\", nor a duration like \"10m\"", item)
			}
			if every <= 0 {
				return ReportOn{}, fmt.Errorf("report_on: interval %q must be positive", item)
			}
			contract.Every = every
		}
	}
	if contract.Never && (contract.OnProgressChange || contract.Every > 0) {
		return ReportOn{}, fmt.Errorf(`report_on: "never" cannot be combined with other values`)
	}
	return contract, nil
}

// Task is one user-defined kind of work. Nothing about its meaning is known to
// the core: the core carries the prompt, enforces the permission scope,
// validates progress against the declared vocabulary, and measures silence
// against the contract. What the task *is* lives entirely in its prompt.
type Task struct {
	Name               string
	Description        string
	PromptPath         string
	Prompt             string
	Permission         string
	Progress           []string // the declared vocabulary, in order
	Terminal           string   // the progress value that means "done", if any
	ReportOn           ReportOn
	RequireFinalReport bool
	StallAfter         time.Duration // zero means derive from ReportOn
	Harness            string        // optional placement pin
}

// ValidProgress reports whether value is in the declared vocabulary.
func (t Task) ValidProgress(value string) bool {
	for _, declared := range t.Progress {
		if declared == value {
			return true
		}
	}
	return false
}

// StallThreshold is how long without any sign of life before an engagement is
// considered stalled rather than merely quiet.
//
// The default is three missed heartbeats, not one: a single missed report is
// routine — a long tool call, a slow build — and nudging on it would train the
// director to interrupt healthy agents. Three consecutive misses is a pattern.
// With no heartbeat declared there is nothing to count, so a flat fifteen
// minutes stands in.
func (t Task) StallThreshold() time.Duration {
	if t.StallAfter > 0 {
		return t.StallAfter
	}
	if t.ReportOn.Every > 0 {
		return 3 * t.ReportOn.Every
	}
	return 15 * time.Minute
}

// Workflow is a named bundle: the tasks available, the permission scopes they
// draw on, and the placement defaults.
//
// A workflow is data the director reads. It is deliberately not an engine: it
// declares no ordering, no transitions, and no graph, because the director is
// already an engine and encoding a flow here would be choosing one on the
// user's behalf. The point of the concept is that "whatever flow they'd like"
// stays achievable.
type Workflow struct {
	Name        string
	Description string
	Source      string // the file it was loaded from, for `director where`

	DirectorPromptPath string
	DirectorPrompt     string

	Harness     string // default placement for tasks that do not pin one
	Tasks       map[string]Task
	Permissions map[string]Permission
}

// TaskNames returns the task names in sorted order.
func (w *Workflow) TaskNames() []string {
	names := make([]string, 0, len(w.Tasks))
	for name := range w.Tasks {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Task looks up a task by name, listing the alternatives on failure — a
// director that guessed a task name should not have to run a second command to
// find out what it should have said.
func (w *Workflow) Task(name string) (Task, error) {
	task, ok := w.Tasks[name]
	if !ok {
		return Task{}, fmt.Errorf("workflow %q has no task %q (available: %s)",
			w.Name, name, strings.Join(w.TaskNames(), ", "))
	}
	return task, nil
}

// PermissionFor returns the scope a task runs under.
func (w *Workflow) PermissionFor(task Task) (Permission, error) {
	if task.Permission == "" {
		return Permission{}, fmt.Errorf("task %q declares no permissions", task.Name)
	}
	permission, ok := w.Permissions[task.Permission]
	if !ok {
		return Permission{}, fmt.Errorf("task %q wants permission scope %q, which this workflow does not define", task.Name, task.Permission)
	}
	return permission, nil
}

// HarnessFor returns the harness a task should be placed on: the task's pin,
// then the workflow default, then "" meaning the caller decides.
func (w *Workflow) HarnessFor(task Task) string {
	if task.Harness != "" {
		return task.Harness
	}
	return w.Harness
}

// LoadWorkflow parses a workflow file and resolves its prompt references.
//
// Prompt files are resolved and read at load time rather than at spawn on
// purpose: a workflow that references a missing prompt is broken, and the place
// to find that out is `director workflows`, not halfway through dispatching
// work to an agent.
func LoadWorkflow(path string) (*Workflow, error) {
	file, err := conf.ParseFile(path)
	if err != nil {
		return nil, err
	}

	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	workflow := &Workflow{
		Name:        name,
		Source:      path,
		Description: file.Global.Get("description"),
		Harness:     file.Global.Get("harness"),
		Tasks:       map[string]Task{},
		Permissions: map[string]Permission{},
	}

	dir := filepath.Dir(path)

	if ref := file.Global.Get("director_prompt"); ref != "" {
		resolved := resolveRelative(dir, ref)
		body, err := os.ReadFile(resolved) // #nosec G304 -- path comes from a config file the user owns
		if err != nil {
			return nil, fmt.Errorf("%s: director_prompt: %w", path, err)
		}
		workflow.DirectorPromptPath = resolved
		workflow.DirectorPrompt = string(body)
	}

	for _, section := range file.SectionsWithPrefix("permission.") {
		scopeName := strings.TrimPrefix(section.Name, "permission.")
		permission := Permission{Name: scopeName}
		for _, raw := range conf.List(section.Entries.Get("allow")) {
			capability, err := ParseCapability(raw)
			if err != nil {
				return nil, fmt.Errorf("%s: [%s]: %w", path, section.Name, err)
			}
			permission.Allow = append(permission.Allow, capability)
		}
		workflow.Permissions[scopeName] = permission
	}

	for _, section := range file.SectionsWithPrefix("task.") {
		task, err := loadTask(path, dir, section)
		if err != nil {
			return nil, err
		}
		workflow.Tasks[task.Name] = task
	}

	if err := workflow.validate(path); err != nil {
		return nil, err
	}
	return workflow, nil
}

func loadTask(path, dir string, section conf.Section) (Task, error) {
	name := strings.TrimPrefix(section.Name, "task.")
	entries := section.Entries

	task := Task{
		Name:               name,
		Description:        entries.Get("description"),
		Permission:         entries.Get("permissions"),
		Progress:           conf.List(entries.Get("progress")),
		Terminal:           entries.Get("terminal"),
		RequireFinalReport: entries.Get("require_final_report") == "true",
		Harness:            entries.Get("harness"),
	}

	contract, err := parseReportOn(entries.Get("report_on"))
	if err != nil {
		return Task{}, fmt.Errorf("%s: [%s]: %w", path, section.Name, err)
	}
	task.ReportOn = contract

	if raw := entries.Get("stall_after"); raw != "" {
		stall, err := time.ParseDuration(raw)
		if err != nil {
			return Task{}, fmt.Errorf("%s: [%s]: stall_after: %w", path, section.Name, err)
		}
		task.StallAfter = stall
	}

	if ref := entries.Get("prompt"); ref != "" {
		resolved := resolveRelative(dir, ref)
		body, err := os.ReadFile(resolved) // #nosec G304 -- path comes from a config file the user owns
		if err != nil {
			return Task{}, fmt.Errorf("%s: [%s]: prompt: %w", path, section.Name, err)
		}
		task.PromptPath = resolved
		task.Prompt = string(body)
	}

	return task, nil
}

func (w *Workflow) validate(path string) error {
	if len(w.Tasks) == 0 {
		return fmt.Errorf("%s: workflow defines no tasks", path)
	}
	for _, name := range w.TaskNames() {
		task := w.Tasks[name]
		if _, err := w.PermissionFor(task); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if task.Terminal != "" && !task.ValidProgress(task.Terminal) {
			return fmt.Errorf("%s: task %q has terminal %q, which is not in its progress vocabulary (%s)",
				path, name, task.Terminal, strings.Join(task.Progress, ", "))
		}
		if task.RequireFinalReport && task.Terminal == "" {
			return fmt.Errorf("%s: task %q sets require_final_report but declares no terminal progress value, so nothing could ever satisfy it",
				path, name)
		}
	}
	return nil
}

func resolveRelative(dir, ref string) string {
	if filepath.IsAbs(ref) {
		return ref
	}
	return filepath.Clean(filepath.Join(dir, ref))
}
