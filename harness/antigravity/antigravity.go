// Package antigravity declares where Antigravity keeps skills, and nothing
// else.
//
// There is no adapter here: director cannot spawn, observe or stop an
// Antigravity conversation. That is not a reason to refuse to install for it.
// Being installable and being drivable are different capabilities — installing
// needs a pair of paths, driving needs a working protocol — and a harness that
// only ever has the first is still worth putting skills in front of, because a
// person directing from Antigravity by hand needs the same instructions as one
// director spawns into.
//
// So this registers a skills location and never appears in `director
// harnesses`, which lists what can be spawned into.
package antigravity

import (
	"os"
	"path/filepath"

	"github.com/rwmyers/agent-director/harness"
)

func init() { harness.RegisterSkillInstaller(Name, locations{}) }

// Name is the harness name, which is what `director setup --host` takes.
const Name = "antigravity"

type locations struct{}

// SkillLocations reports Antigravity's skills directories.
//
// They are not verified against a real installation — Antigravity was not
// present on the machine this was written on, and the paths come from its
// documented layout rather than from having been seen to work. Saying so is the
// point of the Verified flag: installing prints a warning, so anyone using this
// knows to check the skills were picked up and to place them by hand with
// `director skills --cat` if they were not.
func (locations) SkillLocations() (harness.SkillLocations, error) {
	home, _ := os.UserHomeDir()
	_, err := os.Stat(filepath.Join(home, ".gemini"))
	return harness.SkillLocations{
		Description: "Antigravity (paths unverified)",
		GlobalDir:   filepath.Join(home, ".gemini", "antigravity-cli", "skills"),
		ProjectDir:  filepath.Join(".agents", "skills"),
		Verified:    false,
		Present:     err == nil,
	}, nil
}
