# Consuming director from something that is not an agent

director's substrate is a CLI rather than an agent-facing protocol precisely so
that things which are not agent conversations are first-class: a status bar, a
monitoring daemon, a cron job, a TUI, a deploy script.

## Every command speaks JSON

`--json` on any command emits the same structs the human output renders, through
the same marshalling path — so the two cannot drift apart in meaning.

```sh
director status --json | jq -r '.[] | "\(.health)\t\(.title)"'
director tasks --json
director harnesses --json
director directors --json
```

## Exit codes

| Code | Meaning |
|---|---|
| 0 | ok |
| 1 | error |
| 2 | not found |
| 3 | harness unreachable |
| 4 | `director wait` timed out |

`3` is worth handling separately: it means a harness's server is not running,
which is a normal condition and not a reason to alarm anybody.

## Streaming changes

`director watch` emits a line whenever an engagement's health, lifecycle or
progress changes — and only then. A consumer never has to dedupe.

```sh
director watch --json | while read -r line; do
  printf '%s\n' "$line" | jq -r '"\(.engagement) \(.was // "new")->\(.health) \(.title)"'
done
```

Each transition carries `was`, the previous health, so a consumer can tell an
engagement that just went wrong from one that has been wrong for an hour — which
is the difference between notifying somebody and not.

Add `--initial` to receive the current state of everything before watching, so a
consumer that starts late is not looking at a blank screen.

## Blocking until something needs attention

`director wait` is the same feed with a stopping condition: it blocks and exits
as soon as an engagement reaches a health you care about, so a supervisor sleeps
rather than polls.

```sh
while director wait --json > /tmp/next; do
  jq -r '"\(.engagement) is \(.health)"' < /tmp/next
done
```

It wakes on `blocked,complete,abandoned,stalled` by default; `--until` narrows or
widens that, `--engagement` restricts it to particular engagements, and
`--timeout` gives up with exit code `4`, which is distinct from `1` so a script
can tell "nothing happened in time" from "the wait failed".

An engagement already in a matching health when `wait` starts matches
immediately — otherwise a caller that spawned, then waited, would block forever
on the state it was asking about.

## Multiple directors

State is per director, under one root. To watch everything on a machine, iterate:

```sh
for id in $(director directors --json | jq -r '.[].id'); do
  DIRECTOR_ID=$id director status --json
done
```

Two projects with their own `.director` roots are fully independent; point
`--config` or `DIRECTOR_ROOT` at each in turn.

## Writing another front-end

All the logic lives in the exported `director` package, and `cli/` is a thin
shell over it — flags, formatting, exit codes, nothing else. A TUI, an HTTP API
or an MCP server is a peer of the CLI rather than something that shells out to
it and parses its output.

```go
roots, _ := director.ResolveRoots("", cwd)
d, _ := director.Open(roots, "", director.SystemClock)
engagements, _ := d.Status(ctx)
```

The rule that keeps the line honest: **`cli/` may not contain a decision.** If a
behaviour would be identical for a TUI, it belongs in `director/`.
