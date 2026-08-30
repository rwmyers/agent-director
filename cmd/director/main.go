// Command director runs and coordinates independent agent conversations.
//
// This binary is director with the harness adapters it ships with, plus
// whatever director-harness-* plugins are on $PATH. An adapter someone else
// wrote in Go is added by building your own binary from a main exactly like
// this one, with their package imported alongside:
//
//	import (
//		"github.com/rwmyers/agent-director/cli"
//		_ "github.com/rwmyers/agent-director/harness/claudecode"
//		_ "github.com/example/director-aider"
//	)
//
// Both imports are for effect: an adapter package registers itself from init,
// and cli finds it through the registry in the harness package. Nothing here is
// privileged — the built-in adapters go through the same registration as any
// third-party one, so the public contract cannot rot without a compile error.
package main

import (
	"github.com/rwmyers/agent-director/cli"

	// Registers the built-in Claude Code adapter.
	_ "github.com/rwmyers/agent-director/harness/claudecode"
	// Registers the built-in herdr adapter.
	_ "github.com/rwmyers/agent-director/harness/herdr"
	// Registers every director-harness-* executable found on $PATH.
	_ "github.com/rwmyers/agent-director/harness/plugin"
)

func main() { cli.Main() }
