package antigravity

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rwmyers/agent-director/harness"
)

func TestSkillLocations(t *testing.T) {
	t.Parallel()
	// These paths moved here out of cli/install.go. They are the same paths,
	// and this is what says so.
	home, _ := os.UserHomeDir()

	locations, err := locations{}.SkillLocations()
	if err != nil {
		t.Fatalf("SkillLocations() = %v, want no error", err)
	}
	if want := filepath.Join(home, ".gemini", "antigravity-cli", "skills"); locations.GlobalDir != want {
		t.Errorf("GlobalDir = %q, want %q", locations.GlobalDir, want)
	}
	if want := filepath.Join(".agents", "skills"); locations.ProjectDir != want {
		t.Errorf("ProjectDir = %q, want %q", locations.ProjectDir, want)
	}
	if locations.Verified {
		t.Error("Verified = true, want false: these paths have never been checked against a real installation")
	}
	if locations.Description != "Antigravity (paths unverified)" {
		t.Errorf("Description = %q, want it to carry the warning into the picker", locations.Description)
	}
}

func TestRegistration(t *testing.T) {
	t.Parallel()

	t.Run("it is offered as an install target", func(t *testing.T) {
		t.Parallel()
		var found bool
		for _, candidate := range harness.SkillInstallers() {
			if candidate.Name == Name {
				found = true
			}
		}
		if !found {
			t.Errorf("SkillInstallers() does not include %q, want it installable", Name)
		}
	})

	t.Run("it is not offered as a harness to spawn into", func(t *testing.T) {
		t.Parallel()
		// director cannot drive Antigravity. Appearing in `director harnesses`
		// would promise a spawn that cannot happen.
		if _, err := harness.Lookup(Name); err == nil {
			t.Errorf("Lookup(%q) succeeded, want no adapter", Name)
		}
	})
}
