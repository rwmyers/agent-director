---
name: progress-engagement
description: Chain one engagement to a successor - resolving which engagement to follow, waiting for it without blocking, carrying its result into the next brief, and spawning. Use when someone says "when that one is done, start this", queues follow-on work in advance, asks you to chain or hand off from a running engagement, or says /progress-engagement.
---

# Chaining one engagement to the next

Somebody has told you, once and in advance, what should happen after an
engagement finishes. Everything after that sentence is yours: noticing the
finish, reading what it produced, writing the successor's brief out of it, and
spawning. They are not going to be there.

That is the whole point. Without this, moving from one piece of work to the
next needs a person present at four separate moments — to wait, to notice, to
decide what comes next, to ask for the spawn. This collapses those into one
instruction they give before walking away.

**Nothing here assumes what the two pieces of work are.** Not that the first
one plans and the second one builds, not that anything was approved in
between, not that there are two rather than five. Task types come from the
workflow this director was bound to and mean whatever that workflow says —
`director-workflows` has that argument. Any engagement can be chained to any
successor.

The five steps: resolve the predecessor, arm a wait for it, read what it
produced, brief the successor from that, spawn. The third is where the value
is and it is the one that gets skipped.

## Step 1: resolve the predecessor, then expand it

You will get one line with two things in it and no punctuation that reliably
separates them:

> *when a803 is done, get its branch reviewed for concurrency bugs*

> *once the config audit finishes have someone write up what it found*

**Resolve against the fleet, not against the grammar.** The set of engagements
is small, closed, and printed by `director status`. Take the spans of the line
that could be naming one — a four-character handle, a quoted phrase, a run of
words that reads like a title — and test them against it. The span that matches
an engagement is the identifier. **Everything else in the line is the
description of the work**, and it goes to the successor whole.

Resolving a fragment is `remove-engagement`'s discipline and it is the same one
here: a fragment may be four hex characters off a status table or a few words of
a title, and **if it matches more than one engagement you refuse and take the
candidates back to the person**. Do not pick. Chaining onto the wrong
predecessor writes a brief out of somebody else's work and nothing in the
output says that it happened.

Two ways the split itself goes wrong:

- **Nothing resolves.** The engagement may not exist yet, or they may be
  describing it in words you have no handle for. Ask which one they mean, with
  the table in front of them. Do not chain onto the most recent engagement
  because it is the most likely.
- **Two different spans resolve, to two different engagements.** The words of
  the description happened to match another engagement's title. That is
  ambiguity of exactly the kind above: name both and ask.

**Never trim the description down to what you think the brief is.** A clause
you drop as belonging to the predecessor is usually a constraint on the
successor. `new-engagement` will elicit what the line is missing; it cannot
recover what you deleted from it.

Then expand. `director remove` takes fragments; **every other command matches
identifiers exactly**, so a fragment handed to `director read` or `director
wait` is not a near miss, it is `no such engagement` — and `wait` exits `2`,
which reads like the engagement died rather than like you mistyped it. Run
`director status --json`, find the one row, and use its full `id` for
everything from here on.

## Step 2: arm the wait, in the background, scoped

**Check whether it has already finished before you wait for anything.** An
engagement already in a matching health matches immediately — that is
deliberate, and it means a wait armed over a predecessor that finished an hour
ago returns instantly having told you nothing. You saw its health in step 1's
`status --json`. If it is already terminal, skip straight to step 3.

Otherwise:

```sh
director wait --director <id> --engagement <full-id> &
```

The default `--until` is already `blocked,complete,abandoned,stalled` — the
four things that could happen to a predecessor that you have to do something
about. Do not narrow it to `complete`. A predecessor that blocks while you are
waiting has stopped and is burning wall-clock, and a wait that ignored it would
leave it there until the person came back, which is the failure this skill
exists to remove.

**Never block on it in the foreground.** That is the entire fleet idle while
you sit on one engagement, and it is worse here than usually: chaining is a
thing you do *because* nobody is watching, so a foreground wait can hold the
whole director for as long as the predecessor takes.

Three more that will bite you:

- **It fires once.** Every outcome in step 3 that is not `complete` ends with
  re-arming. Forget that and the chain is silently dead, with a row in the
  table that still looks alive.
- **Scope it with `--engagement`.** An unscoped wait matches any engagement in
  the fleet, so any other finished row returns it immediately and the chain
  fires off the wrong signal.
- **A backgrounded command may not start where you did**, and the config root
  comes from the working directory. If it reports no such director for one you
  can plainly see, pass `--config <root>`; `director where` prints it.

If `director attach` said this host cannot background a wait, it will refuse
with exit `5`. Do not force it. Say plainly to the person that the chain cannot
be armed here and that nothing will happen until they prompt you.

**Write the chain down before you hand back.** `director note <full-id>
"queued: <the successor, in one line>"` on the predecessor. Your context will be
compacted, and a chain that exists only in it is a promise nobody can find
afterwards — the predecessor finishes, you wake up, and there is nothing to say
what you were supposed to do about it. The note replaces rather than appends,
so re-state whatever was already there. Then tell the person what you armed.

## Step 3: only `complete` progresses

`abandoned` is not `complete`. It means the process ended short of its task's
terminal progress value — something stopped halfway. **Spawning a successor off
work that failed is worse than doing nothing**, because it produces a
confident-looking engagement built on a brief you invented to cover a gap.

What the wake means, and what to do:

| Woke on | What happened | Do |
|---|---|---|
| `complete` | it reached its terminal value | go to step 4 |
| `blocked` | it asked a question mid-flight | answer or escalate it, then **re-arm** |
| `stalled` | overdue and nothing happening | `director nudge`, then **re-arm**; second time, escalate |
| `abandoned` | it ended without finishing | do not spawn — read it and take it back to the person |

`blocked` is the one that looks like an ending and is not. The wait returned,
the chain is not finished, and if you treat the wake as "the predecessor is
done" you will brief a successor off half-finished work. Deal with the
question the way the `director` skill says, then arm the wait again.

For `abandoned`, read it far enough to say *what* stopped and how far it got,
and put that in front of the person along with the successor you were going to
spawn. They may want it anyway, they may want the predecessor retried, they may
want neither — and it is a different brief in all three cases, which is why it
is not yours to decide. `director-inspect` has the rest of the health verdicts,
including why `quiet` is not `stalled`.

## Step 4: read the predecessor before you brief

**The successor shares none of the predecessor's context.** Not the plan it
wrote, not the bug it found, not the branch it left, not the decision it took.
It starts from your brief and nothing else. So the chain is only worth anything
if you read the predecessor and put its result into that brief — otherwise you
have automated the spawn and thrown away the reason for chaining.

`director read <full-id>` returns what is new since your last read. Summarise as
you go; do not pull the transcript into your context and reason over it.

**The read can come back thin, and it does so silently.** The cursor has
already moved if you read it earlier in the session; a harness that returns a
screen snapshot has finite scrollback, so earlier output may be *gone* rather
than merely behind the cursor; and `[N earlier turns omitted]` means you are
seeing the end of something longer. None of those look like failures.

When you cannot get the result out of the transcript, use what the engagement
published rather than guessing:

- **`materials` in `director status --json`** — the branch, PR, path or document
  it named. That is durable and the transcript is not, and it is what the
  successor actually has to look at.
- **Your own `director note`** on the predecessor.
- Then **write the brief so the successor derives the state for itself**: name
  the branch and the paths and tell it to read them first, rather than
  asserting a summary you are not sure of. A brief that confidently states the
  wrong result is more expensive than one that says "read this branch and work
  out what it did".

If there are no materials either and the transcript is unreadable, you have
nothing to chain from. Say that to the person instead of spawning something
plausible.

## Step 5: hand it to `new-engagement`

`new-engagement` already turns a loose request into one well-formed spawn: it
picks the task type from `director tasks`, pulls the four parts of a brief out
of what it was given, decides the flags, and dispatches. **Use it. Do not
restate any of it here.**

What you bring to it is the description from step 1 plus what you learned in
step 4, written out as if the person had said it — no pronouns pointing at
things only you can see, no "the plan it produced", no "as discussed". Name the
predecessor's branch, its finding, its decision, in words.

One thing changes because this is a chain and nobody is there: `new-engagement`
would normally ask a short round of questions about the gaps, and there is no
one to ask. It already says what to do about that — spawn with the narrowest
boundary the request supports, say in the brief that the boundary is a default
the agent should report against rather than widen, and tell the person
afterwards what you assumed. Take that path deliberately here rather than
inventing answers, and note in your report to them which parts of the brief were
yours rather than theirs.

## The predecessor stays

**Do not remove it.** Not as part of progressing, not once the successor is
running, not because its row is now noise.

Removal is irreversible. The row is the only handle to the transcript, the
note, and the record the successor's brief was written from — and if the brief
turns out to be wrong, or the successor comes back asking what the predecessor
actually decided, the row is where the answer was. A chain runs while nobody is
watching, which is exactly when you least want an unattended, irreversible act
performed on the strength of one line of instruction and your own reading of
it. The harness slot it holds is a real cost, but it is a recoverable one; the
record is not.

If the person asked for the predecessor to be cleared as part of the
instruction, do it — that is their call, and `remove-engagement` has how. But
the ordering is not optional: **read it, brief the successor, spawn, and only
then remove.** Removing first destroys the input to step 4, and by the time you
notice, there is nothing to read. If the spawn failed, do not remove at all —
the predecessor's record is now the only thing left to retry from.

## Chains longer than two links

You can only arm a wait on an engagement that exists. If the person describes
three steps, chain the first link now and note the rest on the predecessor;
when the successor has been spawned and has an id, arm the next link against
it. Do not try to queue the whole sequence at once — there is no identifier to
hang the later links on, and a chain you cannot name in a `director note` is one
your next compaction will lose.
