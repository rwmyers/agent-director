package director

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rwmyers/agent-director/harness"
)

// hostAdapter is a fakeAdapter that also answers the two host questions. The
// pair is split so a test can build an adapter that declares nothing, which is
// the case the conservative default exists for.
type hostAdapter struct {
	fakeAdapter
	hosting harness.Hosting
	ref     string
	inside  bool
}

func (h *hostAdapter) Hosts() harness.Hosting { return h.hosting }
func (h *hostAdapter) Locate() (string, bool) { return h.ref, h.inside }

// withHosts points a director at a set of host-aware adapters, replacing both
// the registry lookup and the list of names location walks.
func withHosts(d *Director, adapters ...*hostAdapter) {
	byName := map[string]harness.Adapter{}
	names := make([]string, 0, len(adapters))
	for _, adapter := range adapters {
		byName[adapter.name] = adapter
		names = append(names, adapter.name)
	}
	d.Lookup = func(name string) (harness.Adapter, error) {
		adapter, ok := byName[name]
		if !ok {
			return nil, errors.New("no such harness: " + name)
		}
		return adapter, nil
	}
	d.Names = func() []string { return names }
}

func TestLocateHost(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	full := harness.Hosting{Background: true, Wake: true}

	t.Run("detection takes the harness that recognises its own environment", func(t *testing.T) {
		t.Parallel()
		d := newTestDirector(t, &fakeAdapter{name: "fake"}, now)
		withHosts(d,
			&hostAdapter{fakeAdapter: fakeAdapter{name: "elsewhere"}, hosting: full},
			&hostAdapter{fakeAdapter: fakeAdapter{name: "panes"}, hosting: full, ref: "w6:p1", inside: true},
		)

		host := d.locateHost()
		if host.Harness != "panes" || host.Ref != "w6:p1" || host.Hosting != full {
			t.Errorf("locateHost() = %+v, want panes/w6:p1/%v", host, full)
		}
		if host.Source != HostDetected {
			t.Errorf("Source = %q, want %q", host.Source, HostDetected)
		}
	})

	t.Run("nothing detected offers nothing", func(t *testing.T) {
		t.Parallel()
		d := newTestDirector(t, &fakeAdapter{name: "fake"}, now)
		withHosts(d, &hostAdapter{fakeAdapter: fakeAdapter{name: "elsewhere"}, hosting: full})

		host := d.locateHost()
		if host.Known() || host.Hosting != harness.UnknownHosting() {
			t.Errorf("locateHost() = %+v, want nothing known and nothing offered", host)
		}
		if host.Source != HostUnknown {
			t.Errorf("Source = %q, want %q", host.Source, HostUnknown)
		}
	})

	t.Run("configuration outranks detection", func(t *testing.T) {
		t.Parallel()
		d := newTestDirector(t, &fakeAdapter{name: "fake"}, now)
		withHosts(d,
			&hostAdapter{fakeAdapter: fakeAdapter{name: "panes"}, hosting: full, ref: "w6:p1", inside: true},
			&hostAdapter{fakeAdapter: fakeAdapter{name: "sessions"}, hosting: harness.Hosting{Background: true}},
		)
		d.Config.Host = "sessions"

		host := d.locateHost()
		if host.Harness != "sessions" || host.Source != HostConfigured {
			t.Errorf("locateHost() = %+v, want the configured sessions", host)
		}
		if host.Hosting != (harness.Hosting{Background: true}) {
			t.Errorf("Hosting = %v, want background only", host.Hosting)
		}
	})

	t.Run("a configured harness this binary does not have offers nothing", func(t *testing.T) {
		t.Parallel()
		// Refusing to load would turn one missing adapter into a director that
		// cannot run at all. Offering nothing costs a turn.
		d := newTestDirector(t, &fakeAdapter{name: "fake"}, now)
		withHosts(d)
		d.Config.Host = "somethingelse"

		host := d.locateHost()
		if host.Harness != "somethingelse" || host.Hosting != harness.UnknownHosting() {
			t.Errorf("locateHost() = %+v, want the name and nothing offered", host)
		}
	})

	t.Run("configuration narrows what an adapter declares", func(t *testing.T) {
		t.Parallel()
		d := newTestDirector(t, &fakeAdapter{name: "fake"}, now)
		withHosts(d, &hostAdapter{fakeAdapter: fakeAdapter{name: "panes"}, hosting: full, ref: "w6:p1", inside: true})
		d.Config.HostLimits = map[string]harness.Hosting{"panes": {Background: true}}

		host := d.locateHost()
		if host.Hosting != (harness.Hosting{Background: true}) {
			t.Errorf("Hosting = %v, want the narrowed background only", host.Hosting)
		}
	})
}

func TestAttachRecordsTheHost(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	d := newTestDirector(t, &fakeAdapter{name: "fake"}, now)
	withHosts(d, &hostAdapter{
		fakeAdapter: fakeAdapter{name: "panes"},
		hosting:     harness.Hosting{Background: true, Wake: true},
		ref:         "w6:p1",
		inside:      true,
	})

	if err := d.claim(); err != nil {
		t.Fatalf("claim() = %v, want no error", err)
	}

	// It must survive a reload: `director wait` and an agent's `director
	// report` are separate processes that only ever see the file.
	reloaded, err := LoadState(d.State.Path)
	if err != nil {
		t.Fatalf("LoadState() = %v, want no error", err)
	}
	want := Host{
		Harness: "panes",
		Ref:     "w6:p1",
		Hosting: harness.Hosting{Background: true, Wake: true},
		Source:  HostDetected,
	}
	if reloaded.Host != want {
		t.Errorf("reloaded host = %+v, want %+v", reloaded.Host, want)
	}
}

func TestAttachReplacesAPreviousHost(t *testing.T) {
	t.Parallel()
	// Two conversations: the last one to attach owns the address. Merging would
	// leave the earlier conversation's pane recorded, so a wake would type into
	// somebody else's screen.
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	d := newTestDirector(t, &fakeAdapter{name: "fake"}, now)
	withHosts(d, &hostAdapter{
		fakeAdapter: fakeAdapter{name: "panes"},
		hosting:     harness.Hosting{Background: true, Wake: true},
		ref:         "w6:p1",
		inside:      true,
	})
	if err := d.claim(); err != nil {
		t.Fatalf("claim() = %v, want no error", err)
	}

	withHosts(d, &hostAdapter{
		fakeAdapter: fakeAdapter{name: "panes"},
		hosting:     harness.Hosting{Background: true, Wake: true},
		ref:         "w9:p4",
		inside:      true,
	})
	if err := d.claim(); err != nil {
		t.Fatalf("claim() = %v, want no error", err)
	}

	if d.State.Host.Ref != "w9:p4" {
		t.Errorf("host ref = %q, want the second conversation's w9:p4", d.State.Host.Ref)
	}
}

func TestHostsConfigMayNarrowButNotWiden(t *testing.T) {
	t.Parallel()
	declared := &hostAdapter{
		fakeAdapter: fakeAdapter{name: "panes"},
		hosting:     harness.Hosting{Background: true},
	}
	lookup := func(name string) (harness.Adapter, error) {
		if name != declared.name {
			return nil, errors.New("no such harness: " + name)
		}
		return declared, nil
	}

	t.Run("narrowing is read", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, "director.conf"), "host = panes\n\n[harness.panes]\nhosts = none\n")

		config, err := loadConfig(root, lookup)
		if err != nil {
			t.Fatalf("loadConfig() = %v, want no error", err)
		}
		if config.Host != "panes" {
			t.Errorf("Host = %q, want panes", config.Host)
		}
		if got := config.HostingFor("panes", declared.hosting); got != harness.UnknownHosting() {
			t.Errorf("HostingFor() = %v, want nothing", got)
		}
	})

	t.Run("widening is refused and names the adapter", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, "director.conf"), "[harness.panes]\nhosts = background, wake\n")

		_, err := loadConfig(root, lookup)
		if err == nil {
			t.Fatal("loadConfig() accepted a widening")
		}
		if !strings.Contains(err.Error(), "panes") {
			t.Errorf("loadConfig() = %v, want the adapter named", err)
		}
	})

	t.Run("a harness this binary does not have is not checked", func(t *testing.T) {
		// One configuration is shared across machines. A plugin installed on
		// one of them must not make director refuse to start on the others.
		root := t.TempDir()
		writeFile(t, filepath.Join(root, "director.conf"), "[harness.elsewhere]\nhosts = background, wake\n")

		config, err := loadConfig(root, lookup)
		if err != nil {
			t.Fatalf("loadConfig() = %v, want no error", err)
		}
		if _, ok := config.HostLimits["elsewhere"]; !ok {
			t.Error("the limit was dropped rather than kept for an adapter that may turn up")
		}
	})

	t.Run("a misspelt capability is refused", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, "director.conf"), "[harness.panes]\nhosts = backgroud\n")

		if _, err := loadConfig(root, lookup); err == nil {
			t.Fatal("loadConfig() accepted an unknown hosting capability")
		}
	})
}

func TestNextTurn(t *testing.T) {
	t.Parallel()
	// The four combinations are four different instructions, which is the whole
	// reason the pair is not an enum with a "can be waited on" mode.
	for _, testCase := range []struct {
		hosting harness.Hosting
		want    string
	}{
		{harness.Hosting{Background: true, Wake: true}, "background `director wait`"},
		{harness.Hosting{Background: true}, "background `director wait`"},
		{harness.Hosting{Wake: true}, "do not background"},
		{harness.Hosting{}, "neither"},
	} {
		host := Host{Harness: "panes", Hosting: testCase.hosting}
		if got := host.NextTurn(); !strings.Contains(got, testCase.want) {
			t.Errorf("NextTurn() for %v = %q, want it to contain %q", testCase.hosting, got, testCase.want)
		}
	}

	// Wake alone must not read as an invitation to block.
	wakeOnly := Host{Harness: "panes", Hosting: harness.Hosting{Wake: true}}
	if strings.Contains(wakeOnly.NextTurn(), "background `director wait` to be woken") {
		t.Errorf("NextTurn() for wake-only = %q, want it not to suggest backgrounding a wait", wakeOnly.NextTurn())
	}
}
