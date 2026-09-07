package cli

import (
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/spf13/cobra"
)

// parseDuration reads a duration flag value, treating a bare number as seconds.
//
// Everything Go's own parser accepts is passed straight through and keeps its
// meaning: "30s", "5m", "1h30m", "500ms", and the bare "0" it already allows.
// Only what it rejects is reconsidered, and only when the rejected value is a
// plain number — so nothing that works today can change, and "30" stops being
// an error nobody had a reason to want.
//
// Fractions are accepted, because "1.5s" already parses and a bare number that
// refused the same fraction would be a rule with a hole in it. Negatives are
// accepted for the same reason: "-5s" parses today, so "-5" means the same
// thing, and whether a given command has any use for a negative duration is its
// own business and unchanged.
func parseDuration(value string) (time.Duration, error) {
	parsed, err := time.ParseDuration(value)
	if err == nil {
		return parsed, nil
	}
	if !isBareNumber(value) {
		return 0, err
	}
	seconds, convErr := strconv.ParseFloat(value, 64)
	if convErr != nil {
		return 0, err
	}
	nanos := seconds * float64(time.Second)
	if nanos >= float64(math.MaxInt64) || nanos <= float64(math.MinInt64) {
		return 0, fmt.Errorf("time: out of range in duration %q", value)
	}
	return time.Duration(nanos), nil
}

// isBareNumber reports whether value is a plain decimal number: an optional
// sign, then digits and at most one decimal point, with at least one digit.
//
// Deliberately narrower than strconv.ParseFloat, which also takes "1e9",
// "0x1p3", "Inf" and "NaN". None of those is a duration somebody typed on
// purpose, and the point of accepting bare numbers is to stop rejecting the
// obvious, not to start accepting typos.
func isBareNumber(value string) bool {
	if value == "" {
		return false
	}
	if value[0] == '+' || value[0] == '-' {
		value = value[1:]
	}
	digits, points := 0, 0
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r == '.':
			points++
		default:
			return false
		}
	}
	return digits > 0 && points <= 1
}

// durationValue is the pflag.Value behind every duration flag in this command
// tree. One type so the four flags cannot drift into meaning different things.
type durationValue struct{ target *time.Duration }

func (v *durationValue) Set(raw string) error {
	parsed, err := parseDuration(raw)
	if err != nil {
		return err
	}
	*v.target = parsed
	return nil
}

func (v *durationValue) Type() string { return "duration" }

// String renders the value the way it would be typed back in. Zero renders as
// "0" rather than "0s" because pflag reads that spelling as "this default is
// the zero value" and leaves it out of --help, which is what the built-in
// duration flag did and what the usage lines here already say in words.
func (v *durationValue) String() string {
	if *v.target == 0 {
		return "0"
	}
	return v.target.String()
}

// durationFlag registers a duration flag that also accepts a bare number of
// seconds, and says so in --help. A drop-in for cmd.Flags().DurationVar.
func durationFlag(cmd *cobra.Command, target *time.Duration, name string, value time.Duration, usage string) {
	*target = value
	cmd.Flags().Var(&durationValue{target: target}, name, usage+" (a bare number means seconds)")
}
