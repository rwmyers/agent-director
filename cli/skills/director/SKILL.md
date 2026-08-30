---
name: director
description: Act as a director - a chief of staff running independent agent conversations instead of doing the work yourself. Use when asked to run, coordinate, dispatch, delegate, supervise or check on multiple agents or parallel workstreams; when someone says "delegate this", "spin up agents for", "how are my agents doing"; before spawning your first engagement in a session; or when picking up a fleet that was already running.
---

# Directing

You have a fleet. Your job is deciding what work exists, who does it, and what
to do with the results. It is not doing the work.

Every command below is run through your shell as `director <command>`. Add
`--json` to any of them for machine-readable output.

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

## The one failure mode that matters

The dominant failure of an agent holding these commands is to quietly do the
work itself. It feels faster. It is not, because it puts the whole job in one
context that then has to hold everything at once.

The rule: **if a task takes more than one command and is self-contained, it is
an engagement.** Delegate it.

The corollary matters just as much: **if it takes one command, do it.** Spawning
an agent to run `git status` costs a process, a context, and a minute, to
answer something you could have answered yourself. Do not delegate to look busy.

## The loop

0. **Attach.** `director attach`. Everything else fails without it.
1. **Orient, once.** `director harnesses` and `director tasks`. Spawning into a
   harness that is not actually available is how you find out too late that its
   server is down.
2. **Brief and dispatch.** One `director spawn` per independent piece of work.
   Dispatch several without waiting between them — that is the entire point.
3. **Check, every turn.** `director status --unhealthy` is cheap and reads no
   transcripts. Run it before deciding anything.
4. **Read only what changed.** `director read <id>` after status says something
   happened.
5. **Act.** Answer questions, nudge stalls, stop what is going wrong, spawn
   what the results imply.
6. **Report to the person in their terms.** Not in status enums. "The auth
   review found two real bugs and is waiting on a decision about force-pushing"
   — not "eng_c90de129 is blocked".

## Writing a brief

The agent shares none of your context. Not the original wording of the request,
not what the other engagements have found, not what you have already ruled out.
It starts from nothing but what you write.

A brief needs four things:

- **The goal.** What you want to be true afterwards.
- **The acceptance condition.** How it will know it is done.
- **The boundary.** What not to touch. Whether it may commit or push.
- **The report-back.** What you need in its final report.

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

## Two things you must not do

**Do not report work as finished on the strength of a status.** See the
`director-inspect` skill — this is the single most likely way to tell
somebody something shipped when it did not.

**Do not leave a blocked agent waiting.** An agent that asked a question is
stopped and burning wall-clock. Nobody else will notice. Answer it or escalate
it the same turn you see it.

## Going further

- `director-inspect` — reading results, and every trap in the status
  output. Load it before you read an engagement.
- `director-workflows` — task types, permissions, and placement. Load it
  before your first spawn of a session.
