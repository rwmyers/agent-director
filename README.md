# agent-director

A chief-of-staff for agent conversations. It delegates work to independent,
resumable agent conversations across coding harnesses, and keeps track of what
each of them is doing.

```
$ director spawn --task investigate "Work out why TestFoo is flaky. Change nothing."
eng_c90de129  Work out why TestFoo is flaky  [investigate on claude-code]

$ director status
ID            HEALTH   LIFECYCLE  PROGRESS  SILENT  TITLE
eng_c90de129  quiet    working    reading   7m      Work out why TestFoo is flaky
eng_a4b17f20  blocked  idle       editing   2m      Fix the auth refactor
eng_88e3c1a5  stalled  working    planning  41m     Migrate the config parser

eng_a4b17f20 asked: May I force-push to feat/auth?
  answer with: director answer ask_578fc0d2 "..."
```

## Getting started

Two separate things, done by two different people at two different times.

**Setting up a project — a person, once.** `director init` writes a
`.director/` root into the repository: workflows, task types, prompts, the
harness default. Those are decisions about how *this* project delegates work,
they belong in version control, and no agent should be making them.

```sh
make install
cd your-project
director init                       # asks which harness, writes .director/ with starter workflows
                                    # safe to repeat: it uses the director already
                                    # registered here. `--new` adds a second one.
director install                    # put the director skills where your harness finds them
$EDITOR .director/workflows/*.conf  # make the task types yours
```

**Starting a directing session — an agent, every conversation.** `director
attach` works out whether to take over a director that is already running or
start a fresh one, and says why.

```sh
director attach
# attached to director dir_b3a3e06f (lead), workflow "goproj"
# because: lead has 1 engagement(s) waiting and nobody tending them
#
# You have inherited 1 engagement(s). Deal with anything waiting before starting new work:
# ID            HEALTH   LIFECYCLE  PROGRESS  SILENT  TITLE
# eng_9e6acceb  blocked  working    editing   14m     migrate the config parser
```

It attaches when exactly one director is unattended, prefers whoever has work
waiting, creates a new one when every director is already being driven by
another conversation — and **refuses to choose** when two unattended directors
both have work, because nothing distinguishes them and picking wrong means
quietly operating on somebody else's fleet.

Attaching records a claim that expires after 30 minutes, which is the only
signal available that another conversation is already here: two conversations
on one fleet would both spawn, both answer, and share a read cursor.

## What it is, and is not

It is a **substrate**, not an agent. The director — the thing that decides what
work exists, who does it, and what to do with the results — is whatever agent
conversation runs these commands. It is not tied to any particular model or
harness, and it could be an agent that agent-director itself spawned.

It is **a CLI**, deliberately, and there is no MCP server. A CLI is usable by
things that are not agent conversations at all: a monitoring daemon, a TUI, a
status bar, a cron job. Binding the substrate to an agent-facing protocol would
make every non-agent consumer a second-class citizen that had to go through a
harness to reach its own data. What actually delivers that independence is the
split between `director/` — where all the logic lives — and `cli/`, which is one
front-end over it. A future TUI or HTTP API is a peer of the CLI, not something
that shells out to it.

## The three status axes

The thing most worth understanding, because a director acts on the third.

| | Answers | Comes from |
|---|---|---|
| **Lifecycle** | is a process attached? | the harness, re-derived every call |
| **Progress** | where has the work got to? | the agent, via `director report` |
| **Health** | does this need me? | derived from both, plus the clock |

Keeping lifecycle and progress apart is what lets a workflow define its own
vocabulary — `triaging → reading → drafting → delivered` — without the core
losing its ability to answer "is this thing even alive".

**`done` does not mean finished.** It means no process is attached. An agent
whose terminal was closed is `done`, and so is one that finished perfectly. The
distinction lives on the progress axis, and health reports it as `complete` or
`abandoned` accordingly. Nothing else in the system will catch a task that
stopped halfway.

### Health, and why `quiet` is not `stalled`

An agent can stop reporting because it is wedged, or because it is busy. Those
look identical from the outside unless something can see whether it is actually
doing anything — so health also reads an activity signal the agent cannot affect
(for Claude Code, the modification time of its transcript).

- `quiet` — overdue a report, **but the harness shows activity**. Leave it alone.
- `stalled` — overdue **and** nothing is happening. `director nudge` it.

Collapse those two and the director either nags healthy agents or ignores dead
ones. `director nudge` means the director does the poking, which is the point:
nobody should have to watch a fleet by hand.

## Two labels, for two audiences

`--title` is director's description of the work, shown in `director status`.
`--name` is what the **harness** displays in its own interface — Claude Code's
prompt box, `/resume` picker and terminal title; a herdr pane header.

```sh
director spawn --task investigate --name auth-review --title "review the auth refactor" "..."
```

Both are optional. Title defaults to the first line of the brief; name defaults
to the title, shortened. Leaving the name empty is not neutral: harnesses derive
one from the working directory, so three engagements in one repository would all
be labelled identically to anyone looking at the harness rather than at director.

## Workflows and task types

Nothing about what a task *means* is built in. A **workflow** is a bundle of
task types, permission scopes, and placement defaults, and a director is bound
to one at `director init`. Task types are yours:

```ini
[permission.read-only]
allow = read, search

[task.investigate]
prompt      = ../prompts/investigate.md
permissions = read-only
progress    = orienting, reading, drafting, delivered
terminal    = delivered
report_on   = progress-change, 5m
require_final_report = true
```

A workflow is **data the director reads, not an engine**. It declares no
ordering, no transitions, and no graph, because the director is already an
engine — encoding a flow here would mean choosing one on your behalf.

`report_on` is the contract that makes silence interpretable, and it is read
three times: rendered into the agent's prompt at spawn (which is the only reason
the agent knows the callback exists), used by the core as the baseline for
lateness, and shown to the director so it knows what silence should look like.

### Permissions fail closed

Capabilities — `read search edit execute network push` — are the one closed set,
because an adapter has to enforce them and cannot map a word it has never heard
of. You compose scopes out of them freely.

An adapter that cannot hold the line a scope asks for **refuses to spawn**:

```
$ director spawn --task risky "..."
director: task "risky" requires permission scope "no-push": claude-code cannot
withhold "push" while granting "execute": publishing goes through the shell, so
denying it means denying commands entirely. Either allow push, or drop execute.
```

Running anyway with a warning would be worse than failing, because the warning
gets lost and the access does not.

## The agent↔director channel

A spawned agent gets an identity in its environment and talks back through the
same CLI. No daemon, no socket; it works whether or not a director is currently
looking.

```sh
director report --progress reading --message "3 of 7 files done"
ANSWER=$(director ask "May I force-push to feat/auth?" --wait)
```

Asking is what makes `blocked` honest — no harness reliably reports "waiting on
a human", so before this the state was guessed or missed.

The callback is available to **every** agent, including read-only ones with no
shell. It is not a capability a workflow grants; it is how the engagement exists
at all. An agent that could do the work but not report it would finish correctly
and be recorded as abandoned.

## Multiple directors

Directors are registered, persist across conversations (so a compacted or
restarted director re-attaches to its fleet), and each owns its own engagements
in its own state file. Configuration resolves from the nearest `.director`
directory upward, so different projects get entirely independent setups.

```sh
director where            # which root won, and what each layer contributed
director directors        # who is registered here
director retire <id>      # remove one; refuses while its agents are still running
```

A director's state file is the only thing mapping an engagement back to its
harness, so `retire` refuses while anything is alive rather than leaving agents
running that nothing can reach. `--stop` ends them first; `--force` orphans
them deliberately and says which.

## Commands

**Setup** — `init` · `attach` · `where` · `workflows` · `tasks` · `directors` ·
`retire` · `harnesses` · `skills` · `install`

**Directing** — `spawn` · `status` · `read` · `send` · `answer` · `nudge` ·
`stop` · `resume` · `note`

**Agent-side** — `report` · `ask`

**Consumers** — `watch`

Every command takes `--json`, marshalling the same structs the human output
renders — so a monitoring consumer and a director see the same thing. Exit codes
carry meaning: `0` ok, `1` error, `2` not found, `3` harness unreachable.

`director read` returns only what the *agent* said, only what is new since your
last read, and trims to a context budget. Your own brief is excluded — you wrote
it, and a composed brief would eat most of the budget before the agent had said
a word.

## Adding a harness

Two ways, and nothing downstream can tell them apart. The built-in adapters have
no privileges: they register through the same `harness.Register` as anything
else, so the public contract cannot rot without a compile error.

- **In Go** — implement `harness.Adapter` and build your own binary importing it
  alongside; see the doc comment on `cmd/director/main.go`.
- **As an executable** — name it `director-harness-<name>`, put it on `$PATH`,
  and speak JSON on stdin and stdout. Any language, no rebuild.
  See [docs/HARNESS-PLUGINS.md](docs/HARNESS-PLUGINS.md) and the working
  reference implementation in `examples/director-harness-demo/` — about a
  hundred lines of Python, written against nothing but that document.

Shipped adapters: **claude-code** (spawn, resume, transcript reads) and
**herdr** (agents in panes; screen-snapshot reads).

## Further reading

- [docs/HARNESS-PLUGINS.md](docs/HARNESS-PLUGINS.md) — the plugin protocol
- [docs/CONSUMERS.md](docs/CONSUMERS.md) — driving director from a UI, a
  monitor, or another front-end

## Development

```sh
make check
```

Runs `gofmt`, `go vet`, the tests, and `golangci-lint`. Linting needs
golangci-lint installed separately:

```sh
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
```
