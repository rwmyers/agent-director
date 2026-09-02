package harness

import (
	"fmt"
	"strings"
)

// Hosting is what a harness offers the director running INSIDE it.
//
// Everything else in this package describes a harness director dispatches work
// TO. This is the other direction, and it is a separate question: the same
// adapter can be excellent at starting conversations and useless as a place for
// a director to sit, because driving a harness needs only a socket while being
// hosted by one is about what the director's own process can do and what can
// reach it.
//
// # Two booleans, not two modes
//
// Background is whether a director here can background a blocking
// `director wait` and still be reachable. Wake is whether something can reach
// into this director's live conversation and make it take a turn. They vary
// independently, and the combination that would break an enum is already in
// this repository: as a host, Claude Code can background a wait but cannot be
// woken, because reaching it means starting a fresh headless process against
// its transcript rather than typing into the conversation somebody is watching.
//
// # Both default false
//
// The zero value claims nothing, and that is the safe answer, because the two
// failures are not symmetric. A director told it can background where it cannot
// freezes, and the person cannot reach it. A director told it can be woken when
// it cannot hands back promising a watcher that does not exist. Being wrong in
// the conservative direction costs only that the director checks on its own
// turn — which is what it does today.
type Hosting struct {
	Background bool `json:"background"`
	Wake       bool `json:"wake"`
}

// Hosting capability names, as they are written in configuration and printed.
const (
	HostBackground = "background"
	HostWake       = "wake"
	// HostNone is how somebody writes down "neither", which is otherwise
	// indistinguishable from having left the key out.
	HostNone = "none"
)

// UnknownHosting is the safe default: a host nothing is known about.
//
// It is a function rather than a bare Hosting{} at call sites so that reading
// the code says what the zero value means. Claiming nothing is a decision, not
// an oversight.
func UnknownHosting() Hosting { return Hosting{} }

// String renders the pair the way a person reads it.
func (h Hosting) String() string {
	var parts []string
	if h.Background {
		parts = append(parts, HostBackground)
	}
	if h.Wake {
		parts = append(parts, HostWake)
	}
	if len(parts) == 0 {
		return HostNone
	}
	return strings.Join(parts, ", ")
}

// Covers reports whether h grants at least everything other does.
//
// This is the widening check: configuration may take capabilities away from
// what an adapter declared, and may never add them. An adapter is the only
// thing that knows whether its harness can actually do this, and a config key
// that could grant a capability the adapter denies would be a promise nothing
// is able to keep.
func (h Hosting) Covers(other Hosting) bool {
	return (h.Background || !other.Background) && (h.Wake || !other.Wake)
}

// Narrow returns what is left of h after applying a limit.
func (h Hosting) Narrow(limit Hosting) Hosting {
	return Hosting{
		Background: h.Background && limit.Background,
		Wake:       h.Wake && limit.Wake,
	}
}

// ParseHosting reads a hosting declaration written as a list of names, naming
// the valid set on failure so a typo in director.conf is fixable without
// opening the source.
//
// An empty list is refused rather than read as "none". A key somebody wrote and
// left blank is far more likely to be an unfinished edit than a deliberate
// statement that this harness offers nothing, and reading it as the latter
// would silently disable a director's ability to wait.
func ParseHosting(values []string) (Hosting, error) {
	var found Hosting
	var none bool
	for _, value := range values {
		switch strings.TrimSpace(value) {
		case HostBackground:
			found.Background = true
		case HostWake:
			found.Wake = true
		case HostNone:
			none = true
		default:
			return Hosting{}, fmt.Errorf("unknown hosting capability %q (valid: %s, %s, %s)",
				value, HostBackground, HostWake, HostNone)
		}
	}
	if none && (found.Background || found.Wake) {
		return Hosting{}, fmt.Errorf("%q cannot be combined with %s or %s", HostNone, HostBackground, HostWake)
	}
	if !none && !found.Background && !found.Wake {
		return Hosting{}, fmt.Errorf("no hosting capability given (valid: %s, %s, %s)",
			HostBackground, HostWake, HostNone)
	}
	return found, nil
}

// Host is the optional declaration of what a harness offers a director running
// inside it.
//
// An adapter that does not implement this declares nothing, which is
// UnknownHosting and refuses everything. That is the right default for an
// adapter written before this existed: it was never asked the question, so it
// has not answered it, and inferring an answer from silence is exactly the
// mistake the zero value is chosen to avoid.
type Host interface {
	Hosts() Hosting
}

// SelfLocator is the optional ability to say whether this process is running
// inside this harness, and which conversation it is.
//
// It generalises herdr's InPane. The ref is the harness's own handle for the
// conversation the director occupies — a pane id, a session id — and is what
// something would address to wake it. An adapter that can tell it is being run
// from inside its harness but cannot name the conversation returns an empty ref
// and true: the id is a label, and declining to notice the harness because the
// label is missing would turn a correct answer into a wrong one.
//
// It must be cheap and it must not fail. Location runs when a conversation
// attaches, over every registered adapter, so an implementation that blocked on
// a network call would make attaching hang on a harness nobody is using.
type SelfLocator interface {
	Locate() (ref string, inside bool)
}

// HostingOf reports what an adapter declares it offers as a host.
func HostingOf(adapter Adapter) Hosting {
	if host, ok := adapter.(Host); ok {
		return host.Hosts()
	}
	return UnknownHosting()
}

// Location is one adapter's answer to "is this director running inside you".
type Location struct {
	Harness string
	Ref     string
	Hosting Hosting
}

// Locate asks every named adapter whether this process is running inside it,
// and returns the best answer.
//
// Nesting is real and is not an error: a director can be a Claude Code
// conversation running in a herdr pane, and both adapters correctly answer yes.
// When that happens the one offering the most is taken, because these bits
// describe what is possible rather than who is nominally in charge — if
// something can reach into this conversation, then this conversation can be
// woken, whichever layer owns the keyboard. Ties fall to the order the caller
// gave, which Names sorts, so the answer is stable rather than dependent on map
// iteration.
//
// An adapter that fails to resolve, or that declines to implement SelfLocator,
// is skipped rather than reported. Location is ambient — nobody asked for it —
// and something nobody asked for must not be able to break attaching.
func Locate(names []string, lookup func(string) (Adapter, error)) (Location, bool) {
	if lookup == nil {
		lookup = Lookup
	}
	var best Location
	var found bool
	for _, name := range names {
		adapter, err := lookup(name)
		if err != nil {
			continue
		}
		locator, ok := adapter.(SelfLocator)
		if !ok {
			continue
		}
		ref, inside := locator.Locate()
		if !inside {
			continue
		}
		candidate := Location{Harness: name, Ref: ref, Hosting: HostingOf(adapter)}
		if !found || offers(candidate.Hosting) > offers(best.Hosting) {
			best, found = candidate, true
		}
	}
	return best, found
}

// offers counts the capabilities a hosting declaration grants, for choosing
// between nested harnesses.
func offers(h Hosting) int {
	count := 0
	if h.Background {
		count++
	}
	if h.Wake {
		count++
	}
	return count
}
