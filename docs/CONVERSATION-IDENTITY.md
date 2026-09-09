# Conversation identity, instead of a clock

A design note, not shipped behaviour. It answers five questions put to it after
the wake path stopped consulting `AttachGrace` (`d644226`).

## The short answer

The direction holds up, with one correction to its framing.

Recording a conversation identity at attach and comparing it later is the right
basis for the claim, and it is a real improvement on a duration: it detects a
collision rather than inferring one. Build it.

But **"an identifier the agent can't lie about" is not achievable here**, and
should not be promised. Every identity any of these harnesses can supply is an
environment variable read by a process the agent controls, and an agent with
`execute` has easier ways to subvert its director than forging one — it can
write the state file. What identity actually buys is correctness against the
confusions that occur *by accident*, plus the ability to tell the director's own
commands apart from its engagements' callbacks. Those are worth having. A
security property that gets believed and is not there would be worse than the
clock.

## 1. What identity each harness can supply, and whether it can be forged

| harness | identity | source | reusable? | forgeable by the agent |
| --- | --- | --- | --- | --- |
| herdr | pane id | `HERDR_PANE_ID`, gated on `HERDR_ENV=1` (`harness/herdr/socket.go:71-73,89-94`) | **yes** — an address, not an identity | yes, `export` |
| claude-code | session id | `CLAUDE_CODE_SESSION_ID`, gated on `CLAUDECODE=1` (`harness/claudecode/claudecode.go:90-91,113-117`) | no | yes, `export` |
| antigravity | none | implements no `SelfLocator` at all | — | n/a |
| plugin | whatever the plugin declares | `Hosting.DetectEnv` / `RefEnv` (`harness/plugin/plugin.go:133-150`) | plugin's choice | yes |

Two things this table makes visible that the framing did not:

**herdr's pane id is an address, not an identity.** herdr reuses pane ids. So
even a perfectly honest pane id does not answer "is this the same conversation
that attached" — it answers "is this the same seat", which is a weaker claim.
Claude Code's session id is a genuine conversation identity: one conversation,
for its life, never reissued, and backed by a transcript on disk that can be
looked up (`harness/claudecode/transcript.go:22`). The two harnesses are not
equally served by this design and the code should not pretend they are.

**The session id is present in practice.** The comment at
`harness/claudecode/claudecode.go:113-117` shrugs at an empty session id because
nothing dials it. Measured from inside a live Claude Code engagement while
writing this, `CLAUDE_CODE_SESSION_ID` is set and non-empty. The empty case is
still real for other invocation modes and must be handled (see §4), but it is
not the common case, and the design should not be shaped around it.

**The threat that actually happens is inheritance, not lying.** A child process
inherits its parent's environment and then *honestly* reports being its parent's
conversation. This codebase already knows it and already fixes it:
`withoutPaneEnv` (`harness/herdr/socket.go:105-117`) strips the pane variables
on the way out of a spawn, with a comment saying exactly why. That mitigation is
the one that matters, it already exists, and identity would inherit its
protection. Real unforgeability would need the harness to assert the identity
out of band — herdr has no API for "which pane is pid N in", and Claude Code
offers no per-session secret. That is a much larger project and I do not think
it is warranted.

## 2. What the claim check becomes, and whether `AttachGrace` survives

The comparison already exists. `selfInitiated()` (`director/notify.go:375`)
takes `locateHost()`, compares harness and a non-empty ref against what was
recorded, and is documented as being written precisely to avoid mistaking one
conversation for another. That pair *is* the conversation identity; it has no
name and is used in one place.

So the claim check becomes:

- The asker **is** the recorded conversation → not a collision, it is you.
- A **different** identity is recorded, and that seat still resolves in its
  harness → **claimed**. Refuse to hand it over.
- A different identity is recorded and it no longer resolves → **unclaimed**.
  (`addressResolves`, `director/notify.go`, landed for the wake path, is the
  same question and the same call.)
- No identity recorded, or the harness supplies none → §4.

`Claimed` at `director/attach.go:100` and `decide()` at `director/attach.go:133`
are where this lands.

**Does `AttachGrace` survive?** Not as the collision test — identity plus
resolution answers that directly and better. A duration still earns a place for
the abandonment prompt in §3, but it is a different number measuring a different
thing, and it should not keep this name: what it would bound is how long a seat
that still resolves may go silent before a person is asked about it. Call that
`AbandonedAfter`, and note that it needs a timestamp *refreshed by activity* —
which `AttachedAt` is not, and must not become. Renewing `AttachedAt` on any
`director` invocation includes every engagement's `report` callback, which would
keep an empty seat warm for as long as the fleet runs.

That is the honest coupling, and it reverses the answer I gave to the original
brief's third judgement call: **do not refresh `AttachedAt` today**, because
nothing can distinguish the director's own command from an engagement's
callback — and *the thing that would make refreshing correct is exactly this
design*. Identity is the prerequisite. Slice 4 below is where refresh belongs,
not before.

## 3. How a dead conversation's director is taken over

Identity solves identity and gives no liveness. `director/attach.go:10-16` is
right that a conversation cannot be asked whether it is alive, and a session id
that never returns would pin a director forever. Three layers, cheapest first:

1. **Resolution — nobody's decision, an observation.** Ask the harness whether
   the recorded seat is still found and still live. If it is not, the
   conversation is demonstrably gone and takeover is automatic. This covers the
   common case — a closed pane, a finished session — and it is already built and
   landed for the wake path.
2. **Unresolvable but plausible — the person's decision.** The harness is
   unreachable, or supplies no identity, or answers "unknown". Nothing is known,
   and a timeout is a legitimate *prompt*: "nothing has attached here in 4 hours
   and the harness cannot confirm the seat; take it over?" This belongs in the
   survey, which already has `RecommendAsk` for exactly the case where only a
   person can say (`director/attach.go:165,186`). No new interface.
3. **Explicit override.** `director attach <id>` already bypasses the survey. It
   should say out loud when it is taking a seat that still resolves, rather than
   doing it silently.

In no layer does a duration silently decide anything. Its only power is to move
a recommendation from "create your own" to "ask the person" — which is the
distinction the direction drew, and it is the right one.

## 4. A harness that can supply no identity

Two cases, and conflating them would be a bug:

- **The harness identifies nothing** (antigravity; a plugin with no
  `DetectEnv`). A claim here can neither be attributed nor disproved. Keep the
  time-based claim as the fallback and **say so in the output**: the survey line
  should read `claimed 12m ago (this harness cannot identify conversations)`,
  not present a guess as a fact. Degrading loudly is this codebase's existing
  habit — `UnknownHosting`, `LifecycleUnknown`, `Complete: false` on a screen
  read.
- **The harness identifies conversations, but this one has no ref** (Claude Code
  with an empty session id). `selfInitiated()` already refuses to treat an empty
  ref as a match, because otherwise every Claude Code conversation on the machine
  would look like this one. That rule carries over unchanged: **empty is never a
  match, and never a claim.**

## 5. Size, and how many engagements

Not one. Strictly ordered:

| # | slice | size | touches | blocked on |
| --- | --- | --- | --- | --- |
| 1 | Retire the clock from the wake path | small | **landed**, `d644226` | — |
| 2 | Name the identity: `ConversationID` recorded at attach, `selfInitiated()` rewritten in terms of it. Pure refactor, no behaviour change | small | `director/host.go`, `director/state.go`, `director/notify.go` | `abandoned-recovery` owns `state.go` |
| 3 | Claim becomes identity + resolution, time only as the named fallback | medium — `decide()`'s reasoning and all its comments are rewritten | `director/attach.go`, `cli/attach.go` | slice 2 |
| 4 | Abandonment prompt: activity timestamp refreshed by the recorded conversation's own commands, `AbandonedAfter`, new `RecommendAsk` reason | medium | `cli/cli.go`, `director/attach.go` | `abandoned-recovery` owns `cli/cli.go`; slice 3 |
| 5 | Actual unforgeability | large, and I would not do it | a herdr API change; nothing exists for Claude Code | — |

Three further engagements (2, 3, 4). Two of them are blocked on
`abandoned-recovery` merging, so slice 2 is the one to queue behind it.
