package cli

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rwmyers/agent-director/harness"
)

// fakeInstaller is a harness that only says where its skills go. It is not
// drivable, which is the point: installing must not require an adapter.
type fakeInstaller struct {
	locations harness.SkillLocations
	err       error
}

func (f fakeInstaller) SkillLocations() (harness.SkillLocations, error) {
	return f.locations, f.err
}

// fakeAdapter is a drivable harness that also declares a skills location.
//
// The Adapter interface is embedded rather than implemented because installing
// never calls any of it. If a method other than the ones stubbed here is ever
// reached, this panics — which is a more useful failure than a test that
// quietly proves the installer knows more about a harness than it should.
type fakeAdapter struct {
	harness.Adapter
	fakeInstaller
	name string
}

func (f fakeAdapter) Name() string { return f.name }

func TestInstallTargets(t *testing.T) {
	// No t.Parallel: the registry is process-global. Everything is registered
	// up front and enumerated once, so the subtests only read.
	declared := harness.SkillLocations{
		Description: "Fake Harness",
		GlobalDir:   "/fake/global/skills",
		ProjectDir:  filepath.Join(".fake", "skills"),
		Verified:    true,
		Present:     true,
	}

	harness.RegisterSkillInstaller("fake-declares", fakeInstaller{locations: declared})
	harness.RegisterSkillInstaller("fake-declines", fakeInstaller{err: harness.ErrNoSkillLocations})
	harness.RegisterSkillInstaller("fake-broken", fakeInstaller{err: errors.New("plugin exited 1")})
	harness.RegisterSkillInstaller("fake-nowhere", fakeInstaller{locations: harness.SkillLocations{Description: "Nowhere"}})
	harness.RegisterSkillInstaller("fake-anonymous", fakeInstaller{
		locations: harness.SkillLocations{GlobalDir: "/fake/anonymous"},
	})
	harness.Register(fakeAdapter{
		name: "fake-drivable",
		fakeInstaller: fakeInstaller{locations: harness.SkillLocations{
			Description: "Drivable", GlobalDir: "/fake/drivable",
		}},
	})
	harness.Register(fakeAdapter{name: "fake-undeclared"})

	found := map[string]host{}
	for _, target := range installTargets() {
		found[target.name] = target
	}

	t.Run("a harness that declares a location is offered", func(t *testing.T) {
		target, ok := found["fake-declares"]
		if !ok {
			t.Fatalf("installTargets() = %v, want the declaring harness", found)
		}
		if target.locations != declared {
			t.Errorf("locations = %+v, want what the harness declared: %+v", target.locations, declared)
		}
	})

	t.Run("a drivable adapter that declares a location is offered too", func(t *testing.T) {
		// A first-party adapter and an install-only declaration reach the
		// installer by the same route; nothing here can tell them apart.
		if _, ok := found["fake-drivable"]; !ok {
			t.Errorf("installTargets() = %v, want the adapter that declared a location", found)
		}
	})

	t.Run("a harness with nothing to say is skipped", func(t *testing.T) {
		// Three ways of having nothing to say, none of which may crash or
		// produce a target that fails at the point of writing.
		for _, name := range []string{"fake-declines", "fake-broken", "fake-nowhere", "fake-undeclared"} {
			if _, ok := found[name]; ok {
				t.Errorf("installTargets() offered %q, want it left out", name)
			}
		}
	})

	t.Run("a harness that names no description is listed under its own name", func(t *testing.T) {
		// Rather than a blank line in the picker, which is unselectable in
		// practice because nobody can tell what it is.
		target, ok := found["fake-anonymous"]
		if !ok {
			t.Fatalf("installTargets() = %v, want the harness with no description", found)
		}
		if target.locations.Description != "fake-anonymous" {
			t.Errorf("Description = %q, want the harness name as a fallback", target.locations.Description)
		}
	})
}

func TestTargetDir(t *testing.T) {
	t.Parallel()
	both := host{name: "both", locations: harness.SkillLocations{
		GlobalDir:  "/home/someone/.harness/skills",
		ProjectDir: filepath.Join(".harness", "skills"),
	}}

	t.Run("global is the absolute directory the harness declared", func(t *testing.T) {
		t.Parallel()
		got, err := targetDir(both, ScopeGlobal, "/repo")
		if err != nil {
			t.Fatalf("targetDir() = %v, want no error", err)
		}
		if got != "/home/someone/.harness/skills" {
			t.Errorf("targetDir(global) = %q, want the declared directory", got)
		}
	})

	t.Run("project joins the declared relative path under the project root", func(t *testing.T) {
		t.Parallel()
		// Relative because the root is the installer's to choose: a harness
		// that returned an absolute path would be picking somebody else's
		// repository for them.
		got, err := targetDir(both, ScopeProject, "/repo")
		if err != nil {
			t.Fatalf("targetDir() = %v, want no error", err)
		}
		if got != filepath.Join("/repo", ".harness", "skills") {
			t.Errorf("targetDir(project) = %q, want it under the project root", got)
		}
	})

	t.Run("a scope the harness has no notion of is refused by name", func(t *testing.T) {
		t.Parallel()
		globalOnly := host{name: "global-only", locations: harness.SkillLocations{GlobalDir: "/g"}}
		_, err := targetDir(globalOnly, ScopeProject, "/repo")
		if err == nil {
			t.Fatal("targetDir(project) = nil error, want a refusal")
		}
		if !strings.Contains(err.Error(), "global-only") {
			t.Errorf("targetDir() error = %q, want it to name the harness", err)
		}
	})
}

// TestPickerOffersEveryAcceptedHost pins the picker to the accepted set.
//
// These are two code paths onto one list, and when they disagree the disagreement
// is invisible: --host works, the picker does not offer it, and the person
// concludes director cannot install for a harness it installs for perfectly
// well. Nothing is asserted about the order beyond its being the same, because
// the order is installTargets'.
func TestPickerOffersEveryAcceptedHost(t *testing.T) {
	// No t.Parallel: the registry is process-global.
	harness.RegisterSkillInstaller("picker-declares", fakeInstaller{locations: harness.SkillLocations{
		Description: "Picker Declares", GlobalDir: "/picker/global",
	}})
	harness.RegisterSkillInstaller("picker-declines", fakeInstaller{err: harness.ErrNoSkillLocations})

	available := installTargets()
	options := hostOptions(available)
	if len(options) != len(available) {
		t.Fatalf("hostOptions() offered %d of %d targets", len(options), len(available))
	}
	for i, candidate := range available {
		if options[i].Value != candidate.name {
			t.Errorf("option %d = %q, want the accepted host %q", i, options[i].Value, candidate.name)
		}
		// Every target must be reachable by name from the picker as well as
		// from --host, which is what makes the two sets the same set.
		if _, ok := lookupHost(available, options[i].Value); !ok {
			t.Errorf("picker offers %q, which --host would refuse", options[i].Value)
		}
	}
	if _, ok := lookupHost(available, "picker-declines"); ok {
		t.Error("a harness that declined a skills location is an accepted host")
	}
}
