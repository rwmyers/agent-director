package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestParseDuration(t *testing.T) {
	t.Parallel()

	// Every form that parses today, with the value it has today. These are the
	// invocations that already work, and none of them may move.
	unchanged := []struct {
		value string
		want  time.Duration
	}{
		{"30s", 30 * time.Second},
		{"5m", 5 * time.Minute},
		{"1h30m", 90 * time.Minute},
		{"500ms", 500 * time.Millisecond},
		{"1.5s", 1500 * time.Millisecond},
		{"-5s", -5 * time.Second},
		{"0", 0},
		{"0s", 0},
		{"-0", 0},
	}
	for _, tc := range unchanged {
		t.Run("unchanged "+tc.value, func(t *testing.T) {
			t.Parallel()
			// Guard the premise: it parsed before, so this case is about
			// keeping a meaning, not gaining one.
			if _, err := time.ParseDuration(tc.value); err != nil {
				t.Fatalf("time.ParseDuration(%q) = %v, want no error", tc.value, err)
			}
			got, err := parseDuration(tc.value)
			if err != nil {
				t.Fatalf("parseDuration(%q) = %v, want no error", tc.value, err)
			}
			if got != tc.want {
				t.Errorf("parseDuration(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}

	// Bare numbers: all of these are parse errors today, which is what makes
	// giving them a meaning safe.
	bare := []struct {
		value string
		want  time.Duration
	}{
		{"30", 30 * time.Second},
		{"1", time.Second},
		{"00", 0},
		{"0.0", 0},
		{"1.5", 1500 * time.Millisecond},
		{".5", 500 * time.Millisecond},
		{"+30", 30 * time.Second},
		{"-5", -5 * time.Second},
		{"3600", time.Hour},
	}
	for _, tc := range bare {
		t.Run("bare "+tc.value, func(t *testing.T) {
			t.Parallel()
			if _, err := time.ParseDuration(tc.value); err == nil {
				t.Fatalf("time.ParseDuration(%q) succeeded, so this is not a newly accepted form", tc.value)
			}
			got, err := parseDuration(tc.value)
			if err != nil {
				t.Fatalf("parseDuration(%q) = %v, want no error", tc.value, err)
			}
			if got != tc.want {
				t.Errorf("parseDuration(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}

	// Still errors, and for the same reason as before: a typo must not become a
	// duration because it happens to start with a digit.
	for _, value := range []string{
		"",
		"abc",
		"30x",
		"30ss",
		"3 0",
		" 30",
		"30 ",
		"1e3",
		"0x10",
		"Inf",
		"NaN",
		"1.2.3",
		"--30",
		"30s5",
		"9999999999999",
	} {
		t.Run("invalid "+value, func(t *testing.T) {
			t.Parallel()
			got, err := parseDuration(value)
			if err == nil {
				t.Fatalf("parseDuration(%q) = %v, want an error", value, got)
			}
			// The message stays a duration message, so a caller reading it
			// learns the same thing it learned before.
			if !strings.Contains(err.Error(), "duration") {
				t.Errorf("parseDuration(%q) error = %q, want it to mention duration", value, err)
			}
		})
	}
}

// TestDurationFlagsAcceptBareSeconds walks the shipped commands, so this
// cannot pass by testing a flag nobody registers.
func TestDurationFlagsAcceptBareSeconds(t *testing.T) {
	t.Parallel()

	commands := []struct {
		name  string
		cmd   *cobra.Command
		flags []string
	}{
		{"ask", newAskCmd(), []string{"timeout"}},
		{"wait", newWaitCmd(), []string{"timeout", "interval"}},
		{"watch", newWatchCmd(), []string{"interval"}},
	}

	for _, tc := range commands {
		for _, name := range tc.flags {
			t.Run(tc.name+" --"+name, func(t *testing.T) {
				t.Parallel()
				flag := tc.cmd.Flags().Lookup(name)
				if flag == nil {
					t.Fatalf("%s has no --%s; this test is watching the wrong flag", tc.name, name)
				}
				if flag.Value.Type() != "duration" {
					t.Fatalf("--%s is a %s, want a duration", name, flag.Value.Type())
				}
				if err := flag.Value.Set("30"); err != nil {
					t.Fatalf("--%s=30: %v", name, err)
				}
				if got := flag.Value.String(); got != "30s" {
					t.Errorf("--%s=30 is %s, want 30s", name, got)
				}
				if err := flag.Value.Set("5m"); err != nil {
					t.Fatalf("--%s=5m: %v", name, err)
				}
				if got := flag.Value.String(); got != "5m0s" {
					t.Errorf("--%s=5m is %s, want 5m0s", name, got)
				}
				if err := flag.Value.Set("30x"); err == nil {
					t.Errorf("--%s=30x was accepted, want an error", name)
				}
				if !strings.Contains(flag.Usage, "bare number means seconds") {
					t.Errorf("--%s usage = %q, want it to mention bare seconds", name, flag.Usage)
				}
			})
		}
	}
}

// TestDurationFlagWiring checks the whole path a user takes: the flag is parsed
// off a command line into the variable the command reads.
func TestDurationFlagWiring(t *testing.T) {
	t.Parallel()

	var got time.Duration
	cmd := &cobra.Command{Use: "x", RunE: func(*cobra.Command, []string) error { return nil }}
	durationFlag(cmd, &got, "timeout", time.Hour, "how long to wait")

	if got != time.Hour {
		t.Fatalf("default = %v, want 1h", got)
	}
	if err := cmd.Flags().Parse([]string{"--timeout", "30"}); err != nil {
		t.Fatalf("Parse(--timeout 30) = %v", err)
	}
	if want := 30 * time.Second; got != want {
		t.Errorf("--timeout 30 = %v, want %v", got, want)
	}

	if err := cmd.Flags().Parse([]string{"--timeout=oops"}); err == nil {
		t.Fatal("--timeout=oops was accepted, want an error")
	}
}

// TestZeroDefaultStaysOutOfHelp pins the cosmetic reason durationValue renders
// zero as "0": pflag prints "(default 0s)" otherwise, next to a usage line that
// already explains what zero means.
func TestZeroDefaultStaysOutOfHelp(t *testing.T) {
	t.Parallel()

	var target time.Duration
	cmd := &cobra.Command{Use: "x"}
	durationFlag(cmd, &target, "timeout", 0, "give up after this long, or 0 to wait forever")

	usage := cmd.Flags().FlagUsages()
	if strings.Contains(usage, "default") {
		t.Errorf("FlagUsages() = %q, want no default for a zero duration", usage)
	}
	if !strings.Contains(usage, "bare number means seconds") {
		t.Errorf("FlagUsages() = %q, want the bare-number note", usage)
	}
}
