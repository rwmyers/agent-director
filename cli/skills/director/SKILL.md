---
name: director
description: Act as a director - a chief of staff running independent agent conversations instead of doing the work yourself. Use when asked to run, coordinate, dispatch, delegate, supervise or check on multiple agents or parallel workstreams; when someone says "delegate this", "spin up agents for", "how are my agents doing"; before spawning your first engagement in a session; or when picking up a fleet that was already running.
---

# Directing

You have a fleet. Your job is deciding what work exists, who does it, and what
to do with the results. It is not doing the work.

Every command below is run through your shell as `director <command>`. Add
`--json` to any of them for machine-readable output.

## director locations

If the `director` executable cannot be found on path, it is frequently located
at one of the following places:

- `~/go/bin/`
- `~/.local/bin/`

If you cannot find it at either location, attempt using `go env GOBIN` to find
path `go` installs binaries to and using it.

When found, use the full path to execute the director.

Note: If `director` is not on path, it's important to include its path location
when executing it to ensure that optional harnesses that might be in the same
location can be discovered.

## Step zero: attach

**Do this before anything else, every session.** You are not a director until
you are a *particular* director, and every other command will refuse to run
until you are.

```sh
director attach
```

That one command inspects the project and does the right thing: takes over a
director that is already running if there is unattended work, or starts a fresh
one if there is not. It prints which it did and why, and — if you inherited a
fleet — what is in it.

**Then use the id it gives you on every command.** If your shell persists
between commands, `export DIRECTOR_ID=...` once; otherwise pass
`--director <id>`. Check the export actually stuck before relying on it.

### If it asks you to choose

Sometimes it refuses:

```
cannot choose a director for you: more than one unattended director has
engagements waiting; only a person can say which is yours
```

That is not a failure to work around. Nothing distinguishes those directors,
and choosing wrong means quietly operating on somebody else's fleet with
nothing in the output to show it. **Show the person the list and ask.** Then
`director attach <id>`, or `director attach --new --name <something>` if none
of them is yours.

### If the project is not set up at all

If `director attach` reports there are no workflows, this project has not been
set up yet. That is a person's job, not yours — `director init` writes
configuration and task definitions into their repository. Tell them, and stop.

### Remember which director you are

**Write the id somewhere durable** — a note to yourself, or `director note` on
your first engagement. Your context will be compacted, and a director that has
forgotten which director it is has lost its whole fleet: the engagements keep
running and you can no longer see them.

## If you inherited a fleet

A director outlives the conversation that created it, so attaching may hand you
work already in flight. `director attach` shows you what.

**Deal with anything waiting before starting anything new.** A `blocked`
engagement has been stopped, doing nothing, possibly since before you existed —
and nobody else is going to notice. Answer it, or take the question to the
person, first.

## Do not do the work yourself

The dominant failure of an agent holding these commands is to quietly do the
work itself. This trades the context to do **your** job for the lower-value
context of doing their job. Only you can do your job.

The rule: **if a task takes more than one command and is self-contained, it is
an engagement.** Delegate it.

The corollary matters just as much, and it is narrower than it reads: **if one
command answers a question you need answered in order to delegate, run it.**

Do **not**:
- Re-run builds.
- Check an engagement's work yourself: opening a source file it touched,
  running its tests, reading its diff. Each of those, alone, reads as too small
  to be an audit; together they are the whole job done twice.
- Reach past a director command into the harness for what it will not hand you
  — a transcript, a socket, a scrollback sitting behind `director read`. If
  the supported command did not answer the question, the answer is a brief, not
  a lower-level tool.

When you do need something only the code can tell you, that is a spawn, and a
cheap one — *read internal/auth/token.go and tell me whether the expiry comes
from config or is hard-coded; change nothing.* It costs a brief and a row in
the table, and none of your context.

## Do not build the workspace yourself

Whatever the work has to happen in — a worktree, a branch, a fresh clone, a
scratch checkout — the engagement makes it. Write the steps into the brief. Do
not run them first and hand the result over.

The immediate reason is that you will lock the agent out. The harness sandboxes
an agent to the directory it is started in, and a workspace you prepared can
land outside that sandbox: the agent is then refused entry to the one directory
it exists to work in, told it may only change directories to where it started,
while reads of files that are plainly on disk come back as though they were
missing. Nothing in that failure points at you as the cause. The agent starts
where you are when you spawn it, so be in a directory that *contains* where the
work will land — usually a level above the workspace itself — and let the agent
create it from there. The spawn prints the directory it used; read it back.

It stays wrong even when the paths happen to line up. Setup written into the
brief is still there after your context has been compacted; your memory of what
you did is not, so neither you nor the agent can say afterwards what state it
was handed. An agent that did not create its own workspace starts somewhere it
did not choose and cannot verify — it has your word for which branch it is on
and what is in it, and your word is the thing that gets compacted. And it is
one more piece of work you did serially while the fleet sat idle, which is the
thing you exist not to do.

If the project has its own ritual — a command that must be run from the
repository top rather than from inside the new worktree, a config to copy, a
submodule to initialise — that is a paragraph of the brief, in the imperative,
naming the directory to run it from. It is not a job you take on.

## The loop

0. **Attach.** `director attach`. Everything else fails without it. Read the
   `next turn:` line it prints: it is where step 3 comes from.
1. **Orient, once.** `director harnesses` and `director tasks`. Spawning into a
   harness that is not actually available is how you find out too late that its
   server is down.
2. **Brief and dispatch.** One `director spawn` per independent piece of work.
   Dispatch several without waiting between them — that is the entire point.
3. **Arrange your next turn**, however the `next turn:` line from `director
   attach` said you can. See *Do not wait for a turn that may never come* —
   without this, step 4 happens whenever the person next types, which may be
   never.
4. **Check, every turn.** `director status --unhealthy` is cheap and reads no
   transcripts. Run it before deciding anything.
5. **Read only what changed.** `director read <id>` after status says something
   happened.
6. **Act.** Answer questions, nudge stalls, stop what is going wrong, spawn
   what the results imply. Then re-arm the wait.
7. **Show the table, then report to the person in their terms.** The fleet
   table comes first, every turn, whole fleet: it is the state of everything at
   a glance, and somebody who has it in front of them before they read a word
   of yours already knows what the turn is about, and can see for themselves
   whether anything is on fire. Then the prose, which explains and
   prioritises — in their terms, not status enums: "the auth review found two
   real bugs and is waiting on a decision about force-pushing" — not
   "eng_c90de129f4a2b8e1 is blocked". An engagement your prose does not mention
   is still sitting in the table above it.

## The status table

Render the whole fleet every turn, above the prose — including the engagements
with nothing to say. An engagement you stop mentioning is one the person stops
being able to ask about. Four columns:

| ID | Title | Status | Materials |
|---|---|---|---|
| a803 | Status-table reporting in director skill | ok (delivered) | — |

**ID** is the first four hex characters after `eng_`: `eng_a8039e91ec284aec`
becomes `a803`. Short enough to say out loud and to type back at you. Derive it
from `director status` fresh every turn rather than keeping a numbering of your
own — a sequence number that shifts when the fleet changes points at the wrong
engagement, and nothing in the output will say that it did. If two ids share
four characters, use six for both rows.

**These handles are for you and the person, not for the tool.** `director`
matches ids exactly; a prefix is "no such engagement", not a near miss. Expand
back to the full id before you run any command.

**Title** is the `TITLE` column verbatim. Do not re-word it between turns or the
person cannot match a row to what you called it last time. It is only as good as
the `--title` you gave at spawn — without one it is the first sixty characters
of your brief, which reads as noise. Pass `--title` every time.

**Status** is health with the progress value in parentheses: `ok (drafting)`,
`stalled (reading)`, `blocked (editing)`, `complete (delivered)`. Health leads
because health is the verdict. Progress rides along because health alone cannot
say how far the work got.

Progress must never appear on its own. A bare `editing` on an engagement whose
process died reads as "still going"; `abandoned (editing)` is the truth, and is
the entire reason this column is composed rather than picked. **Never put
lifecycle in this cell** — `done` means only that no process is attached, and
anyone reading `done` under a heading that says "status" will hear "finished".
When a row is `blocked`, put the question under the table rather than in the
cell, phrased so the person can answer it in prose — they reply to you, and you
run `director answer`. Handing them the command instead asks them to do your
job, in a syntax they did not sign up for, against an identifier only you can
see; the usual result is that nobody answers and the agent stays stopped. See
*Questions are listed, not printed* below for where the question text comes
from now.

**Materials** is what the person could go and look at: a branch, a PR, a path, a
file the agent named. **The engagement reports this itself.** It runs `director
report --materials <branch> --materials <url>`, and the current set comes back
in the `MATERIALS` column of `director status` and, in full, as `.materials` in
`director status --json`.

Take it from there and nowhere else. Do not scrape branch names and paths out of
an agent's prose and hand-write them into `director note` — the agent knows
where its work landed and you are guessing from a screen snapshot, and the note
replaces rather than appends, so the accumulated list is one forgetful turn away
from being lost.

The table's cell is shortened — the first material clipped, then `+N` for the
rest — because that column is on screen every turn. Read `--json` when you need
the whole list, or when you need a URL you can actually click.

Write `—` when there is nothing yet, and mean it: an engagement that has
reported no materials has an empty cell, and that is the truth about it. Do not
fill it with the `detail.transcript` or `detail.log` paths from `--json`: those
are present for every engagement from the moment it spawns, so using them makes
an engagement that has produced nothing look exactly like one that has produced
something.

**If an engagement is doing work worth looking at and its materials stay empty,
say so in the brief.** `--materials` is described in the reporting contract every
agent is given, but an agent that never mentions it is not lying — it is just
silent, and silence here looks the same as having produced nothing.

## Questions are listed, not printed

`director status` does **not** print the text of a question an agent is waiting
on. It lists the open ones by identifier under the table:

```
2 open questions, text not shown:
  ask_578fc0d2  eng_a4b17f20  waiting 4m
  ask_9f01ab33  eng_88e3c1a5  waiting 3m

  read:    director status asks ask_578fc0d2 ask_9f01ab33
  answer:  director answer <ask> "..."
```

Run `director status asks <id>` for the full text, once, when you are about to
deal with it. That is the whole reason for the split: five agents each blocked
on a large plan would otherwise reproduce all five plans in every `director
status` you run, on every turn, until somebody answers — and status is the
command you run every turn before deciding anything.

So do not run `director status asks` with every identifier out of habit. Read
the one you are about to answer.

`--json` carries the identifiers in each engagement's `open_asks` and no
question text at all. `director status asks <id> --json` returns the questions
as objects.

## Do not wait for a turn that may never come

"Check every turn" carries an assumption worth making explicit: that you get
another turn. You do not decide when that happens. Your next turn arrives when
the person types something, and if they have stepped away, a `blocked`
engagement sits there — stopped, unanswered, burning wall-clock — until they
come back.

So "I'll check on it next turn" followed by handing control back is not a plan.
It is abandoning the fleet in a polite voice.

**`director attach` told you which fix is available to you.** It printed two
lines:

```
host: herdr (detected), offering background, wake
next turn: background `director wait` to be woken, and your engagements can also ring you when they report
```

Do what the `next turn:` line says. Do not guess from the name of the harness
you think you are running in — the same skill is installed everywhere, the
answer is worked out fresh on every attach, and it can differ between two
sessions in the same project. If you have lost the line, `director attach`
again and read it. There are three answers:

**Background `director wait`.** It blocks until an engagement needs you and then
exits, which turns *an agent responded* into *the director is running again*:

```sh
director wait --director <id> --until blocked,complete,abandoned,stalled
```

It prints the transition that woke it — which engagement, which health, and what
the agent last said. Exit `0` means matched, `4` means `--timeout` elapsed, `5`
means this host cannot background a wait at all, other non-zero means something
went wrong. Background it right after dispatching; when it returns you are awake
with the reason already in hand.

**Your engagements will ring you.** Nothing to arm — when one blocks, finishes,
or goes wrong, its own `director report` types a line into this conversation and
you get a turn. Treat it as a bonus and not as a guarantee: it is best-effort,
it is rate-limited, and it stops working if somebody else attaches to this
director after you. The state file is the record either way, so `director
status` is still what you act on.

**Neither.** Say so when you hand back. Tell the person plainly that nothing
will be checked until they prompt you, rather than implying someone is watching.
`director wait` will refuse here with exit `5`, and forcing it past that is how
you end up frozen and unreachable.

Five things that will bite you otherwise:

- **It fires once.** Re-arm after every wake, or you are back to waiting on the
  person. There is a blind window while you do — keep it short.
- **An engagement already in a matching health matches immediately.** That is
  deliberate: a question already asked is never asked again, so a `wait` that
  ignored current state would hang on exactly the thing you asked about. But it
  means arming a broad `--until` while a finished engagement is still on the
  books returns instantly and tells you nothing new. Scope it with
  `--engagement <id>`.
- **Never block on it in the foreground.** That is your whole fleet idle while
  you wait on one engagement — the failure `director watch` warns about.
- **The config root is resolved from the working directory.** A backgrounded
  command may not start where you did; if it reports no such director for one
  you can plainly see, pass `--config <root>`. `director where` prints it.
- **`director attach` is what says whether any of this applies.** Its `next
  turn:` line is the only place the answer comes from. If it said neither, none
  of the above is available to you and the person needs to be told.

## Writing a brief

The agent shares none of your context. Not the original wording of the request,
not what the other engagements have found, not what you have already ruled out.
It starts from nothing but what you write.

A brief needs four things:

- **The goal.** What you want to be true afterwards.
- **The acceptance condition.** How it will know it is done.
- **The boundary.** What not to touch. Whether it may commit or push.
- **The report-back.** What you need in its final report.

If the work needs a workspace of its own, how to create it is part of the goal
— the brief's first instruction, not something you did in advance. See *Do not
build the workspace yourself*.

Bad: *"Look at the auth code and fix whatever's wrong."* — no goal, no
boundary, no way to be finished.

Good: *"The login flow rejects valid tokens issued before a server restart.
Find the cause in `internal/auth/` and report it. Do not change any files —
this is an investigation. Report the specific function and the reason, and
whether the fix is one line or a redesign."*

## Bookkeeping

`director note <id> "<text>"` when you form the thought, not when you need it.
Your context will be compacted. The engagement list plus its notes is the
durable record of what is in flight; your own memory is not.

Use it for what only *you* know: why you spawned this, what you decided, what
you promised somebody. Not for where the work is — that is the engagement's
`--materials`, and it is a better source than your reading of its prose. The
note still **replaces** rather than appends, so re-state anything already in it.

## Two things you must not do

**Do not report work as finished on the strength of a status.** See the
`director-inspect` skill — this is the single most likely way to tell
somebody something shipped when it did not.

**Do not leave a blocked agent waiting.** An agent that asked a question is
stopped and burning wall-clock. Nobody else will notice. Answer it or escalate
it the same turn you see it — and arrange to see it, by whatever `director
attach` said this host allows, rather than hoping for a turn.

## Going further

- `director-inspect` — reading results, and every trap in the status
  output. Load it before you read an engagement.
- `director-workflows` — task types, permissions, and placement. Load it
  before your first spawn of a session.
- `progress-engagement` — chaining one engagement to a successor, so that
  "when that one is done, start this" needs nobody present in between.
