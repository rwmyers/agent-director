# Writing a harness plugin

director drives agent tools through adapters. Two ways to add one:

- **A Go adapter** — implement `harness.Adapter` and build your own binary with
  it imported for effect. See the doc comment on `cmd/director/main.go`.
- **An executable plugin** — any language, no rebuild. This document.

There is no difference downstream. Both land at the same registry, and a
built-in adapter has no access the public contract does not expose — if it did,
the contract would rot unnoticed until somebody outside this repository tried
to use it.

## Discovery

Name your executable `director-harness-<name>` and put it on `$PATH`. Anything
matching is loadable; there is no allowlist. Where two directories hold the same
plugin the earlier wins, as a shell would resolve it. A built-in adapter of the
same name wins over a plugin, so an experimental copy cannot silently displace
the real one.

Discovery reads directory entries and executes nothing, so `director --help`
never runs your plugin.

## Protocol

One process per call. The verb is the only argument. A JSON request arrives on
stdin; a JSON result goes to stdout.

```
$ director-harness-demo get
{"api_version":1,"config":{"socket":"/tmp/x"},"params":{"ref":"p7"}}
```

- **exit 0**, result object on stdout
- **non-zero exit**, `{"error": "..."}` on stdout

An `{"error": ...}` reply is believed even on a zero exit, because a plugin that
says what went wrong is more useful than an exit status. Anything on stderr is
logged, tagged with your plugin's name, and never parsed — write progress and
debugging there freely.

**director owns the timeout.** A wedged plugin cannot wedge director, including
one that leaves a child process holding its output pipe.

`config` is your `[harness.<name>]` section from `director.conf`, passed through
verbatim. director never looks inside it: validating it would mean knowing about
every harness that will ever exist.

## Verbs

### `describe` — mandatory, called first

Nothing else runs until this succeeds, so a plugin speaking the wrong version
fails with *that* complaint rather than with whatever another verb happens to do.

```json
{"api_version": 1, "name": "demo", "version": "0.1.0", "enforces": ["read", "search"]}
```

`enforces` lists the capabilities you can genuinely restrict. **Declare only
what you can actually hold.** director refuses to spawn under a scope that
withholds a capability you have not declared, rather than running the agent with
more access than the workflow asked for. Over-declaring defeats the entire
permission system, and the failure is silent.

Declaring `[]` is a perfectly good answer. It means your harness runs
unrestricted agents, and director will only use it for unrestricted scopes.

### `spawn`

Start a conversation and **return as soon as it is addressable** — never when
the agent has answered. A spawn that blocked for a first turn would stall the
director's own turn, and dispatching several things at once is the point.

Request `params` carries `id`, `dir`, `title`, `name`, `prompt`, `allow`, `env`.

```json
{"ref": "p7", "detail": {"pid": "1234"}}
```

`ref` is your own handle. It need not be the `id` director minted — Claude Code
insists on a UUID, herdr assigns a pane id after the fact, and both work. `ref`
is opaque to everything outside your plugin.

**`env` must reach the agent's process.** It carries the engagement's identity
and callback token. Without it the agent cannot report progress or ask
questions, and every engagement you host will look abandoned.

### `get` and `list`

```json
{"ref": "p7", "found": true, "lifecycle": "working",
 "last_activity_at": "2026-08-29T12:00:00Z", "detail": {}}
```

`lifecycle` is one of `starting`, `working`, `idle`, `blocked`, `done`,
`unknown`. Anything unrecognised becomes `unknown`.

**`found: false` is a result, not an error.** Panes get closed and transcripts
get deleted; director must be able to tell that from your plugin being broken.

**`last_activity_at` is the most valuable field you can supply.** It is when the
harness last saw the agent actually *do* something, and because it does not
depend on the agent cooperating, it is what lets director tell an agent that is
busy from one that is wedged. A file modification time is usually enough. It is
read on every status call, so make it cheap — one stat, or a field you already
have. Leave it empty if you truly cannot supply it; director then degrades that
engagement toward "stalled" rather than assuming it is fine.

`list` returns `{"engagements": [...]}`.

### `send`, `stop`

`send` takes `ref` and `text`. `stop` takes `ref` and `mode` (`end` or
`interrupt`); both must leave the conversation resumable and neither may destroy
a transcript. Refuse a mode you cannot honour rather than implementing a kill
under a gentler name — a director told it interrupted a turn, when in fact the
process is gone, will draw the wrong conclusion about what it can resume.

### `read` — optional

```json
{"kind": "turns", "cursor": "61", "total": 102, "complete": true,
 "turns": [{"seq": "42", "role": "assistant", "at": "...", "text": "..."}]}
```

`kind` is `turns` (append-only, cursor meaningful) or `screen` (a terminal
snapshot: no cursor, `complete: false`, and scrollback is finite). Setting
`complete: false` is what stops a director concluding an agent said nothing when
its output merely scrolled away.

The cursor is opaque — a line ordinal, a timestamp, an event id, whatever
resumes cheaply for you.

**If you cannot read, fail the verb.** Do not return an empty result: that reads
as "the agent said nothing", which is a different and much more dangerous claim
than "I cannot see what it said".

## A worked example

`examples/director-harness-demo/director-harness-demo` is a complete
implementation in about a hundred lines of Python. It was written against this
document, which is the test of whether the interface is an interface.

```sh
cp examples/director-harness-demo/director-harness-demo ~/.local/bin/
director harnesses
```
