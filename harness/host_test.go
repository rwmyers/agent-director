package harness

import (
	"context"
	"testing"
)

// stub is the minimum Adapter a registry test needs. It is deliberately not a
// working harness: these tests are about declarations, not about spawning.
type stub struct {
	name     string
	hosting  *Hosting
	ref      string
	inside   bool
	locates  bool
	displays bool
}

func (s *stub) Name() string                            { return s.name }
func (s *stub) Enforceable() []Capability               { return nil }
func (s *stub) Permits([]Capability) error              { return nil }
func (s *stub) Send(context.Context, SendRequest) error { return nil }
func (s *stub) Stop(context.Context, StopRequest) error { return nil }
func (s *stub) Get(context.Context, string) (Observation, error) {
	return Observation{}, nil
}
func (s *stub) List(context.Context, Filter) ([]Observation, error) { return nil, nil }
func (s *stub) Spawn(context.Context, SpawnRequest) (SpawnResult, error) {
	return SpawnResult{}, nil
}

type hostingStub struct {
	*stub
}

func (h hostingStub) Hosts() Hosting { return *h.hosting }

type locatingStub struct {
	hostingStub
}

func (l locatingStub) Locate() (string, bool) { return l.ref, l.inside }

// displayingStub is a locating adapter that has declared itself a display
// layer, which is the multiplexer half of the nested case.
type displayingStub struct {
	locatingStub
}

func (d displayingStub) Displays() bool { return true }

// adapterFor wraps a stub in exactly the optional interfaces it declares, so a
// test can say "this adapter has no Hosts method" and mean it.
func adapterFor(s *stub) Adapter {
	switch {
	case s.hosting != nil && s.locates && s.displays:
		return displayingStub{locatingStub{hostingStub{s}}}
	case s.hosting != nil && s.locates:
		return locatingStub{hostingStub{s}}
	case s.hosting != nil:
		return hostingStub{s}
	default:
		return s
	}
}

func TestHostingString(t *testing.T) {
	for _, testCase := range []struct {
		hosting Hosting
		want    string
	}{
		{Hosting{}, "none"},
		{Hosting{Background: true}, "background"},
		{Hosting{Wake: true}, "wake"},
		{Hosting{Background: true, Wake: true}, "background, wake"},
	} {
		if got := testCase.hosting.String(); got != testCase.want {
			t.Errorf("%#v.String() = %q, want %q", testCase.hosting, got, testCase.want)
		}
	}
}

func TestHostingCoversAndNarrow(t *testing.T) {
	full := Hosting{Background: true, Wake: true}
	background := Hosting{Background: true}

	if !full.Covers(background) {
		t.Error("a full declaration should cover a narrower one")
	}
	if background.Covers(full) {
		t.Error("a narrow declaration must not cover a wider one: that is widening")
	}
	if !UnknownHosting().Covers(UnknownHosting()) {
		t.Error("nothing covers nothing")
	}
	if got := full.Narrow(background); got != background {
		t.Errorf("Narrow() = %v, want %v", got, background)
	}
	if got := background.Narrow(Hosting{Wake: true}); got != (Hosting{}) {
		t.Errorf("Narrow() = %v, want nothing", got)
	}
}

func TestParseHosting(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		values  []string
		want    Hosting
		wantErr bool
	}{
		{name: "both", values: []string{"background", "wake"}, want: Hosting{Background: true, Wake: true}},
		{name: "one", values: []string{"background"}, want: Hosting{Background: true}},
		{name: "none", values: []string{"none"}},
		{name: "empty is refused", values: nil, wantErr: true},
		{name: "unknown word", values: []string{"backgroud"}, wantErr: true},
		{name: "none with something else", values: []string{"none", "wake"}, wantErr: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := ParseHosting(testCase.values)
			if testCase.wantErr {
				if err == nil {
					t.Fatalf("ParseHosting(%v) = %v, want an error", testCase.values, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseHosting(%v): %v", testCase.values, err)
			}
			if got != testCase.want {
				t.Errorf("ParseHosting(%v) = %v, want %v", testCase.values, got, testCase.want)
			}
		})
	}
}

func TestHostingOfDefaultsToNothing(t *testing.T) {
	plain := adapterFor(&stub{name: "plain"})
	if got := HostingOf(plain); got != UnknownHosting() {
		t.Errorf("HostingOf(an adapter that declares nothing) = %v, want %v", got, UnknownHosting())
	}

	declared := Hosting{Background: true}
	speaks := adapterFor(&stub{name: "speaks", hosting: &declared})
	if got := HostingOf(speaks); got != declared {
		t.Errorf("HostingOf() = %v, want %v", got, declared)
	}
}

func TestLocate(t *testing.T) {
	full := Hosting{Background: true, Wake: true}
	backgroundOnly := Hosting{Background: true}

	adapters := map[string]Adapter{
		"outer":     adapterFor(&stub{name: "outer", hosting: &full, locates: true, inside: true, ref: "pane-3"}),
		"inner":     adapterFor(&stub{name: "inner", hosting: &backgroundOnly, locates: true, inside: true, ref: "sess-1"}),
		"elsewhere": adapterFor(&stub{name: "elsewhere", hosting: &full, locates: true, inside: false}),
		"silent":    adapterFor(&stub{name: "silent"}),
	}
	lookup := func(name string) (Adapter, error) {
		adapter, ok := adapters[name]
		if !ok {
			return nil, ErrNoSkillLocations // any error; Locate must skip it
		}
		return adapter, nil
	}

	t.Run("two layers of the same kind resolve to the one offering the most", func(t *testing.T) {
		// Neither of these declared itself a display, so there is nothing to
		// tell them apart by depth and what they offer breaks the tie.
		// "inner" sorts first, so taking the first answer would lose wake.
		got, ok := Locate([]string{"elsewhere", "inner", "outer", "silent", "missing"}, lookup)
		if !ok {
			t.Fatal("Locate() found nothing")
		}
		if got.Harness != "outer" || got.Ref != "pane-3" || got.Hosting != full {
			t.Errorf("Locate() = %+v, want outer/pane-3/%v", got, full)
		}
	})

	t.Run("an agent inside a multiplexer resolves to the agent", func(t *testing.T) {
		// The real shape of the bug: a Claude Code conversation in a herdr
		// pane. The pane offers more — it can be typed into — and taking it
		// would tell the director it can be woken when nothing can reach the
		// agent sitting in the pane. The innermost layer is the answer, and the
		// display layer says so itself rather than being recognised by name.
		layered := map[string]Adapter{
			"pane":  adapterFor(&stub{name: "pane", hosting: &full, locates: true, displays: true, inside: true, ref: "pane-3"}),
			"agent": adapterFor(&stub{name: "agent", hosting: &backgroundOnly, locates: true, inside: true, ref: "sess-1"}),
		}
		layeredLookup := func(name string) (Adapter, error) {
			adapter, ok := layered[name]
			if !ok {
				return nil, ErrNoSkillLocations
			}
			return adapter, nil
		}

		// Both orders, because the answer must come from the declaration and
		// not from whichever adapter happened to be asked first.
		for _, order := range [][]string{{"agent", "pane"}, {"pane", "agent"}} {
			got, ok := Locate(order, layeredLookup)
			if !ok {
				t.Fatalf("Locate(%v) found nothing", order)
			}
			if got.Harness != "agent" || got.Ref != "sess-1" || got.Hosting != backgroundOnly {
				t.Errorf("Locate(%v) = %+v, want agent/sess-1/%v", order, got, backgroundOnly)
			}
		}
	})

	t.Run("a multiplexer with nothing inside it is still the host", func(t *testing.T) {
		// Standing aside is only ever in favour of an agent. A director at a
		// bare shell prompt in a pane has the pane as its host, wake and all,
		// and the fix must not cost it that.
		only := map[string]Adapter{
			"pane": adapterFor(&stub{name: "pane", hosting: &full, locates: true, displays: true, inside: true, ref: "pane-3"}),
		}
		got, ok := Locate([]string{"pane"}, func(name string) (Adapter, error) {
			adapter, present := only[name]
			if !present {
				return nil, ErrNoSkillLocations
			}
			return adapter, nil
		})
		if !ok || got.Harness != "pane" || got.Hosting != full {
			t.Errorf("Locate() = %+v, %v, want pane offering %v", got, ok, full)
		}
	})

	t.Run("nothing answering is not an error", func(t *testing.T) {
		if got, ok := Locate([]string{"elsewhere", "silent", "missing"}, lookup); ok {
			t.Errorf("Locate() = %+v, want no location", got)
		}
	})
}
