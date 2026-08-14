package runeutil

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// TestCountGraphemesApprox covers empty input, ASCII, precomposed and
// decomposed accents, mark-only input, stacked marks, multi-byte scripts,
// emoji, variation selectors, zero-width joiners, and invalid UTF-8.
func TestCountGraphemesApprox(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"empty", "", 0},
		{"ascii word", "hello", 5},
		{"precomposed accent", "café", 4},
		{"decomposed accent", "cafe\u0301", 4},
		{"only combining marks", "\u0301\u0308", 0},
		{"mark after letter", "e\u0301", 1},
		{"stacked marks", "a\u0301\u0301", 1},
		{"cjk ideographs", "日本語", 3},
		{"single emoji", "🚀", 1},
		{"emoji with variation selector", "❤\ufe0f", 1},
		{"zwj family emoji", "👨\u200d👩\u200d👧\u200d👦", 7},
		{"invalid utf-8 bytes", "\xff\xfe", 2},
		{"mixed valid and invalid", "a\xffb", 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := CountGraphemesApprox(c.in); got != c.want {
				t.Errorf("CountGraphemesApprox(%q) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

// FuzzCountGraphemesApprox checks the invariant properties of the
// approximate count: it never exceeds the rune count, never goes negative,
// and equals the rune count whenever the input has no marks.
func FuzzCountGraphemesApprox(f *testing.F) {
	f.Add("")
	f.Add("hello")
	f.Add("café")
	f.Add("cafe\u0301")
	f.Add("👨\u200d👩\u200d👧\u200d👦")
	f.Add("\xff\xfe")
	f.Fuzz(func(t *testing.T, s string) {
		got := CountGraphemesApprox(s)
		runes := utf8.RuneCountInString(s)
		if got < 0 || got > runes {
			t.Fatalf("CountGraphemesApprox(%q) = %d, want in [0, %d]", s, got, runes)
		}
		if strings.IndexFunc(s, unicode.IsMark) == -1 && got != runes {
			t.Fatalf("CountGraphemesApprox(%q) = %d, want %d (no marks)", s, got, runes)
		}
	})
}
