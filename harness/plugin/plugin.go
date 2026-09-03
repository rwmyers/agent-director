// Package plugin registers every director-harness-* executable director can
// find — on $PATH, or in the directory holding the running director binary — as
// a harness adapter.
//
// An external plugin and a built-in adapter land at the same registry and
// nothing downstream can tell them apart. The Go interface is the contract;
// exec plus JSON is one transport into it. That is what makes the interface
// real rather than aspirational — a first-party adapter with access the public
// contract does not expose would let the contract rot unnoticed.
package plugin

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/rwmyers/agent-director/harness"
	"github.com/rwmyers/agent-director/internal/plugin"
)

// Verbs a harness plugin may implement. Only describe is mandatory; a plugin
// that cannot read or resume simply fails those verbs, and declares as much in
// describe so director never offers what is not there.
const (
	VerbSpawn = "spawn"
	VerbSend  = "send"
	VerbGet   = "get"
	VerbList  = "list"
	VerbStop  = "stop"
	VerbRead  = "read"
	// VerbDispose reclaims the slot a conversation occupies in the harness's
	// own interface. Only reached when describe declared "disposes".
	VerbDispose = "dispose"
)

func init() { RegisterAll() }

// RegisterAll discovers plugins and registers one adapter each.
//
// Discovery reads directory entries and executes nothing, so this costs
// nothing at startup: a plugin is described lazily, once, the first time it is
// actually used.
//
// Where to look is not this package's business. It used to split $PATH itself,
// which meant two packages held an opinion about where plugins live and only
// one of them got fixed when the answer changed. internal/plugin owns that
// answer now; this calls Discover and registers what comes back.
func RegisterAll() {
	for _, found := range plugin.Discover() {
		harness.Register(&Adapter{client: plugin.New(found)})
	}
}

// Adapter drives one external plugin.
type Adapter struct {
	client *plugin.Client

	once        sync.Once
	description *plugin.Description
	describeErr error
}

// Name is the name discovery derived from the filename. It is what director
// calls the plugin even if describe reports something else, since that is the
// name a workflow pins and a config section uses.
func (a *Adapter) Name() string { return a.client.Name }

// describe runs the mandatory handshake once.
//
// A failure here costs the user this plugin and says why; it must never take
// down the whole command, because one broken executable in a directory
// discovery searches would then stop director working at all.
func (a *Adapter) describe() (*plugin.Description, error) {
	a.once.Do(func() {
		a.client.Stderr = os.Stderr
		a.description, a.describeErr = a.client.Describe(context.Background())
	})
	return a.description, a.describeErr
}

// SkillLocations reports where the plugin says director's skills belong.
//
// Present unconditionally, like Read, because the optional-interface probe
// happens before describe has run; a plugin that declares no skills block
// returns harness.ErrNoSkillLocations, which the installer reads as a decline
// rather than as a fault. Asking costs one describe per plugin, which is why
// only `director install` asks — the rest of director never needs to know.
func (a *Adapter) SkillLocations() (harness.SkillLocations, error) {
	description, err := a.describe()
	if err != nil {
		return harness.SkillLocations{}, err
	}
	if description.Skills == nil {
		return harness.SkillLocations{}, harness.ErrNoSkillLocations
	}
	return harness.SkillLocations{
		Description: description.Skills.Description,
		GlobalDir:   description.Skills.GlobalDir,
		ProjectDir:  description.Skills.ProjectDir,
		Verified:    description.Skills.Verified,
		Present:     description.Skills.Present,
	}, nil
}

// Hosts reports what the plugin declares it offers a director running inside it.
//
// A plugin that declares no hosting block, or that cannot be described at all,
// offers nothing. That is the conservative direction and it is the only safe
// one here: a plugin wrongly credited with backgrounding freezes a director,
// and one wrongly credited with wake has it hand back promising a watcher that
// does not exist.
func (a *Adapter) Hosts() harness.Hosting {
	description, err := a.describe()
	if err != nil || description.Hosting == nil {
		return harness.UnknownHosting()
	}
	return harness.Hosting{
		Background: description.Hosting.Background,
		Wake:       description.Hosting.Wake,
	}
}

// Locate reports whether this director is running inside the plugin's harness,
// by matching the environment the plugin said its own conversations carry.
//
// Present unconditionally, like Read, because the optional-interface probe
// happens before describe has run. The match is done here rather than by asking
// the plugin, so the answer costs the one describe every other method already
// pays for rather than a second process per attach — and a plugin that is
// broken, missing or slow simply is not detected, which is what ambient
// detection has to do.
func (a *Adapter) Locate() (string, bool) {
	description, err := a.describe()
	if err != nil || description.Hosting == nil || len(description.Hosting.DetectEnv) == 0 {
		return "", false
	}
	for key, want := range description.Hosting.DetectEnv {
		got := os.Getenv(key)
		if got == "" || (want != "" && got != want) {
			return "", false
		}
	}
	var ref string
	if description.Hosting.RefEnv != "" {
		ref = os.Getenv(description.Hosting.RefEnv)
	}
	return ref, true
}

// Disposes reports whether the plugin says its harness has a slot to reclaim.
//
// A plugin that declares nothing, or that cannot be described at all, disposes
// of nothing — which makes every removal behave exactly as it did before this
// existed. That is the conservative direction: a slot left behind is untidy,
// while a slot closed on a harness that never claimed to have one is a verb
// called on a plugin that does not implement it, on a path where the record has
// already gone.
func (a *Adapter) Disposes() bool {
	description, err := a.describe()
	return err == nil && description.Disposes
}

// Dispose asks the plugin to reclaim a conversation's slot.
//
// Present unconditionally, like Read, because the optional-interface probe
// happens before describe has run. What gates it is Disposes above, which is
// what harness.DisposerFor asks: a plugin that did not declare the capability
// is never called here at all.
func (a *Adapter) Dispose(ctx context.Context, req harness.DisposeRequest) error {
	if _, err := a.describe(); err != nil {
		return err
	}
	return a.client.Call(ctx, VerbDispose, req, nil)
}

// Enforceable reports what the plugin says it can control.
func (a *Adapter) Enforceable() []harness.Capability {
	description, err := a.describe()
	if err != nil {
		return nil
	}
	var caps []harness.Capability
	for _, raw := range description.Enforces {
		if capability, err := harness.ParseCapability(raw); err == nil {
			caps = append(caps, capability)
		}
	}
	return caps
}

// Permits applies the fail-closed rule against what the plugin declared.
//
// The check is done here rather than delegated to the plugin because a plugin
// that simply forgot to implement it would otherwise pass everything. A
// declaration is a promise director can check; an unimplemented verb is not.
func (a *Adapter) Permits(allow []harness.Capability) error {
	if _, err := a.describe(); err != nil {
		return err
	}
	enforceable := map[harness.Capability]bool{}
	for _, capability := range a.Enforceable() {
		enforceable[capability] = true
	}
	var cannot []harness.Capability
	for _, capability := range harness.Withheld(allow) {
		if !enforceable[capability] {
			cannot = append(cannot, capability)
		}
	}
	if len(cannot) > 0 {
		return fmt.Errorf("%s does not declare that it can withhold %s; refusing to spawn with more access than asked for",
			a.Name(), harness.JoinCapabilities(cannot))
	}
	return nil
}

// Spawn starts a conversation through the plugin.
func (a *Adapter) Spawn(ctx context.Context, req harness.SpawnRequest) (harness.SpawnResult, error) {
	if _, err := a.describe(); err != nil {
		return harness.SpawnResult{}, err
	}
	var result harness.SpawnResult
	if err := a.client.Call(ctx, VerbSpawn, req, &result); err != nil {
		return harness.SpawnResult{}, err
	}
	return result, nil
}

// Send pushes text into a running conversation.
func (a *Adapter) Send(ctx context.Context, req harness.SendRequest) error {
	if _, err := a.describe(); err != nil {
		return err
	}
	return a.client.Call(ctx, VerbSend, req, nil)
}

// Get observes one engagement.
func (a *Adapter) Get(ctx context.Context, ref string) (harness.Observation, error) {
	if _, err := a.describe(); err != nil {
		return harness.Observation{}, err
	}
	var observation harness.Observation
	if err := a.client.Call(ctx, VerbGet, map[string]string{"ref": ref}, &observation); err != nil {
		return harness.Observation{}, err
	}
	observation.Ref = ref
	return observation, nil
}

// List observes everything the plugin can see.
func (a *Adapter) List(ctx context.Context, filter harness.Filter) ([]harness.Observation, error) {
	if _, err := a.describe(); err != nil {
		return nil, err
	}
	var result struct {
		Engagements []harness.Observation `json:"engagements"`
	}
	if err := a.client.Call(ctx, VerbList, filter, &result); err != nil {
		return nil, err
	}
	return result.Engagements, nil
}

// Stop stops an engagement.
func (a *Adapter) Stop(ctx context.Context, req harness.StopRequest) error {
	if _, err := a.describe(); err != nil {
		return err
	}
	return a.client.Call(ctx, VerbStop, req, nil)
}

// Read returns an engagement's output.
//
// Present unconditionally because the optional-interface probe happens before
// describe has run, and running describe during a capability probe would mean
// executing every plugin discovery found just to render a help table. A plugin that
// cannot read fails the verb, which the core surfaces as an error rather than
// as an empty result.
func (a *Adapter) Read(ctx context.Context, req harness.ReadRequest) (harness.ReadResult, error) {
	if _, err := a.describe(); err != nil {
		return harness.ReadResult{}, err
	}
	var result harness.ReadResult
	if err := a.client.Call(ctx, VerbRead, req, &result); err != nil {
		return harness.ReadResult{}, err
	}
	return result, nil
}
