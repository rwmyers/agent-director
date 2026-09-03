---
name: director-remove
description: Drop engagements from a director's record with director remove - how to resolve which ones, naming several at once, when removing one strands a running agent, and what removal does not touch. Use when a spawn failed and left a row nothing can clear, when a batch of finished engagements is only noise in the status table, or when asked to remove, clear or forget engagements.
---

# Removing engagements

## The trap: removing a live engagement strands it

The director's state file is the only thing that maps an engagement back to its
harness. Remove the row and the agent does not stop — it keeps running, keeps
costing money, and can no longer be listed, read, answered or stopped by
anything, because the only handle to it was the row you deleted.

So `director remove` refuses while the engagement is alive, and tells you which
two ways out exist:

    director remove <id> --stop     end it, then forget it
    director remove <id> --force    leave it running, unreachable, deliberately

The refusal is per engagement and naming several does not soften it. Every
identifier in a batch is checked exactly as if it had been given on its own,
and the flags apply to all of them — `--stop` in a batch stops every live one
in it, which is a much larger act than stopping one. Name the batch, read what
it refuses, and decide about those separately.

**Take `--stop` unless somebody has said otherwise.** `--force` is for an agent
that should genuinely keep working with no director tracking it, which is rare
and is a person's decision, not yours. If you find yourself reaching for
`--force` to get past a refusal, you are about to orphan something — stop and
say so instead.

What it refuses on is lifecycle — whether a process is attached — and not
health, and it observes that fresh rather than trusting the stored row. Only
`abandoned` and `complete` are certain to remove without either flag; both mean
the harness saw the process end. `stalled` does not. Stalled says a report is
overdue and the harness has seen no activity, which is also what an idle,
reachable agent looks like, so a stalled engagement is often still live and
`remove` refuses it with the same two ways out. The case that removes cleanly is
the ordinary one: a spawn that failed and left a row with no process behind it,
or finished work whose row is now noise.

## Resolve to exactly one, before you run anything

You will be given a fragment — four hex characters off a status table, or a few
words of a title — not a full identifier.

`director remove` accepts a fragment and matches it against both identifiers
and titles, case-insensitively. If it matches more than one it refuses and
lists the candidates. **Do not then pick one.** Take the refusal back to
whoever asked, with the candidates, and let them say which. Guessing between
two engagements removes somebody else's work and nothing in the output says
that it happened.

Every fragment in a batch is resolved that way separately, so a batch is only
as safe as its worst fragment. Resolve each one before you run anything.

Every *other* director command matches identifiers exactly. So the moment you
have resolved a fragment, expand it: run `director status --json`, find the
one engagement, and use its full `id` for the `stop`, `read` or `note` you run
around the removal. A fragment passed to `director read` is simply not found,
which reads like a missing engagement rather than a mistyped one.

## Nothing undoes it, so read before you remove

There is no restore. The row is gone from the state file when the command
returns, and with it the note you attached, the read cursor, the token, and any
question the engagement had raised. `director read` needs the row, so anything
that still matters — the transcript above all — has to be read *before* the
removal. Afterwards there is nothing left to read it through.

Asking permission is not what makes that safe. When the fragment resolves to
exactly one engagement, remove it and say which row went, in the same message as
the result — id, title, health, progress:

    removed eng_41bfe6a01111ac1b  spawn onto herdr  stalled (no progress)

One line each, health first, the way `director status` composes it — and one
line per engagement when you removed several, not a count. Somebody told only
"removed eng_41bf" has been given nothing to check you against, and somebody
told "removed 6 engagements" has been given less than that. They still get to
check you; they are just not made to answer a question first when they have
already asked for the removal. Ambiguity is the gate here, not agreement — a
fragment matching more than one engagement goes back to them, one match does
not.

## Clearing several

`director remove` takes as many engagements as you name:

    director remove eng_41bfe6a01111ac1b eng_9c02aa7730b1e5d4 eng_00d1f8b2c4e69a37

This **best-effort, not all-or-nothing**. Whatever could be removed is removed,
whatever could not is reported, and the command exits non-zero having done part
of the job.

`--json` returns a list of the removals that happened, one object per
engagement, whether you named one or ten. What failed is on stderr and in the
exit code, not in that list.

## What removal does not do

It removes the director's memory of the work. It does not touch the work.

- **Not the transcript or the log.** They live wherever the harness put them.
  You cannot read them through this director afterwards, though — `director
  read` needs the row. If the transcript matters, read it *before* removing.
- **Not the workflow, the task type, or the prompts.**
- **Not the agent's output.** Branches, worktrees, commits, files: all
  untouched. Removing an engagement never reverts anything it did.
- **Not other engagements**, and not the director itself.

Say this plainly when you report a removal, because "removed" invites the
reading that the work was undone.

## What to reach for instead

- **The work is going the wrong way** — `director stop`. That ends the process
  and *keeps* the row, which is what you want while you still care what it did.
  Stop and remove are a pair: stop deals with the agent, remove deals with the
  record.
- **You are done with the whole fleet** — `director retire`, which removes the
  director and its entire record. Never remove every engagement in a batch to
  approximate it: that leaves the director itself behind, still claimed, still
  listed, holding nothing.
- **The row is wrong rather than unwanted** — `director note` it. Removal is
  not a way to make a status table say what you wish it said.

Never edit the state file by hand to do this. Writes are locked and atomic
because an agent's `report` can land between your own two commands; a hand-edit
races that and loses whatever arrived in between.
