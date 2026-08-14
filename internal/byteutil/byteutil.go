// Package byteutil provides small, dependency-free helpers for formatting
// byte counts for humans. It is a leaf package: it imports only the standard
// library.
package byteutil

import (
	"fmt"
	"strings"
)

// sizeUnit names one base-1024 unit and its size in bytes.
type sizeUnit struct {
	size uint64
	name string
}

// humanUnits lists the base-1024 unit names from largest to smallest. The
// largest value an int64 can hold is just under 8 EiB, so EB is the largest
// unit needed.
var humanUnits = []sizeUnit{
	{1 << 60, "EB"},
	{1 << 50, "PB"},
	{1 << 40, "TB"},
	{1 << 30, "GB"},
	{1 << 20, "MB"},
	{1 << 10, "KB"},
}

// HumanSize formats a byte count as a short human-readable string using
// base-1024 units: "512B", "1.5KB", "3.2MB". Values below one kibibyte render
// as an integer byte count; values at or above it render with one fractional
// digit, and a trailing ".0" is dropped, so 1024 renders as "1KB". Negative
// counts render with a leading "-". HumanSize(0) returns "0B".
func HumanSize(n int64) string {
	// Magnitude is carried in uint64 so the negation of MinInt64 cannot
	// overflow: uint64(-(n+1))+1 equals |n| for every negative n.
	var (
		neg bool
		u   uint64
	)
	if n < 0 {
		neg = true
		u = uint64(-(n + 1)) + 1
	} else {
		u = uint64(n)
	}
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	if u < 1<<10 {
		fmt.Fprintf(&b, "%dB", u)
		return b.String()
	}
	for _, unit := range humanUnits {
		if u >= unit.size {
			fmt.Fprintf(&b, "%s%s", oneDecimal(float64(u)/float64(unit.size)), unit.name)
			return b.String()
		}
	}
	return "0B"
}

// oneDecimal formats v with one fractional digit, dropping a trailing ".0".
func oneDecimal(v float64) string {
	return strings.TrimSuffix(fmt.Sprintf("%.1f", v), ".0")
}
