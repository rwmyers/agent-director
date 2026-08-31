---
name: director-remove
description: Drop one engagement from a director's record with director remove - how to resolve which one, when removing it strands a running agent, and what removal does not touch. Use when a spawn failed and left a row nothing can clear, when a finished engagement is only noise in the status table, or when asked to remove, clear or forget an engagement.
---

# Removing one engagement

## The trap: removing a live engagement strands it

The director's state file is the only thing that maps an engagement back to its
harness. Remove the row and the agent does not stop — it keeps running, keeps
costing money, and can no longer be listed, read, answered or stopped by
anything, because the only handle to it was the row you deleted.

So `director remove` refuses while the engagement is alive, and tells you which
two ways out exist:

    director remove <id> --stop     end it, then forget it
    director remove <id> --force    leave it running, unreachable, deliberately

**Take `--stop` unless somebody has said otherwise.** `--force` is for an agent
that should genuinely keep working with no director tracking it, which is rare
and is a person's decision, not yours. If you find yourself reaching for
`--force` to get past a refusal, you are about to orphan something — stop and
say so instead.

A `stalled`, `abandoned` or `complete` engagement is not live and removes
without either flag. That is the ordinary case: a spawn that failed and left a
row with no process behind it, or finished work whose row is now noise.

## Resolve to exactly one, before you run anything

You will be given a fragment — four hex characters off a status table, or a few
words of a title — not a full identifier.

`director remove` accepts a fragment and matches it against both identifiers
and titles, case-insensitively. If it matches more than one it refuses and
lists the candidates. **Do not then pick one.** Take the refusal back to
whoever asked, with the candidates, and let them say which. Guessing between
two engagements removes somebody else's work and nothing in the output says
that it happened.

Every *other* director command matches identifiers exactly. So the moment you
have resolved a fragment, expand it: run `director status --json`, find the
one engagement, and use its full `id` for the `stop`, `read` or `note` you run
around the removal. A fragment passed to `director read` is simply not found,
which reads like a missing engagement rather than a mistyped one.

## Confirm before removing, because nothing undoes it

There is no restore. The row is gone from the state file when the command
returns, and with it the note you attached, the read cursor, the token, and any
question the engagement had raised.

Show what will go and get agreement first — id, title, health, progress:

    eng_41bfe6a01111ac1b  spawn onto herdr  stalled (no progress)

One line per engagement, health first, the way `director status` composes it.
Somebody who is shown only "removing eng_41bf" has been given nothing to check
you against. If they asked for a removal in the same breath — "clear that dead
spawn" — you still show the row you matched, because the thing being confirmed
is *which* engagement, not whether they meant it.

Remove one at a time. There is no bulk form, and a loop over a fragment list is
how a fleet gets cleared by somebody who meant to clear one row.

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
  director and its entire record. Never remove engagements one at a time to
  approximate it.
- **The row is wrong rather than unwanted** — `director note` it. Removal is
  not a way to make a status table say what you wish it said.

Never edit the state file by hand to do this. Writes are locked and atomic
because an agent's `report` can land between your own two commands; a hand-edit
races that and loses whatever arrived in between.
