package timeutil

import (
	"math"
	"testing"
	"time"
)

// TestHumanDuration covers zero, whole units, composite units, sub-second
// precision, negative durations, and the extreme int64 duration values.
func TestHumanDuration(t *testing.T) {
	cases := []struct {
		name string
		in   time.Duration
		want string
	}{
		{"zero", 0, "0s"},
		{"seconds", 45 * time.Second, "45s"},
		{"hours and minutes", 2*time.Hour + 30*time.Minute, "2h30m"},
		{"minutes and seconds", 90 * time.Second, "1m30s"},
		{"whole hour", time.Hour, "1h"},
		{"whole day", 24 * time.Hour, "24h"},
		{"fractional seconds", 1500 * time.Millisecond, "1.5s"},
		{"milliseconds only", 100 * time.Millisecond, "100ms"},
		{"fractional milliseconds", 1500 * time.Microsecond, "1.5ms"},
		{"microseconds only", 100 * time.Microsecond, "100µs"},
		{"fractional microseconds", 2500 * time.Nanosecond, "2.5µs"},
		{"nanoseconds only", time.Nanosecond, "1ns"},
		{"minute with sub-second", time.Minute + 30*time.Millisecond, "1m30ms"},
		{"hour with fractional seconds", time.Hour + time.Second + time.Millisecond, "1h1.001s"},
		{"negative seconds", -45 * time.Second, "-45s"},
		{"negative composite", -(2*time.Hour + 30*time.Minute), "-2h30m"},
		{"negative sub-second", -500 * time.Millisecond, "-500ms"},
		{"max duration", time.Duration(math.MaxInt64), "2562047h47m16.854775807s"},
		{"min duration", time.Duration(math.MinInt64), "-2562047h47m16.854775808s"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := HumanDuration(c.in); got != c.want {
				t.Errorf("HumanDuration(%v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
