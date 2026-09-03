---
name: director-workflows
description: Choose task types, permission scopes and harness placement when spawning engagements as a director. Use before the first spawn of a session, when deciding what kind of engagement to create, when a spawn is refused on permissions, or when asked what a director can run.
---

# Task types, permissions, placement

## Task types are not built in

Everything a director can spawn comes from the workflow it was bound to at
`director init`. Nothing about what a task *means* is built into the tool —
the names, the stages, the permissions and the prompts are all somebody's
configuration.

So: **run `director tasks` before your first spawn.** Do not assume a task
called `review` or `implement` exists, and do not guess at a name. `director
tasks <name>` shows one in full: what it is for, what it may do, what progress
values it reports, and how often it is expected to speak.

Pick the task type by what the work *is*, not by what would be convenient. If
the work only needs to be understood, use a read-only task; giving an
investigation write access invites it to start fixing things you have not
agreed to.

## Progress vocabularies are per task

Each task declares its own progress values. Those are the only ones its agent
may report, and they are the only ones you will see in `director status` for
that engagement. Two task types in the same workflow will usually have
different ones, and a value that is terminal for one means nothing for another.

Do not infer completion from a progress value's name. `delivered` is terminal
for a task only if that task says so — health tells you which, by reporting
`complete` rather than `abandoned`.

## Permissions fail closed

A permission scope grants capabilities: `read`, `search`, `edit`, `execute`,
`network`, `push`. Anything not granted is denied.

If a harness cannot actually enforce what a scope asks for, **the spawn is
refused** rather than run with more access than requested:

    director: task "risky" requires permission scope "no-push": claude-code
    cannot withhold "push" while granting "execute" ...

This is not a bug to work around. Do not retry with a different harness hoping
it will be laxer, and do not edit the workflow to loosen the scope so the spawn
goes through. Either the task genuinely needs that access — in which case say
so to the person and let them change it — or pick a task type whose scope fits.

An agent under a restrictive scope can still always report and ask. The
callback channel is not one of the capabilities a workflow grants.

## Placement

A task type may pin a harness; otherwise the workflow's default applies, and
`--harness` overrides both.

Run `director harnesses` once at the start of a session. A harness that needs a
running server may not have one, and finding that out at spawn time wastes a
turn. Prefer a harness the person can watch when they will want to take over;
prefer a headless one for work nobody will be watching.

**Placement is decided once.** There is no moving an engagement between
harnesses afterwards, so if you are unsure which the person wants, ask before
spawning rather than after.

### Which herdr window an engagement lands in

Never the one the person is looking at. herdr would put a new tab in whatever
window is focused, which means a fleet spawned while somebody was moving around
their session ends up scattered across it.

- A director **running in a herdr pane** places every engagement in its own
  window, whatever herdr is focused on at the time. Two spawns an hour apart
  land in the same place.
- A director **with no pane of its own** — headless, or hosted by claude-code —
  places into a window labelled `director`, which is created if it is not
  already there. That is where to look for an unattended fleet.
- Spawning never moves the person. The window is never focused and the tab is
  never raised.
- If a director's own pane has been closed out from under it, the spawn is
  refused and says so. It does not quietly place the agent somewhere else.

## Naming

`--title` is what *you* see in `director status`. `--name` is what the *harness*
shows in its own interface.

Set `--name` when spawning more than one engagement in the same directory.
Without it the harness derives a label from the working directory, so three
engagements in one repository become indistinguishable to anyone looking at the
harness instead of at you.
