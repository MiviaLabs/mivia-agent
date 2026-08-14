package byteutil

import (
	"math"
	"testing"
)

// TestHumanSize covers zero, integer byte counts, unit boundaries, fractional
// values, rounding, negative counts, and the extreme int64 values.
func TestHumanSize(t *testing.T) {
	cases := []struct {
		name string
		in   int64
		want string
	}{
		{"zero", 0, "0B"},
		{"one byte", 1, "1B"},
		{"below kibibyte", 1023, "1023B"},
		{"kibibyte boundary", 1024, "1KB"},
		{"one and a half kiB", 1536, "1.5KB"},
		{"rounded kiB", 1600, "1.6KB"},
		{"mebibyte boundary", 1024 * 1024, "1MB"},
		{"three mebibytes", 3 * 1024 * 1024, "3MB"},
		{"decimal mebibytes", 3355443, "3.2MB"},
		{"gibibyte boundary", 1024 * 1024 * 1024, "1GB"},
		{"one and a half GiB", 3 * 1024 * 1024 * 1024 / 2, "1.5GB"},
		{"tebibyte boundary", 1024 * 1024 * 1024 * 1024, "1TB"},
		{"max int64", math.MaxInt64, "8EB"},
		{"negative bytes", -1024, "-1KB"},
		{"negative fractional", -1536, "-1.5KB"},
		{"min int64", math.MinInt64, "-8EB"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := HumanSize(c.in); got != c.want {
				t.Errorf("HumanSize(%d) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
