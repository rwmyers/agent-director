package director

import (
	"fmt"
	"time"

	"github.com/rwmyers/agent-director/harness"
)

// HostSource says how a director's host was decided. It exists so the answer
// can say where it came from: detection is ambient, and somebody reading a
// refusal they disagree with needs to know whether a file or the environment
// produced it.
type HostSource string

const (
	// HostUnknown means nothing answered and nothing was configured.
	HostUnknown HostSource = "unknown"
	// HostDetected means an adapter recognised its own environment.
	HostDetected HostSource = "detected"
	// HostConfigured means `host` in director.conf named it.
	HostConfigured HostSource = "configured"
)

// Host is where the conversation driving this director is itself running.
//
// It is the counterpart to the harness an engagement runs on, and the two are
// unrelated: a director in a herdr pane can dispatch to Claude Code, and one in
// Claude Code can dispatch to panes. What this records is the director's own
// seat — what it can do about arranging its next turn, and what address, if
// any, something could use to give it one.
//
// It is re-detected on every attach and never merged with what was there
// before. The previous conversation's host is a fact about that conversation
// and is not evidence about this one; carrying it forward is how a director
// that moved would keep an address that now rings somebody else's screen.
type Host struct {
	// Harness is the adapter name, empty when nothing was found.
	Harness string `json:"harness,omitempty"`
	// Ref is the harness's handle for the conversation the director occupies —
	// a pane id, a session id. It may be empty even for a known host.
	Ref     string          `json:"ref,omitempty"`
	Hosting harness.Hosting `json:"hosting"`
	Source  HostSource      `json:"source"`

	// LastWokenAt is when an engagement last rang this director, and is the
	// floor that keeps a busy fleet from typing into the conversation on every
	// report.
	LastWokenAt time.Time `json:"last_woken_at,omitempty"`
}

// Known reports whether anything is known about where this director is sitting.
func (h Host) Known() bool { return h.Harness != "" }

// Describe says what was decided, in one line, for a person or an agent to act
// on. It is the sentence `director attach` prints and the one a refusal quotes.
func (h Host) Describe() string {
	if !h.Known() {
		return "no host detected"
	}
	return fmt.Sprintf("%s (%s), offering %s", h.Harness, h.Source, h.Hosting)
}

// locateHost works out where this director is running.
//
// Configuration wins outright, because it is somebody stating a fact about
// their own setup and an ambient signal must never overrule a statement. With
// nothing configured, every adapter that can recognise its own conversations is
// asked, and the best answer is taken — see harness.Locate for what happens
// when a director is inside two harnesses at once, which is ordinary rather
// than exceptional.
//
// A configured harness that is not registered in this binary is still recorded
// by name, offering nothing. Refusing to load would turn one unavailable
// adapter into a director that cannot run at all, and offering nothing is
// exactly the conservative answer the zero value is for: the cost is that the
// director checks on its own turn.
func (d *Director) locateHost() Host {
	if name := d.Config.Host; name != "" {
		host := Host{Harness: name, Source: HostConfigured}
		if adapter, err := d.lookup(name); err == nil {
			host.Hosting = harness.HostingOf(adapter)
			if locator, ok := adapter.(harness.SelfLocator); ok {
				if ref, inside := locator.Locate(); inside {
					host.Ref = ref
				}
			}
		}
		host.Hosting = d.Config.HostingFor(name, host.Hosting)
		return host
	}

	located, ok := harness.Locate(d.harnessNames(), d.lookup)
	if !ok {
		return Host{Source: HostUnknown}
	}
	return Host{
		Harness: located.Harness,
		Ref:     located.Ref,
		Hosting: d.Config.HostingFor(located.Harness, located.Hosting),
		Source:  HostDetected,
	}
}

// lookup resolves an adapter through the injected registry.
func (d *Director) lookup(name string) (harness.Adapter, error) {
	if d.Lookup != nil {
		return d.Lookup(name)
	}
	return harness.Lookup(name)
}

// harnessNames lists the adapters worth asking where this director is sitting.
func (d *Director) harnessNames() []string {
	if d.Names != nil {
		return d.Names()
	}
	return harness.Names()
}
