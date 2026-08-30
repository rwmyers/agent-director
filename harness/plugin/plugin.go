// Package plugin registers every director-harness-* executable on $PATH as a
// harness adapter.
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
	"path/filepath"
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
)

func init() { RegisterAll() }

// RegisterAll discovers plugins and registers one adapter each.
//
// Discovery reads directory entries and executes nothing, so this costs
// nothing at startup: a plugin is described lazily, once, the first time it is
// actually used.
func RegisterAll() {
	for _, found := range plugin.DiscoverIn(pathDirs()) {
		harness.Register(&Adapter{client: plugin.New(found)})
	}
}

func pathDirs() []string {
	return filepath.SplitList(os.Getenv("PATH"))
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
// down the whole command, because one broken executable on $PATH would then
// stop director working at all.
func (a *Adapter) describe() (*plugin.Description, error) {
	a.once.Do(func() {
		a.client.Stderr = os.Stderr
		a.description, a.describeErr = a.client.Describe(context.Background())
	})
	return a.description, a.describeErr
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
// executing every plugin on $PATH just to render a help table. A plugin that
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
