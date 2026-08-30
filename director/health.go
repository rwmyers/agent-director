package director

import (
	"time"

	"github.com/rwmyers/agent-director/harness"
)

// Health is the core's verdict on whether an engagement is well, and whether it
// needs the director's attention. It is the axis a director acts on.
//
// Lifecycle answers "is a process attached", progress answers "where has the
// work got to", and neither on its own tells a director what to do. An agent
// can be lifecycle-working and silently wedged; it can be lifecycle-done having
// achieved nothing. Health exists so that the director notices, rather than a
// human noticing that the director did not.
//
// It is derived on every call and never stored. Health is a function of now, so
// a persisted "ok" becomes wrong purely through the passage of time — which is
// the one way a staleness detector can fail while continuing to look healthy.
type Health string

const (
	// HealthOK means the engagement is meeting its reporting contract.
	HealthOK Health = "ok"
	// HealthQuiet means a report is overdue, but the harness still shows the
	// agent doing work. It is working and not talking, which is not a problem.
	HealthQuiet Health = "quiet"
	// HealthStalled means a report is overdue and the harness has seen no
	// activity either. The director should nudge it.
	HealthStalled Health = "stalled"
	// HealthBlocked means the agent asked a question and is waiting for an
	// answer. Only a director, or a human through one, can clear it.
	HealthBlocked Health = "blocked"
	// HealthAbandoned means the process ended without the work reaching its
	// terminal progress value. This is the silent-failure case: an agent whose
	// terminal was closed mid-task looks exactly like one that finished, unless
	// something says otherwise.
	HealthAbandoned Health = "abandoned"
	// HealthComplete means the process ended having reached terminal progress.
	HealthComplete Health = "complete"
	// HealthUnknown means there is not enough signal to judge — the harness
	// could not say, or supplies no activity clock. It degrades toward stalled
	// rather than toward ok, because a needless nudge costs one turn and an
	// ignored dead agent costs the whole engagement.
	HealthUnknown Health = "unknown"
)

// AllHealths is every verdict the core can return, for callers that have to
// validate a health somebody typed. Kept beside the constants so a new one
// cannot be added without this list being in front of whoever adds it.
var AllHealths = []Health{
	HealthOK, HealthQuiet, HealthStalled, HealthBlocked,
	HealthAbandoned, HealthComplete, HealthUnknown,
}

// NeedsDirector reports whether this verdict calls for an action now. It is
// what `director status --unhealthy` filters on, and the shape of the check a
// director should be running every turn.
func (h Health) NeedsDirector() bool {
	switch h {
	case HealthStalled, HealthBlocked, HealthAbandoned, HealthComplete:
		return true
	default:
		return false
	}
}

// healthInput is everything the verdict depends on, gathered in one place so
// that the derivation below is a pure function and can be tested by moving a
// clock rather than by sleeping.
type healthInput struct {
	lifecycle      harness.Lifecycle
	progress       string
	task           Task
	hasPendingAsk  bool
	lastReportAt   time.Time
	lastActivityAt time.Time
	startedAt      time.Time
	now            time.Time
}

// deriveHealth is the whole verdict, in the order the questions actually
// matter: a blocked agent needs an answer whatever else is true; a finished one
// is judged on whether it finished the work rather than on how it exited; and
// only a live one can be judged on silence.
func deriveHealth(in healthInput) Health {
	// A pending question outranks everything. The agent has stopped on purpose
	// and no amount of waiting or nudging will move it.
	if in.hasPendingAsk {
		return HealthBlocked
	}

	if in.lifecycle == harness.LifecycleBlocked {
		return HealthBlocked
	}

	// The process is gone. The only question left is whether the work got
	// where it was going, which is the progress axis's business and not the
	// lifecycle's — the harness cannot distinguish a finished agent from one
	// whose terminal was closed, and this is where that distinction is made.
	if in.lifecycle == harness.LifecycleDone {
		if in.task.Terminal == "" {
			// The task declares no finish line, so "done" is all there is to
			// say and calling it abandoned would be an accusation we cannot
			// support.
			return HealthComplete
		}
		if in.progress == in.task.Terminal {
			return HealthComplete
		}
		return HealthAbandoned
	}

	// Still starting. Silence is expected and means nothing yet; the stall
	// threshold applies from startup so a spawn that never came up is still
	// eventually caught.
	if in.lifecycle == harness.LifecycleStarting {
		if in.now.Sub(in.startedAt) > in.task.StallThreshold() {
			return HealthStalled
		}
		return HealthOK
	}

	if in.lifecycle == harness.LifecycleUnknown {
		// The harness answered and does not know. Give it the same grace a
		// working engagement gets, then treat continued silence as a stall.
		if silentFor(in) > in.task.StallThreshold() {
			return HealthStalled
		}
		return HealthUnknown
	}

	// Alive and working or idle. This is where the reporting contract and the
	// activity clock earn their keep.
	if in.task.ReportOn.Never {
		// The workflow said not to expect reports, so silence carries no
		// information and only a dead activity clock is evidence of anything.
		if !in.lastActivityAt.IsZero() && in.now.Sub(in.lastActivityAt) > in.task.StallThreshold() {
			return HealthStalled
		}
		return HealthOK
	}

	overdue := reportOverdue(in)
	if !overdue {
		return HealthOK
	}

	// The report is late. Whether that matters depends entirely on whether the
	// harness can see the agent doing anything — this is the distinction that
	// separates an agent busy with a long tool call from one that is wedged,
	// and without it a director either nags healthy agents or ignores dead ones.
	if in.lastActivityAt.IsZero() {
		// No activity clock available. Not enough signal to say it is fine;
		// not enough to say it is stalled either, until the threshold passes.
		if silentFor(in) > in.task.StallThreshold() {
			return HealthStalled
		}
		return HealthUnknown
	}

	if in.now.Sub(in.lastActivityAt) > in.task.StallThreshold() {
		return HealthStalled
	}
	return HealthQuiet
}

// reportOverdue reports whether the agent has missed its heartbeat. A contract
// with no interval — progress-change only — can never be overdue on the clock,
// because there is no interval to be late against.
func reportOverdue(in healthInput) bool {
	if in.task.ReportOn.Every <= 0 {
		return false
	}
	return silentFor(in) > in.task.ReportOn.Every
}

// silentFor is how long since the agent last said anything, measured from spawn
// when it has never spoken at all — otherwise an agent that never reports would
// register as perfectly silent for zero seconds, forever.
func silentFor(in healthInput) time.Duration {
	since := in.lastReportAt
	if since.IsZero() {
		since = in.startedAt
	}
	if since.IsZero() {
		return 0
	}
	return in.now.Sub(since)
}
