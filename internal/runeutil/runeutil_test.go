package runeutil

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestCountGraphemesApprox pins the documented contract: runes count one
// each, combining marks are zero-width, and the count degrades gracefully on
// empty, long, and invalid UTF-8 input.
func TestCountGraphemesApprox(t *testing.T) {
	cases := []struct {
		name string
		s    string
		want int
	}{
		{"empty string", "", 0},
		{"ascii word", "hello", 5},
		{"ascii with spaces", "hello world", 11},
		{"single rune", "é", 1},
		{"precomposed accent", "café", 4},
		{"decomposed accent", "cafe\u0301", 4},
		{"mark after letter", "e\u0301", 1},
		{"multiple marks", "e\u0301\u0301", 1},
		{"nonspacing mark zero width", "a\u0327", 1},
		{"enclosing mark zero width", "a\u20DD", 1},
		{"spacing mark zero width", "\u0915\u093E", 1},
		{"leading mark", "\u0301a", 1},
		{"mark alone", "\u0301", 0},
		{"multibyte runes", "日本語", 3},
		{"mixed script", "héllo", 5},
		{"control characters count", "a\nb\t", 4},
		{"emoji zwj counts per rune", "\U0001F468\u200D\U0001F469\u200D\U0001F467", 5},
		{"invalid utf8 becomes replacement", "a\xffb", 3},
		{"long input", strings.Repeat("x", 1000), 1000},
	}
	for _, c := range cases {
		if got := CountGraphemesApprox(c.s); got != c.want {
			t.Errorf("CountGraphemesApprox(%q) = %d, want %d", c.s, got, c.want)
		}
	}
}

// FuzzCountGraphemesApprox checks the count invariants over arbitrary input:
// it never underflows, never exceeds the rune count, and never panics.
func FuzzCountGraphemesApprox(f *testing.F) {
	for _, seed := range []string{"", "hello", "cafe\u0301", "\u0915\u093E", "\U0001F468\u200D\U0001F469"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got := CountGraphemesApprox(s)
		if got < 0 {
			t.Fatalf("CountGraphemesApprox(%q) = %d, want >= 0", s, got)
		}
		if got > utf8.RuneCountInString(s) {
			t.Fatalf("CountGraphemesApprox(%q) = %d, want <= rune count %d", s, got, utf8.RuneCountInString(s))
		}
	})
}
