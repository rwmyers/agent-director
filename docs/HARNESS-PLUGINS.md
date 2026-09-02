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

#### `skills` — optional

Where director's skills belong for your harness. Declare it and `director
install` offers your harness as a target, with no change to director itself.

```json
{"api_version": 1, "name": "demo", "version": "0.1.0", "enforces": [],
 "skills": {"description": "Demo", "global_dir": "/home/you/.demo/skills",
            "project_dir": ".demo/skills", "verified": true, "present": true}}
```

- `description` — your harness's name as a person would write it. It is what the
  picker shows. Omitted, director falls back to your plugin's name.
- `global_dir` — an absolute directory covering every project on this machine.
  Expand `$HOME` yourself; director does not.
- `project_dir` — **relative to a repository root**, which director joins for
  you. An absolute path here would be answering a question you were not asked:
  which repository is being installed into is director's to decide.
- `verified` — whether these paths were confirmed against a real installation
  rather than read off documentation. `false` makes installing print a warning
  telling the user to check the skills were picked up. Say `false` if you are
  guessing: an unverified path that looks authoritative wastes an afternoon.
- `present` — whether your harness looks installed here. It only pre-selects a
  checkbox and is never a gate, so a plugin that cannot tell says `false` and its
  target is still offered.

Omit the whole block if your harness has no place for skills. director then
passes over you silently rather than offering a target it cannot write, which is
an ordinary answer — the built-in herdr adapter gives it, because herdr launches
somebody else's agent in a pane and that agent loads skills from its own harness.
Declaring only one of the two directories is fine too; the other scope is simply
not offered.

None of this requires the rest of the protocol. Being installable and being
drivable are separate capabilities, and a harness director cannot spawn into can
still say where its skills go.

#### `hosting` — optional

What your harness offers a director running **inside** it. Everything else here
describes a harness director dispatches work *to*; this is the other direction,
and it is a separate question. Declare it and director can sit in your harness
with no change to director itself.

```json
{"api_version": 1, "name": "demo", "version": "0.1.0", "enforces": [],
 "hosting": {"background": true, "wake": true,
             "detect_env": {"DEMO_INSIDE": "1"}, "ref_env": "DEMO_SESSION_ID"}}
```

- `background` — a director here can background a blocking `director wait` and
  still be reachable. Without it `director wait` refuses, and the director checks
  on its own turn instead.
- `wake` — something can reach into this director's **live** conversation and
  make it take a turn. That is your `send` verb, addressed at the ref below. Say
  `false` if your send starts a fresh headless process against a transcript: that
  produces a turn nobody is looking at, and a director told it can be woken hands
  back promising a watcher that does not exist.
- `detect_env` — the environment your harness sets inside one of its own
  conversations. Every entry must match: a value is compared exactly, and an
  empty value means the variable need only be set to something. director checks
  these itself, in process — it does not run you to ask — because location is
  worked out on every attach across every registered harness, including the ones
  nobody is using.
- `ref_env` — the variable holding this conversation's own ref, which is what a
  wake is addressed to. Unset yields an empty ref, which is still *inside*.

Both bits default to `false`, which is what omitting the block means. That is
the safe answer: a director wrongly told it can background freezes where nobody
can reach it, and one wrongly told it can be woken abandons its fleet politely.
Being wrong the conservative way only costs it a turn.

Omit `detect_env` and your harness is never detected — it can still be named
outright with `host = <name>` in `director.conf`. A person can also narrow what
you declared, per harness, and may never widen it:

```
[harness.demo]
hosts = background
```

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
