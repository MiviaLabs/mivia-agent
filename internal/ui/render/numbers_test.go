package render

import (
	"math"
	"testing"
)

func TestCompactTokens(t *testing.T) {
	for _, tc := range []struct {
		n    int64
		want string
	}{{999, "999"}, {1000, "1.0k"}, {1284, "1.3k"}, {2940, "2.9k"}} {
		if got := CompactTokens(tc.n); got != tc.want {
			t.Errorf("CompactTokens(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

// TestGroupThousands pins the comma grouping used by other render surfaces
// (ux-rules.md 11.8, wireframes-panes.md section 4 grammar).
func TestGroupThousands(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{1, "1"},
		{340, "340"},           // under a thousand is never grouped
		{999, "999"},           //
		{1000, "1,000"},        // first group
		{1284, "1,284"},        // the footer's own example
		{12345, "12,345"},      //
		{123456, "123,456"},    //
		{1000000, "1,000,000"}, // two groups
		{-340, "-340"},         // negatives keep the sign
		{-1284, "-1,284"},      //
		{math.MinInt, "-9,223,372,036,854,775,808"}, // negation must not overflow
	}
	for _, c := range cases {
		if got := GroupThousands(c.n); got != c.want {
			t.Errorf("GroupThousands(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}
