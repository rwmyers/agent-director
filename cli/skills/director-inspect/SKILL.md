---
name: director-inspect
description: Read and interpret engagement results as a director - what director status and director read actually mean, and the traps in each. Use when checking on engagements, reading what an agent produced, deciding whether work is finished, or when an engagement looks stalled, blocked, abandoned or unknown.
---

# Interpreting engagements

## The three axes

`director status` gives you three different answers to three different
questions. Confusing them is where directors go wrong.

- **LIFECYCLE** — is a process attached? Comes from the harness.
- **PROGRESS** — where has the work got to? Comes from the agent, in the
  vocabulary its task type declared.
- **HEALTH** — does this need me? Derived from the other two plus elapsed time.

**Act on HEALTH.** The others are inputs to it.

## The trap: `done` does not mean finished

`done` means no process is attached. That is all.

An agent whose terminal was closed is `done`. An agent that crashed is `done`.
An agent that completed the work perfectly is also `done`. The lifecycle cannot
tell them apart, and neither can you from that column alone.

The distinction is on the progress axis, and health reports it:

- **`complete`** — the process ended having reached its task's terminal
  progress value. This is finished.
- **`abandoned`** — the process ended short of it. Something stopped halfway.

**Never tell anyone a piece of work is done because the lifecycle says `done`.**
Read the transcript, or ask the agent, first.

## What each health verdict asks of you

| Verdict | What it means | Do |
|---|---|---|
| `ok` | meeting its reporting contract | nothing |
| `quiet` | overdue a report, but the harness shows it working | nothing — it is busy, not stuck |
| `stalled` | overdue **and** nothing is happening | `director nudge <id>` |
| `blocked` | it asked a question | `director status asks <ask>`, then `director answer <ask> "<text>"` |
| `abandoned` | ended without finishing | read it; find out what happened |
| `complete` | ended having finished | collect the result |
| `unknown` | not enough signal | wait a turn, then treat as stalled |

`quiet` and `stalled` look identical from the outside — both are silent. The
difference is that `quiet` has an activity signal from the harness and
`stalled` does not. Trust it: nudging a `quiet` agent interrupts work that is
going fine.

If an engagement is `stalled` and you have already nudged it once, do not nudge
again. Escalate to the person: something is wrong that you cannot fix by asking.

## Where the work is

`materials` is the engagement's own statement of where its work landed: a
branch, a pull request, a path, a document. It reports it with `director report
--materials`, and each report **replaces** the set, so what you see is the
current one rather than everything ever mentioned.

Two things follow. An empty `materials` means the agent has not said — it does
not mean the agent has produced nothing, and it is not something you fix by
scraping paths out of its prose into `director note`. And a material that
disappeared between two turns was retracted by the agent, not lost by you.

## Putting a verdict in one cell

A director's status table has one column for all three axes. Compose it as
health with progress in parentheses — `ok (drafting)`, `abandoned (editing)` —
and leave lifecycle out entirely. Health is the verdict and belongs first.
Progress says how far the work got and is misleading alone. Lifecycle's `done`
is the trap above, and a person who sees it in a column headed "status" will
read it as "finished".

`complete (delivered)` is still not a claim that the work is right. It says the
process ended having reached the task's terminal value. Read the engagement
before you repeat it.

## `unknown` is not an error

`unknown` means the harness answered and does not know. Wait a turn.

If a **command** fails instead — exit code 3, or an error naming a socket or a
binary — the harness did not answer at all. Say so plainly and stop reasoning
about the fleet. Do not describe agents you cannot actually see.

## Reading without drowning

`director read <id>` returns only what is new since your last read. Read
repeatedly; do not ask for everything at once.

- The default returns only what the *agent* said. Your own brief and follow-ups
  are excluded, because you wrote them and a composed brief would eat most of
  the budget before the agent had said a word. `--include user,assistant` shows
  both.
- Tool calls and thinking are excluded and are larger still. `--include
  tool_use` only to diagnose what an agent actually ran, never to "see what
  happened".
- If the output starts with `[N earlier turns omitted]`, you are seeing the
  most recent material and the rest is still there behind the cursor.
- If it says the harness returns a **screen snapshot**, scrollback is finite.
  Earlier output may be *gone* rather than absent. Do not conclude the agent
  said nothing.

Summarise as you read. Do not pull a transcript into your context and then
reason over it — that is how a director ends up as expensive as the work.

## What `status --json` carries that the table does not

Per engagement:

- `note` — whatever *you* last wrote with `director note`. Only the most recent
  one; writing a note replaces it.
- `materials` — the full list of where the engagement said its work can be
  found, in the order it named them. The table shows the first one clipped plus
  `+N`, because it is on screen every turn; this is the whole thing, and the
  place to get a URL you can click. Absent entirely when the engagement has
  reported none — an engagement that has produced nothing looks like one that
  has produced nothing.
- `open_asks` — the identifiers of the questions it is waiting on. Identifiers
  only. There is **no** `pending_ask` object and no question text anywhere in
  `status --json`; see *Answering questions* below.
- `detail` — harness plumbing: `pid`, `log`, `prompt`, `transcript`. It is not
  the engagement's output; it is filled in identically for an agent that
  delivered and one that died in its first second. Never put it in a Materials
  column.

## Answering questions

`blocked` means an agent asked something it correctly refused to guess at.

`director status` does **not** print the question. It lists the open ones by
identifier under the table, with the commands that act on them:

```
1 open question, text not shown:
  ask_578fc0d2  eng_a4b17f20  waiting 4m

  read:    director status asks ask_578fc0d2
  answer:  director answer ask_578fc0d2 "..."
```

`director status asks <id> [<id>...]` prints the full text of the questions you
name, and works under `--json` too. `director status asks` with nothing named
lists the open ones by identifier and prints no text — it is the index, not a
dump.

**Fetch the text of the one you are about to answer, not all of them.** That
split is the point: status is the command you run every turn, and five agents
blocked on large plans would otherwise reproduce all five plans on every one of
those turns until somebody answered. Pulling them all back by hand recreates
exactly that.

Answer decisively. "Do whatever you think is best" hands the decision back to
an agent that already told you it should not make it. If the choice is genuinely
not yours either, escalate to the person — and say which engagement is waiting
and on what, because they cannot see the fleet.
