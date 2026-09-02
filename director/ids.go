package director

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// Identifier prefixes. They are visible in state files, in `director status`,
// and in the environment of every spawned agent, so a stray identifier in a log
// says what kind of thing it names without a lookup.
const (
	directorPrefix   = "dir_"
	engagementPrefix = "eng_"
	askPrefix        = "ask_"
)

// newID mints a prefixed random identifier. Sixteen hex characters is 64 bits,
// which is far more than enough for the number of engagements a machine will
// ever hold, and short enough that a human can compare two by eye.
func newID(prefix string) (string, error) {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("minting an identifier: %w", err)
	}
	return prefix + hex.EncodeToString(raw), nil
}

// newToken mints an engagement's callback secret. It is injected into the
// agent's environment and required by report and ask, so that an agent can only
// speak for itself: without it, one confused agent could report progress or
// answer questions on behalf of a sibling, and the director would have no way
// to tell that it had happened.
func newToken() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("minting a token: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

// Clock is the source of now. Health is a function of elapsed time, so tests
// have to be able to move time without sleeping; every path that asks what time
// it is goes through here.
type Clock func() time.Time

// SystemClock is the default.
func SystemClock() time.Time { return time.Now() }

func (d *Director) now() time.Time {
	if d.Clock == nil {
		return time.Now()
	}
	return d.Clock()
}

// formatTime renders a timestamp for a state file. RFC3339 in UTC, so two state
// files written in different timezones still sort and compare.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// parseTime reads a timestamp from a state file. An unparseable or absent value
// is the zero time rather than an error: a corrupt timestamp should degrade one
// engagement's health to unknown, not make the whole fleet unreadable.
func parseTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

// formatBool renders a flag for a state file. The pair with the read side is
// deliberately asymmetric: only "true" reads back as true, so a value nothing
// wrote — a state file from an older director, a hand edit — leaves the
// conservative answer in place.
func formatBool(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
