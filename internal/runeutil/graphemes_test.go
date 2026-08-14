package runeutil

import (
	"strings"
	"testing"
)

// TestCountGraphemesApprox covers empty input, plain ASCII, single-rune and
// multi-codepoint emoji, precomposed and combining accented text, zero-width
// combining marks, CJK runes, invalid UTF-8, and oversized input.
func TestCountGraphemesApprox(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"empty", "", 0},
		{"single ascii", "a", 1},
		{"ascii word", "hello", 5},
		{"spaces and punctuation", "a b, c.", 7},
		{"newline and tab", "\n\t", 2},
		{"precomposed accented", "café", 4},
		{"combining accent", "cafe\u0301", 4},
		{"multiple combining marks", "a\u0301\u0301\u0301", 1},
		{"only combining marks", "\u0301\u0301", 0},
		{"combining keycap sequence", "1\uFE0F\u20E3", 1},
		{"emoji single rune", "😀", 1},
		{"family emoji raw runes", "👨\u200D👩\u200D👧\u200D👦", 7},
		{"cjk runes", "漢字", 2},
		{"invalid utf8 byte", "\xff", 1},
		{"invalid utf8 bytes", "\xff\xfe", 2},
		{"mixed valid and invalid", "a\xffb", 3},
		{"oversized ascii", strings.Repeat("a", 1<<20), 1 << 20},
		{"oversized with combining marks", strings.Repeat("a\u0301", 1<<19), 1 << 19},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := CountGraphemesApprox(c.in); got != c.want {
				t.Errorf("CountGraphemesApprox(%q) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

// TestCountGraphemesApproxNeverNegative checks that any input, including
// invalid UTF-8, produces a non-negative count.
func TestCountGraphemesApproxNeverNegative(t *testing.T) {
	for _, in := range []string{"", "a", "\x80\x81\xff", strings.Repeat("\u0301", 64)} {
		if got := CountGraphemesApprox(in); got < 0 {
			t.Errorf("CountGraphemesApprox(%q) = %d, want >= 0", in, got)
		}
	}
}
