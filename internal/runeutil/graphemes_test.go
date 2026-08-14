package runeutil

import "testing"

// TestCountGraphemesApprox covers empty input, ASCII text, combining marks
// counted as zero-width, mark-only input, multi-byte runes, and the documented
// approximation for sequences outside full UAX #29 grapheme segmentation.
func TestCountGraphemesApprox(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"empty", "", 0},
		{"ascii", "hello", 5},
		{"ascii with space", "a b", 3},
		{"precomposed accent", "café", 4},
		{"combining mark", "cafe\u0301", 4},
		{"combining mark mid string", "a\u0300b", 2},
		{"combining mark at start", "\u0301a", 1},
		{"combining mark at end", "a\u0301", 1},
		{"marks only", "\u0301\u0300", 0},
		{"non-latin runes", "日本語", 3},
		{"emoji", "😀", 1},
		{"emoji with zwj counts per rune", "\U0001F469\u200d\U0001F4BB", 3},
		{"skin tone modifier counts", "\U0001F44D\U0001F3FD", 2},
		{"invalid utf8 replacement", "a\xffb", 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := CountGraphemesApprox(c.in); got != c.want {
				t.Errorf("CountGraphemesApprox(%q) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}
