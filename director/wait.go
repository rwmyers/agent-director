package director

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrWaitTimeout is returned when Wait gave up before anything matched. It is a
// distinct error rather than a nil transition so that a caller — and the exit
// code a caller branches on — can tell "nothing happened" from "something
// happened and it was this".
var ErrWaitTimeout = errors.New("timed out waiting for a matching engagement")

// ErrHostCannotWait is returned when the director's own host cannot background
// a blocking wait.
//
// It is a refusal rather than a warning because the failure it prevents is
// total and silent. A director that blocks where it cannot background is gone:
// it is not doing anything, it is not answering the person, and there is
// nothing in the conversation to say why. Declining costs it one turn — it
// checks `director status` itself, which is what it does today — and the whole
// asymmetry between the two mistakes is why the bits default to false.
var ErrHostCannotWait = errors.New("this director's host cannot background a wait")

// DefaultWaitUntil is the set of healths worth waking a consumer for: exactly
// those where Health.NeedsDirector is true. Waiting on "ok" or "quiet" would
// return on ordinary progress, which is what watch is for.
var DefaultWaitUntil = []Health{HealthBlocked, HealthComplete, HealthAbandoned, HealthStalled}

// WaitOptions configures a single blocking wait.
type WaitOptions struct {
	// Until is the set of healths to return on. Empty means DefaultWaitUntil.
	Until []Health
	// Engagements narrows the wait to specific engagement ids. Empty means any.
	Engagements []string
	// Interval is how often the harnesses are polled, as for WatchOptions.
	Interval time.Duration
	// Timeout gives up after this long. Zero waits forever.
	Timeout time.Duration
	// Force waits anyway on a host that does not declare it can background one.
	//
	// It exists because detection is ambient and can be wrong, and somebody who
	// knows better must not be stuck behind an adapter's declaration with no
	// way past it. The lasting fix is `host` in director.conf; this is the way
	// through for one command.
	Force bool
}

// Wait blocks until an engagement reaches one of the healths asked for, and
// returns the transition that got it there.
//
// This is the transition feed with a stopping condition, not a second way of
// observing the fleet: it consumes Watch, so the two can never disagree about
// what a change is or when it happened.
//
// The feed is started with IncludeInitial set, so an engagement that is already
// blocked when Wait is called matches straight away. A consumer that only saw
// subsequent changes would block forever on the exact state it was waiting for
// — an already-answered question is not going to transition into being asked
// again.
func (d *Director) Wait(ctx context.Context, opts WaitOptions) (Transition, error) {
	host := d.currentHost()
	if !opts.Force && !host.Hosting.Background {
		return Transition{}, fmt.Errorf("%w: %s.\n\n%s", ErrHostCannotWait, host.Describe(), waitRefusalAdvice)
	}

	until, err := healthSet(opts.Until)
	if err != nil {
		return Transition{}, err
	}

	only := map[string]bool{}
	for _, id := range opts.Engagements {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		// A misspelled id would otherwise wait forever on an engagement that
		// cannot ever transition, which looks identical to patience.
		if _, ok := d.State.Engagements[id]; !ok {
			return Transition{}, fmt.Errorf("%w: no engagement %q", ErrNotFound, id)
		}
		only[id] = true
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var expired <-chan time.Time
	if opts.Timeout > 0 {
		timer := time.NewTimer(opts.Timeout)
		defer timer.Stop()
		expired = timer.C
	}

	feed, err := d.Watch(ctx, WatchOptions{Interval: opts.Interval, IncludeInitial: true})
	if err != nil {
		return Transition{}, err
	}

	for {
		select {
		case transition, ok := <-feed:
			if !ok {
				if err := ctx.Err(); err != nil {
					return Transition{}, err
				}
				return Transition{}, errors.New("the transition feed closed unexpectedly")
			}
			if len(only) > 0 && !only[transition.Engagement] {
				continue
			}
			if !until[transition.Health] {
				continue
			}
			return transition, nil
		case <-expired:
			return Transition{}, fmt.Errorf("%w after %s", ErrWaitTimeout, opts.Timeout)
		case <-ctx.Done():
			return Transition{}, ctx.Err()
		}
	}
}

// healthSet validates the healths to wait on and returns them as a set. An
// unknown value is refused rather than dropped: a caller who typed one wrong
// would otherwise wait on a condition that can never occur.
func healthSet(healths []Health) (map[Health]bool, error) {
	if len(healths) == 0 {
		healths = DefaultWaitUntil
	}
	set := map[Health]bool{}
	for _, health := range healths {
		parsed, err := ParseHealth(string(health))
		if err != nil {
			return nil, err
		}
		set[parsed] = true
	}
	return set, nil
}

// ParseHealth turns a caller's string into a Health, naming the valid set when
// it cannot.
func ParseHealth(value string) (Health, error) {
	candidate := Health(strings.TrimSpace(value))
	for _, known := range AllHealths {
		if candidate == known {
			return known, nil
		}
	}
	names := make([]string, 0, len(AllHealths))
	for _, known := range AllHealths {
		names = append(names, string(known))
	}
	return "", fmt.Errorf("unknown health %q: valid values are %s", value, strings.Join(names, ", "))
}

// waitRefusalAdvice is what to do instead. It names the one thing that works
// everywhere — checking on your own turn — before the two ways to change the
// verdict, because a director reading this needs a plan for right now more than
// it needs a configuration key.
const waitRefusalAdvice = `Blocking here would leave this conversation unreachable with nothing to say why.
Check the fleet on your own turn instead:

    director status --unhealthy

and tell the person plainly that nothing will be looked at until they prompt you.
If this host really can background a command, say so once in director.conf:

    host = <harness>

or pass --force to wait anyway this time.`
