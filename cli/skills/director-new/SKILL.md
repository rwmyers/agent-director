---
name: director-new
description: Turn a loose request into one well-formed director spawn - choosing the task type from the workflow, eliciting the parts of the brief the person left out, and dispatching it. Use when someone says /director-new, describes a piece of work in a sentence and wants it delegated, or asks you to spawn, dispatch or brief an engagement.
---

# Briefing one engagement

You have been handed a sentence and asked to make an engagement out of it.
Intake is the whole job: work out what kind of work this is, get the missing
parts of the brief out of the person, spawn once. Not do the work yourself,
and not spawn three engagements because the sentence had three clauses.

## Decide the type from the task list, never from a name

Task types come from the workflow this director was bound to. There is no
built-in `review` and no built-in `implement`. **Run `director tasks` before
you decide anything** and choose from what it actually prints; `director
tasks <name>` shows one in full — what it is for, what it may do, what
progress values it reports.

Pick by what the work *is*, not by what is convenient. Work that only needs
to be understood — *why does this happen*, *is this safe to change* — gets
a read-only task. Giving an investigation write access invites it to start
changing things nobody agreed to, and you find out from the diff.

If the request straddles two types — understand this, then fix it — that is
two engagements, and the second cannot be briefed until the first reports.
Spawn the investigation now and say the fix will follow. Do not widen the
read-only task's brief to cover the fix instead.

`director-workflows` has the rest: permission scopes, placement, and what to
do when a spawn is refused.

## Pull the four parts out of what you were given

The `director` skill's *Writing a brief* section names them — the goal, the
acceptance condition, the boundary, the report-back. Knowing what a good
brief contains is not the hard part here. The hard part is that you were
given one sentence and three of the four are missing from it.

So read the request as an answer to each in turn, and write down which ones
it genuinely answered:

- **Goal** — usually the only one present. Restate it as what will be true
  afterwards, not as the activity.
- **Acceptance** — rarely present. Which command passes, which behaviour
  changes, what the final report has to contain.
- **Boundary** — almost never present, and the expensive one. Which paths,
  which branch it makes or stays on, whether it may commit, whether it may
  push.
- **Report-back** — if you cannot say what you will do with the result, you
  do not have this yet.

Everything you know that the agent does not is also part of the brief: what
another engagement has already found, what has been ruled out, which file
the person pointed at. It shares none of your context and none of theirs.

## The workspace is the brief's first step, not something you do first

If the work needs a worktree, a branch, a clone or a checkout of its own, the
brief says how to make one and the agent makes it. Do not prepare it yourself
and spawn into the result, however few commands that would take.

Mostly you would be locking the agent out rather than helping it. The harness
sandboxes an agent to the directory it starts in, so a workspace you created
can sit outside that sandbox, and the agent is then refused every attempt to
enter the one directory you meant it to work in — a failure that reads as
missing files rather than as something you did. The agent starts where you are
when you spawn it, so spawn from a directory that contains where the work will
land and let the agent make the rest. The setup then survives in the brief
after your own context is compacted, and the agent starts in a state it made
and can check rather than one it was handed.

So write it as the first instruction, in the imperative, naming the directory
to run it from: *"run `<setup command>` from `<path>`, then do your work in
`<path>/<slug>`."* The `director` skill's *Do not build the workspace
yourself* has the rest of the argument.

## Ask about the gaps that change the work; default the rest

One short round of questions, then spawn. Two or three, in a single message.
Not a form, and not a second round unless the answers contradict each other.

Worth asking:

- **The acceptance condition**, when the work could plausibly stop in more
  than one place. An agent with no finish line either stops early or keeps
  going past what was wanted.
- **Commit and push.** Ask every time unless they have already said. This is
  the gap where guessing wrong leaves something you cannot take back.
- **Which repository or directory**, when more than one is in play.
- **The task type**, when two of them fit and the difference between them is
  whether the work gets write access.

Not worth asking — decide, and say what you decided:

- `--name`, `--title`, `--harness` and every other flag. Placement is the
  one exception: it is decided once and cannot be changed afterwards, so ask
  if the person will want to watch or take over.
- Scope details the agent can settle for itself by looking.
- Anything one command would answer. Run the command — a question you could
  answer, that is, not a change you could make.

If nobody is there to answer, do not stall. Spawn with the narrowest
boundary the request supports — no commit, no push, one directory — say in
the brief that the boundary is a default and the agent should report rather
than widen it, and tell the person afterwards what you assumed.

## Spawn

```sh
director spawn "<brief>" --task <type> --title "<short label>" \
  --name <harness-label>
```

**The agent starts in the directory you run this from.** Check where you are
first, and read back the `starting in` line the spawn prints — a working
directory that drifted a level down moves every agent you spawn, and nothing in
the command says so.

**Always pass `--title`.** Without it the title is the first line of the
brief cut at sixty characters, so the status table fills with truncated
prose that nobody can match back to a row a turn later. Write a short noun
phrase for the work, and keep using it verbatim afterwards.

**Pass `--name` whenever more than one engagement shares a directory.**
Without it the harness labels the conversation from the working directory,
and three engagements in one repository become indistinguishable to anyone
looking at the harness rather than at you.

Before you run it, read the brief back as the agent will receive it: no
pronouns pointing at things only you can see, no "as discussed", no "the
file we talked about".

## After the spawn

`director spawn` returns as soon as the conversation starts. The agent has
not done anything yet, and you have not finished delegating until something
other than the person can wake you.

- **Note what is durable.** `director note <id> "<text>"`: why you spawned it,
  the boundaries you defaulted, anything the person said that did not fit in the
  brief. Your context gets compacted; the note does not. It replaces rather
  than appends, so re-write the whole list each time. Where the work lands is
  not yours to record — the engagement reports that itself with `director
  report --materials`, and it shows up in the `MATERIALS` column.
- **Arrange your next turn** the way the `next turn:` line from `director
  attach` said you can — usually backgrounding `director wait` so the engagement
  finishing or blocking wakes you. The `director` skill's *Do not wait for a
  turn that may never come* has the three answers, the flags, and the five ways
  it bites.
- **Tell the person what you spawned**, in their terms, with the id's first
  four characters and whatever you assumed on their behalf.
