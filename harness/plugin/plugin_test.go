package plugin

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/rwmyers/agent-director/harness"
	"github.com/rwmyers/agent-director/internal/plugin"
)

// adapterFor builds an adapter over an executable shell-script plugin, which is
// as much of a plugin as any of this needs: the contract is a process that
// answers describe, and the language it is written in is not director's
// business.
func adapterFor(t *testing.T, name, describeReply string) *Adapter {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, plugin.Prefix+name)
	script := "#!/bin/sh\ncat >/dev/null\n" + describeReply + "\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil { // #nosec G306 -- a test fixture that must be executable
		t.Fatal(err)
	}
	return &Adapter{client: plugin.New(plugin.Found{Name: name, Path: path})}
}

func TestAdapterSkillLocations(t *testing.T) {
	t.Parallel()

	t.Run("a plugin that declares a skills location becomes an install target", func(t *testing.T) {
		t.Parallel()
		// The whole point of the change: this plugin was never compiled into
		// director, and director has no list it has to appear on.
		adapter := adapterFor(t, "declares", `echo '{"api_version":1,"name":"declares","version":"0.1.0",
			"enforces":[],
			"skills":{"description":"Declares","global_dir":"/home/someone/.declares/skills",
			"project_dir":".declares/skills","verified":true,"present":true}}'`)

		locations, err := adapter.SkillLocations()
		if err != nil {
			t.Fatalf("SkillLocations() = %v, want the plugin's declaration", err)
		}
		want := harness.SkillLocations{
			Description: "Declares",
			GlobalDir:   "/home/someone/.declares/skills",
			ProjectDir:  ".declares/skills",
			Verified:    true,
			Present:     true,
		}
		if locations != want {
			t.Errorf("SkillLocations() = %+v, want %+v", locations, want)
		}
	})

	t.Run("a plugin that declares no skills block declines rather than fails", func(t *testing.T) {
		t.Parallel()
		// Declining must be distinguishable from breaking, because the
		// installer stays quiet about one and complains about the other.
		adapter := adapterFor(t, "silent", `echo '{"api_version":1,"name":"silent","version":"0.1.0","enforces":[]}'`)

		if _, err := adapter.SkillLocations(); !errors.Is(err, harness.ErrNoSkillLocations) {
			t.Errorf("SkillLocations() = %v, want ErrNoSkillLocations", err)
		}
	})

	t.Run("a plugin that cannot be described reports why", func(t *testing.T) {
		t.Parallel()
		// Not ErrNoSkillLocations: a plugin the user installed and cannot find
		// in the install list must not vanish silently.
		adapter := adapterFor(t, "broken", `echo '{"api_version":99,"name":"broken"}'`)

		_, err := adapter.SkillLocations()
		if err == nil || errors.Is(err, harness.ErrNoSkillLocations) {
			t.Errorf("SkillLocations() = %v, want the describe failure", err)
		}
	})

	t.Run("declaring only one scope leaves the other empty", func(t *testing.T) {
		t.Parallel()
		// A harness with no project-local configuration says so by omission,
		// and the installer offers only the scope that exists.
		adapter := adapterFor(t, "globalonly", `echo '{"api_version":1,"name":"globalonly","version":"0.1.0",
			"skills":{"description":"Global Only","global_dir":"/opt/globalonly/skills"}}'`)

		locations, err := adapter.SkillLocations()
		if err != nil {
			t.Fatalf("SkillLocations() = %v, want no error", err)
		}
		if locations.GlobalDir == "" || locations.ProjectDir != "" {
			t.Errorf("SkillLocations() = %+v, want a global directory and no project one", locations)
		}
	})
}

func TestAdapterHosting(t *testing.T) {
	t.Run("a plugin declares both bits and where to find itself", func(t *testing.T) {
		// The acceptance case: nothing in director names this harness, and it
		// still declares that a director sitting in it can be backgrounded and
		// woken, and how to tell that it is sitting in it.
		adapter := adapterFor(t, "boxes", `echo '{"api_version":1,"name":"boxes","version":"0.1.0",
			"enforces":[],
			"hosting":{"background":true,"wake":true,
			"detect_env":{"BOXES_INSIDE":"1"},"ref_env":"BOXES_BOX_ID"}}'`)

		want := harness.Hosting{Background: true, Wake: true}
		if got := adapter.Hosts(); got != want {
			t.Errorf("Hosts() = %v, want %v", got, want)
		}

		t.Setenv("BOXES_INSIDE", "")
		t.Setenv("BOXES_BOX_ID", "")
		if ref, inside := adapter.Locate(); inside {
			t.Errorf("Locate() outside the harness = %q, %v, want not inside", ref, inside)
		}

		t.Setenv("BOXES_INSIDE", "1")
		t.Setenv("BOXES_BOX_ID", "box-7")
		ref, inside := adapter.Locate()
		if !inside || ref != "box-7" {
			t.Errorf("Locate() = %q, %v, want \"box-7\", true", ref, inside)
		}

		// A variable set to something else is not this harness.
		t.Setenv("BOXES_INSIDE", "0")
		if ref, inside := adapter.Locate(); inside {
			t.Errorf("Locate() with a mismatched value = %q, %v, want not inside", ref, inside)
		}
	})

	t.Run("a plugin may declare one bit and no detection at all", func(t *testing.T) {
		// Background without wake is a real combination, and a harness that
		// cannot recognise its own conversations is still nameable in
		// configuration — it just is not detected.
		adapter := adapterFor(t, "quiet", `echo '{"api_version":1,"name":"quiet","version":"0.1.0",
			"hosting":{"background":true}}'`)

		if got := adapter.Hosts(); got != (harness.Hosting{Background: true}) {
			t.Errorf("Hosts() = %v, want background only", got)
		}
		if ref, inside := adapter.Locate(); inside {
			t.Errorf("Locate() with no detect_env = %q, %v, want not inside", ref, inside)
		}
	})

	t.Run("a plugin that declares nothing offers nothing", func(t *testing.T) {
		adapter := adapterFor(t, "silenthost", `echo '{"api_version":1,"name":"silenthost","version":"0.1.0"}'`)
		if got := adapter.Hosts(); got != harness.UnknownHosting() {
			t.Errorf("Hosts() = %v, want nothing", got)
		}
	})

	t.Run("a plugin that cannot be described offers nothing rather than failing", func(t *testing.T) {
		// Detection is ambient. A broken plugin must not be able to stop a
		// director working out where it is sitting.
		adapter := adapterFor(t, "brokenhost", `echo '{"api_version":99,"name":"brokenhost"}'`)
		if got := adapter.Hosts(); got != harness.UnknownHosting() {
			t.Errorf("Hosts() = %v, want nothing", got)
		}
		if _, inside := adapter.Locate(); inside {
			t.Error("Locate() on a broken plugin reported inside")
		}
	})
}
