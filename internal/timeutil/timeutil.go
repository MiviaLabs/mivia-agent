// Package timeutil provides small, dependency-free helpers for formatting
// time.Duration values for humans. It is a leaf package: it imports only the
// standard library.
package timeutil

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// HumanDuration formats d as a short human-readable string such as "2h30m",
// "1m30s", or "45s". It keeps only the non-zero components: a duration of
// exactly two and a half hours renders as "2h30m", not "2h30m0s". Hours are
// the largest unit, so a day renders as "24h".
//
// Sub-second precision is preserved in the largest unit that fits: 1500ms
// renders as "1.5s", 1500µs as "1.5ms", and 2500ns as "2.5µs". Negative
// durations render with a leading "-". HumanDuration(0) returns "0s", and the
// extreme values time.Duration(math.MinInt64) and time.Duration(math.MaxInt64)
// render without overflow.
func HumanDuration(d time.Duration) string {
	if d == 0 {
		return "0s"
	}
	// Magnitude is carried in uint64 so the negation of MinInt64 cannot
	// overflow: uint64(-(d+1))+1 equals |d| for every negative d.
	var (
		neg bool
		u   uint64
	)
	if d < 0 {
		neg = true
		u = uint64(-(d + 1)) + 1
	} else {
		u = uint64(d)
	}
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	sec := uint64(time.Second)
	h := u / uint64(time.Hour)
	u %= uint64(time.Hour)
	m := u / uint64(time.Minute)
	u %= uint64(time.Minute)
	s := u / sec
	u %= sec
	if h > 0 {
		fmt.Fprintf(&b, "%dh", h)
	}
	if m > 0 {
		fmt.Fprintf(&b, "%dm", m)
	}
	if s > 0 {
		fmt.Fprintf(&b, "%d", s)
		if u > 0 {
			writeFraction(&b, u, sec)
		}
		b.WriteByte('s')
	}
	if s == 0 && u > 0 {
		writeSubSecond(&b, u)
	}
	return b.String()
}

// writeFraction appends the decimal fraction of unit that rem represents,
// trimming trailing zeros: rem=500_000_000 with unit=1s appends ".5", and
// rem=1 with unit=1s appends ".000000001". rem must satisfy 0 < rem < unit.
func writeFraction(b *strings.Builder, rem, unit uint64) {
	digits := strconv.FormatUint(rem, 10)
	places := 0
	for u := unit; u > 1; u /= 10 {
		places++
	}
	for len(digits) < places {
		digits = "0" + digits
	}
	b.WriteByte('.')
	b.WriteString(strings.TrimRight(digits, "0"))
}

// writeSubSecond appends the sub-second remainder u (0 < u < 1s) in the
// largest unit it fits, with a fractional part when needed: 100ms stays
// "100ms", but 1500µs renders as "1.5ms" and 2500ns as "2.5µs".
func writeSubSecond(b *strings.Builder, u uint64) {
	switch {
	case u >= uint64(time.Millisecond):
		writeUnit(b, u, uint64(time.Millisecond), "ms")
	case u >= uint64(time.Microsecond):
		writeUnit(b, u, uint64(time.Microsecond), "µs")
	default:
		writeUnit(b, u, 1, "ns")
	}
}

// writeUnit appends u as a quantity of unit with the given suffix, adding a
// fractional part when u is not a whole number of units.
func writeUnit(b *strings.Builder, u, unit uint64, suffix string) {
	whole := u / unit
	rem := u % unit
	fmt.Fprintf(b, "%d", whole)
	if rem > 0 {
		writeFraction(b, rem, unit)
	}
	b.WriteString(suffix)
}
