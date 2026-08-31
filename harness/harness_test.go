package harness

import (
	"testing"
)

type stubInstaller struct{ description string }

func (s stubInstaller) SkillLocations() (SkillLocations, error) {
	return SkillLocations{Description: s.description}, nil
}

// stubAdapter is a drivable harness. Only Name is called here; the rest of the
// interface is embedded and would panic if the registry ever reached for it.
type stubAdapter struct {
	Adapter
	name string
}

func (s stubAdapter) Name() string { return s.name }

type stubInstallingAdapter struct {
	stubAdapter
	stubInstaller
}

func TestSkillInstallers(t *testing.T) {
	// No t.Parallel: the registry is process-global.
	RegisterSkillInstaller("stub-install-only", stubInstaller{description: "Install Only"})
	Register(stubAdapter{name: "stub-drivable-only"})
	Register(stubInstallingAdapter{
		stubAdapter:   stubAdapter{name: "stub-both"},
		stubInstaller: stubInstaller{description: "From The Adapter"},
	})
	// The same name registered both ways. The adapter is the more authoritative
	// account of a harness it can actually drive, so it wins — the same rule
	// that makes a built-in adapter beat a plugin of the same name.
	RegisterSkillInstaller("stub-both", stubInstaller{description: "From The Registration"})

	found := map[string]SkillInstaller{}
	var order []string
	for _, candidate := range SkillInstallers() {
		found[candidate.Name] = candidate.Installer
		order = append(order, candidate.Name)
	}

	t.Run("an install-only harness is listed without being drivable", func(t *testing.T) {
		if _, ok := found["stub-install-only"]; !ok {
			t.Errorf("SkillInstallers() = %v, want the install-only harness", order)
		}
		// And it is not spawnable, which is the separation the two registries
		// exist to keep.
		if _, err := Lookup("stub-install-only"); err == nil {
			t.Error("Lookup(install-only) succeeded, want no adapter for a harness director cannot drive")
		}
	})

	t.Run("an adapter that declares nothing is not listed", func(t *testing.T) {
		if _, ok := found["stub-drivable-only"]; ok {
			t.Error("SkillInstallers() offered a harness that never said where its skills go")
		}
	})

	t.Run("an adapter wins over an install-only registration of the same name", func(t *testing.T) {
		installer, ok := found["stub-both"]
		if !ok {
			t.Fatalf("SkillInstallers() = %v, want the harness registered both ways", order)
		}
		locations, err := installer.SkillLocations()
		if err != nil {
			t.Fatalf("SkillLocations() = %v, want no error", err)
		}
		if locations.Description != "From The Adapter" {
			t.Errorf("Description = %q, want the adapter's own declaration", locations.Description)
		}
	})

	t.Run("the order is stable", func(t *testing.T) {
		// The installer renders this straight into a picker, so a set iterated
		// in map order would move the entries between runs.
		for i := 1; i < len(order); i++ {
			if order[i-1] >= order[i] {
				t.Fatalf("SkillInstallers() = %v, want it sorted by name", order)
			}
		}
	})
}
