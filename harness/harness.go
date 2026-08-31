// Package harness is the contract between agent-director and the agent tools it
// drives, and the registry every adapter lands in.
//
// An adapter teaches director how to start a conversation in one harness, tell
// whether it is still alive, push text into it, and stop it. Adapters are
// deliberately thin: they translate, and they do not decide. Nothing here
// returns "this work is finished" or "this may be removed", because those are
// judgements the core and the director make from several signals, and an
// adapter that could assert them would be able to mislead the whole system by
// returning one wrong string.
//
// # Two identities
//
// Every engagement has an ID minted by director before any process exists, and
// a Ref which is the harness's own handle for the same thing. They are separate
// because harnesses disagree about who gets to name things: Claude Code accepts
// a caller-assigned session id up front, while herdr assigns a pane id only
// after a pane exists. Collapsing them would force one harness's model onto the
// other, and would stop director recording a binding before it spawns — which
// is what makes a crash mid-spawn recoverable instead of leaving an untracked
// agent process nobody's name is on.
//
// # Built-ins are not privileged
//
// The adapters shipped in this repository register through the same Register
// call as anything else, and are resolved the same way. If a first-party
// adapter had access the public interface did not expose, the interface would
// quietly rot until a third party tried to use it.
package harness

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Capability is one thing an agent may be permitted to do.
//
// The set is closed. Task types are entirely user-defined, but capabilities
// cannot be, because an adapter has to enforce them and cannot map a word it
// has never heard of. A permission system that silently ignores an unknown
// capability is worse than none, because it gets believed. Users compose scopes
// out of these; they do not invent new ones.
type Capability string

const (
	CapRead    Capability = "read"    // read files in the working directory
	CapSearch  Capability = "search"  // search the filesystem and the web
	CapEdit    Capability = "edit"    // modify files
	CapExecute Capability = "execute" // run commands
	CapNetwork Capability = "network" // reach the network directly
	CapPush    Capability = "push"    // publish work outside this machine
)

// AllCapabilities is the closed set, in a stable order for display.
var AllCapabilities = []Capability{CapRead, CapSearch, CapEdit, CapExecute, CapNetwork, CapPush}

// ParseCapability validates a capability name, naming the valid set on failure
// so a typo in a workflow file is fixable without opening the source.
func ParseCapability(name string) (Capability, error) {
	for _, capability := range AllCapabilities {
		if string(capability) == name {
			return capability, nil
		}
	}
	return "", fmt.Errorf("unknown capability %q (valid: %s)", name, JoinCapabilities(AllCapabilities))
}

// JoinCapabilities renders a capability list for a message.
func JoinCapabilities(caps []Capability) string {
	parts := make([]string, len(caps))
	for i, capability := range caps {
		parts[i] = string(capability)
	}
	return strings.Join(parts, ", ")
}

// Lifecycle is whether a process is attached to a conversation and what it is
// doing. It is the harness's answer and nothing else's, re-derived on every
// call — a cached lifecycle is a lie the moment a process exits, and a director
// acting on a stale "working" waits forever.
//
// The set is closed. An adapter that sees a value it does not recognise reports
// LifecycleUnknown rather than guessing, because every value here feeds a
// decision about whether to interrupt a human.
type Lifecycle string

const (
	// LifecycleStarting means the engagement has been asked for but cannot yet
	// accept input. Both harnesses have a real window here — herdr's pane is
	// not yet at an agent prompt, Claude Code has not yet written a transcript
	// — and without this value the director would send a prompt into a shell.
	LifecycleStarting Lifecycle = "starting"
	// LifecycleWorking means a turn is in flight.
	LifecycleWorking Lifecycle = "working"
	// LifecycleIdle means alive, reachable, nothing in flight.
	LifecycleIdle Lifecycle = "idle"
	// LifecycleBlocked means stopped, waiting on a human.
	LifecycleBlocked Lifecycle = "blocked"
	// LifecycleDone means no process is attached.
	//
	// It does NOT mean the work finished. Claude Code reports the underlying
	// condition when a terminal is closed, when a session is suspended for
	// resume, and on logout. Whether anything was accomplished is a question
	// for the progress axis, never for this one.
	LifecycleDone Lifecycle = "done"
	// LifecycleUnknown means the harness answered and does not know. It is not
	// the value for "the harness did not answer" — that is an error, because a
	// stopped server must not look like a fleet of confused agents.
	LifecycleUnknown Lifecycle = "unknown"
)

// Normalize maps a reported lifecycle onto the closed set, turning anything
// unrecognised into LifecycleUnknown so an unexpected value lands somewhere
// harmless rather than somewhere convenient.
func (l Lifecycle) Normalize() Lifecycle {
	switch l {
	case LifecycleStarting, LifecycleWorking, LifecycleIdle, LifecycleBlocked, LifecycleDone:
		return l
	default:
		return LifecycleUnknown
	}
}

// Live reports whether a process is attached.
func (l Lifecycle) Live() bool {
	return l == LifecycleStarting || l == LifecycleWorking || l == LifecycleIdle || l == LifecycleBlocked
}

// Observation is what an adapter can see about one engagement right now.
//
// It is deliberately narrow. Everything director knows that the harness does
// not — the task, the brief, progress, notes, cursors, pending questions —
// lives in director's own state, and an adapter is never asked about it.
type Observation struct {
	// Ref is the harness's handle. Set by List; echoed by Get.
	Ref string
	// Found is false when the harness has no record of the ref at all. That is
	// an ordinary result, not an error: panes get closed and transcripts get
	// deleted, and the core must be able to tell that from a broken adapter.
	Found bool

	Lifecycle Lifecycle

	// LastActivityAt is when the harness last saw the agent actually do
	// something — a transcript grew, a pane emitted output.
	//
	// This is the signal that makes a faulty agent detectable, because it does
	// not depend on the agent cooperating: an agent that has stopped reporting
	// but is still working looks different from one that is wedged. It is read
	// on every status call, so an adapter must be able to produce it cheaply —
	// one stat, or a field it already fetched. An adapter that genuinely cannot
	// supply it leaves it zero, and health degrades to unknown rather than
	// guessing.
	LastActivityAt time.Time

	// Detail is whatever the adapter wants to say about itself. The core passes
	// it through to display and to --json, and never branches on a key.
	Detail map[string]string
}

// SpawnRequest asks an adapter to start a conversation.
type SpawnRequest struct {
	// ID is minted by director before this call. An adapter that can accept a
	// caller-assigned identity should use it as the Ref.
	ID    string
	Dir   string
	Title string
	// Name is how the harness should label this conversation in its own
	// interface — Claude Code's prompt box and /resume picker, a herdr pane
	// header, a terminal title.
	//
	// It is not Title. Title is director's description of the work and is shown
	// by director; Name is what a person sees when they look at the harness
	// directly, which is where several agents working in one repository are
	// otherwise indistinguishable — harnesses tend to derive a label from the
	// working directory, so siblings all end up with the same one. An adapter
	// whose harness has no notion of a display name ignores this.
	Name string
	// Prompt is fully composed by the core: the task's prompt, the reporting
	// contract rendered as prose, and the director's brief. An adapter must
	// deliver it verbatim and must not edit or summarise it.
	Prompt string
	// Allow is the permitted capability set. Everything absent is denied, and
	// an adapter that cannot enforce a denial must fail this call rather than
	// run the agent unrestricted.
	Allow []Capability
	// Env is injected into the agent's process so it can call back. Without it
	// the agent has no way to report progress or ask a question.
	Env map[string]string
	// Options is a per-spawn escape hatch mirroring Observation.Detail.
	Options map[string]string
}

// Allows reports whether a capability is permitted by this request.
func (s SpawnRequest) Allows(capability Capability) bool {
	for _, allowed := range s.Allow {
		if allowed == capability {
			return true
		}
	}
	return false
}

// SpawnResult is what an adapter reports back about the conversation it started.
type SpawnResult struct {
	Ref    string
	Detail map[string]string
}

// SendRequest pushes text into a running conversation.
type SendRequest struct {
	Ref  string
	Text string
}

// StopMode selects how an engagement is stopped. Both modes leave the
// conversation resumable; neither destroys a transcript.
type StopMode string

const (
	// StopEnd terminates the process.
	StopEnd StopMode = "end"
	// StopInterrupt stops the current turn only.
	StopInterrupt StopMode = "interrupt"
)

// StopRequest asks an adapter to stop an engagement.
type StopRequest struct {
	Ref  string
	Mode StopMode
}

// Filter narrows a List.
type Filter struct {
	Dir string
}

// Adapter is the contract. Every method here is mandatory; anything optional is
// a separate interface a caller type-asserts for, so that an adapter which
// cannot do something declines by omission rather than by returning a lie.
type Adapter interface {
	// Name is the registry name, which is also the [harness.<name>] config
	// section and the name a workflow pins.
	Name() string

	// Enforceable lists the capabilities this adapter can control at all. It is
	// for display — `director harnesses` — and is not the gate.
	Enforceable() []Capability

	// Permits reports whether this adapter can honour an exact allow-set,
	// returning an error naming what it cannot hold.
	//
	// The gate is a method rather than a set intersection because
	// enforceability is not per-capability: it depends on the combination.
	// Claude Code can deny "push" when "execute" is already denied — there is
	// no shell to push from — but cannot deny it while allowing execute, since
	// git is just another command. A static list could express neither answer,
	// so it would have to be wrong in one direction, and the safe direction
	// would refuse every scope that withholds push, including read-only.
	//
	// The rule is fail-closed: when in doubt an adapter returns an error and
	// the spawn does not happen. Running with more access than the workflow
	// asked for, and warning about it, is the one outcome a permission system
	// must not have — the warning gets lost and the access does not.
	Permits(allow []Capability) error

	// Spawn starts a conversation and returns as soon as it is addressable —
	// never when the agent has answered. A spawn that blocked for a first turn
	// would stall the director's own turn, and dispatching several things and
	// then checking on them is the entire point of the tool.
	Spawn(ctx context.Context, req SpawnRequest) (SpawnResult, error)

	// Send pushes text into a running conversation.
	Send(ctx context.Context, req SendRequest) error

	// Get observes one engagement. It is the most frequent call the core makes,
	// so it must be cheap.
	Get(ctx context.Context, ref string) (Observation, error)

	// List observes every engagement this adapter can see.
	List(ctx context.Context, filter Filter) ([]Observation, error)

	// Stop stops an engagement.
	Stop(ctx context.Context, req StopRequest) error
}

// Reader is the optional ability to return an engagement's output.
//
// An adapter that cannot read must not implement this. Returning an empty
// result instead would read as "the agent said nothing", which is a different
// and much more dangerous claim than "I cannot see what it said".
type Reader interface {
	Read(ctx context.Context, req ReadRequest) (ReadResult, error)
}

// ReadRequest asks for an engagement's output since a cursor.
type ReadRequest struct {
	Ref    string
	Cursor string
	Limit  int
	// Include selects turn roles. Empty means the adapter's own default.
	Include []string
}

// ReadKind says what sort of record was returned. It is data, not capability:
// it tells the core and the director whether the cursor means anything and
// whether the record is lossy, without the core branching on a harness name.
type ReadKind string

const (
	// ReadTurns is an ordered, append-only conversation with a usable cursor.
	ReadTurns ReadKind = "turns"
	// ReadScreen is a terminal snapshot: no cursor, finite scrollback, and so
	// an absence of output is not evidence that the agent said nothing.
	ReadScreen ReadKind = "screen"
)

// Turn is one normalised unit of an agent's output.
type Turn struct {
	Seq  string    `json:"seq"`
	Role string    `json:"role"`
	At   time.Time `json:"at,omitempty"`
	Text string    `json:"text"`
}

// ReadResult is an engagement's output.
type ReadResult struct {
	Kind   ReadKind `json:"kind"`
	Cursor string   `json:"cursor"`
	Total  int      `json:"total"`
	// Complete is false when the record is lossy, as a screen snapshot always
	// is. The director is told rather than left to assume.
	Complete bool   `json:"complete"`
	Turns    []Turn `json:"turns"`
}

// Resumer is the optional ability to reopen a finished conversation.
//
// An adapter that cannot resume must not implement this, and the core must not
// fall back to spawning: that would create a second conversation with the same
// title, and the director would then be talking to the wrong one without any
// signal that it had happened.
type Resumer interface {
	Resume(ctx context.Context, req ResumeRequest) (SpawnResult, error)
}

// ResumeRequest reopens an engagement.
type ResumeRequest struct {
	Ref    string
	Dir    string
	Prompt string
	// Fork asks for a branch: a new conversation seeded with this one's
	// context, leaving the original untouched. The core mints a new engagement
	// for the result, because a fork is a new engagement and not a mutation.
	Fork bool
	Env  map[string]string
}

// SkillInstaller is the ability to say where director's skills belong for one
// harness, so that `director install` can offer it as a target.
//
// Being installable and being drivable are separate: a harness may declare
// where its skills go, may be able to run a conversation, or may do both.
// Installing needs only a set of paths, which is why this is not part of
// Adapter — requiring a whole Adapter to place a file would mean a harness
// director cannot drive could not be installed for either, and the two
// questions have nothing to do with each other.
//
// An Adapter that implements this is offered as an install target
// automatically. A harness that is only installable registers through
// RegisterSkillInstaller and never appears in `director harnesses`, because
// nothing can spawn into it. A harness with no answer — herdr hosts somebody
// else's agent and has no skills directory of its own — declines by omission
// rather than being offered a target that cannot be written.
type SkillInstaller interface {
	// SkillLocations reports where the skills go and whether the harness looks
	// present. It returns ErrNoSkillLocations when this harness has no answer.
	SkillLocations() (SkillLocations, error)
}

// ErrNoSkillLocations means the harness has no place to put skills. It is an
// ordinary answer, not a failure, and installing must pass over the harness
// quietly rather than reporting it as broken.
//
// A Go adapter says this by not implementing SkillInstaller at all. The
// sentinel is for adapters that cannot decide at compile time — a plugin
// adapter is one Go type standing for every plugin executable, so whether it
// has an answer is only known once the plugin has been asked.
var ErrNoSkillLocations = errors.New("harness declares no skills location")

// SkillLocations is everything installing needs to know about one harness.
//
// It is plain data, and the same shape a plugin returns from describe, so a
// first-party adapter and an external executable are indistinguishable to the
// installer. Anything the installer had to know that this could not carry would
// be a reason for it to special-case a harness name again.
type SkillLocations struct {
	// Description is the harness's name as a person would write it, shown in
	// the picker and above what was written.
	Description string `json:"description"`

	// GlobalDir is the absolute directory covering every project on this
	// machine. Empty means the harness has no such notion, and global install
	// is not offered for it.
	GlobalDir string `json:"global_dir"`

	// ProjectDir is where skills live inside a repository, relative to its
	// root. Empty means the harness has no such notion, and project install is
	// not offered for it.
	//
	// Relative rather than a resolved path because the project root is the
	// installer's to choose: an adapter that returned an absolute path would be
	// answering a question it was not asked, and would have to be trusted not
	// to have picked a directory somewhere else entirely.
	ProjectDir string `json:"project_dir"`

	// Verified records whether these paths were confirmed against a real
	// installation. An unverified guess must say so rather than look as
	// authoritative as one that was checked.
	Verified bool `json:"verified"`

	// Present reports whether the harness appears to be installed here. It
	// drives which targets are pre-selected, so it is a hint and never a gate:
	// a harness that is not detected is still offered, because detection is a
	// guess and refusing to install over it would be unfixable.
	Present bool `json:"present"`
}

var (
	registryMu sync.RWMutex
	registry   = map[string]Adapter{}
	installers = map[string]SkillInstaller{}
)

// RegisterSkillInstaller records where a harness keeps its skills without
// claiming director can drive it.
//
// It is how a harness that has no adapter — one director cannot spawn into, or
// cannot spawn into yet — still gets installed for. Adapters do not call this:
// one that implements SkillInstaller is picked up from the adapter registry, so
// there is one declaration per harness rather than two that can disagree.
func RegisterSkillInstaller(name string, installer SkillInstaller) {
	registryMu.Lock()
	defer registryMu.Unlock()
	installers[name] = installer
}

// NamedSkillInstaller is one harness that may have somewhere to put skills.
type NamedSkillInstaller struct {
	Name      string
	Installer SkillInstaller
}

// SkillInstallers returns every harness worth asking where its skills go,
// sorted by name: the adapters that implement SkillInstaller, plus the
// install-only registrations.
//
// It returns the installers rather than the answers because asking can be
// expensive and can fail — an external plugin is a process — and what to do
// about a harness that will not answer is the caller's decision, not the
// registry's.
func SkillInstallers() []NamedSkillInstaller {
	registryMu.RLock()
	defer registryMu.RUnlock()

	found := map[string]SkillInstaller{}
	for name, installer := range installers {
		found[name] = installer
	}
	// An adapter wins over an install-only registration of the same name, the
	// way a built-in adapter wins over a plugin: the thing that can actually
	// drive the harness is the more authoritative account of it.
	for name, adapter := range registry {
		if installer, ok := adapter.(SkillInstaller); ok {
			found[name] = installer
		}
	}

	names := make([]string, 0, len(found))
	for name := range found {
		names = append(names, name)
	}
	sort.Strings(names)

	ordered := make([]NamedSkillInstaller, 0, len(names))
	for _, name := range names {
		ordered = append(ordered, NamedSkillInstaller{Name: name, Installer: found[name]})
	}
	return ordered
}

// Register adds an adapter. Adapters call this from init, and the binary
// composes its adapter set by importing them for effect.
func Register(adapter Adapter) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[adapter.Name()] = adapter
}

// Lookup returns a registered adapter.
func Lookup(name string) (Adapter, error) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	adapter, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("no harness named %q (registered: %s)", name, strings.Join(namesLocked(), ", "))
	}
	return adapter, nil
}

// Names returns the registered adapter names, sorted.
func Names() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return namesLocked()
}

func namesLocked() []string {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Capabilities describes what an adapter can do, for `director harnesses`.
type Capabilities struct {
	Read   bool `json:"read"`
	Resume bool `json:"resume"`
}

// CapabilitiesOf probes the optional interfaces an adapter satisfies.
func CapabilitiesOf(adapter Adapter) Capabilities {
	var found Capabilities
	if _, ok := adapter.(Reader); ok {
		found.Read = true
	}
	if _, ok := adapter.(Resumer); ok {
		found.Resume = true
	}
	return found
}

// Allows reports whether an allow-set contains a capability. Adapters use it
// when implementing Permits.
func Allows(allow []Capability, capability Capability) bool {
	for _, allowed := range allow {
		if allowed == capability {
			return true
		}
	}
	return false
}

// Withheld returns every capability an allow-set does not grant — which is
// exactly the set an adapter is being asked to enforce.
func Withheld(allow []Capability) []Capability {
	var withheld []Capability
	for _, capability := range AllCapabilities {
		if !Allows(allow, capability) {
			withheld = append(withheld, capability)
		}
	}
	return withheld
}
