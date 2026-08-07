package textutil

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestTruncateRuneSafeShortStringUnchanged checks that input at or below the
// byte limit is returned unchanged.
func TestTruncateRuneSafeShortStringUnchanged(t *testing.T) {
	cases := []struct {
		s   string
		max int
	}{
		{"", 10},
		{"a", 1},
		{"héllo", 10},
		{"日本語", 9},
		{"exact", 5},
	}
	for _, c := range cases {
		if got := TruncateRuneSafe(c.s, c.max); got != c.s {
			t.Errorf("TruncateRuneSafe(%q, %d) = %q, want unchanged %q", c.s, c.max, got, c.s)
		}
	}
}

// TestTruncateRuneSafeCutAtRuneBoundary checks that a cut that lands exactly on
// a UTF-8 rune start keeps the whole rune: the result is intact, not spliced.
func TestTruncateRuneSafeCutAtRuneBoundary(t *testing.T) {
	cases := []struct {
		s    string
		max  int
		want string
	}{
		{"héllo wörld", 3, "hé"},  // h (1) + é (2) = 3 bytes
		{"héllo wörld", 4, "hél"}, // next byte starts a new rune
		{"日本語", 6, "日本"},          // 3-byte runes: cut after 2 runes
		{"a🙂b", 5, "a🙂"},          // 4-byte rune: cut after it
		{"héllo", 3, "hé"},
	}
	for _, c := range cases {
		if got := TruncateRuneSafe(c.s, c.max); got != c.want {
			t.Errorf("TruncateRuneSafe(%q, %d) = %q, want %q", c.s, c.max, got, c.want)
		}
	}
}

// TestTruncateRuneSafeMidRune checks that a cut inside a rune backs off to a
// rune start: the result is valid UTF-8 and a byte prefix of the original.
func TestTruncateRuneSafeMidRune(t *testing.T) {
	cases := []struct {
		s    string
		max  int
		want string
	}{
		{"héllo", 2, "h"}, // cut inside é (2-byte rune)
		{"日本語", 4, "日"},   // cut inside 本 (3-byte rune)
		{"日本語", 2, ""},    // cut inside 日; back off to start of string
		{"a🙂b", 3, "a"},   // cut inside 🙂 (4-byte rune)
	}
	for _, c := range cases {
		got := TruncateRuneSafe(c.s, c.max)
		if got != c.want {
			t.Errorf("TruncateRuneSafe(%q, %d) = %q, want %q", c.s, c.max, got, c.want)
		}
		if !utf8.ValidString(got) {
			t.Errorf("TruncateRuneSafe(%q, %d) = %q, not valid UTF-8", c.s, c.max, got)
		}
		if !strings.HasPrefix(c.s, got) {
			t.Errorf("TruncateRuneSafe(%q, %d) = %q, not a prefix of the original", c.s, c.max, got)
		}
	}
}

// TestTruncateRuneSafeASCII checks exact truncation of pure ASCII input.
func TestTruncateRuneSafeASCII(t *testing.T) {
	s := "abcdef"
	if got := TruncateRuneSafe(s, 3); got != "abc" {
		t.Errorf("TruncateRuneSafe(%q, 3) = %q, want %q", s, got, "abc")
	}
	if got := TruncateRuneSafe(s, 6); got != s {
		t.Errorf("TruncateRuneSafe(%q, 6) = %q, want unchanged", s, got)
	}
}

// TestTruncateRuneSafeProperties checks invariants across every byte limit:
// valid UTF-8, a prefix of the input, and never longer than the limit.
func TestTruncateRuneSafeProperties(t *testing.T) {
	inputs := []string{"", "a", "abc", "héllo", "héllo wörld", "日本語", "a🙂b", "e\u0301"}
	for _, s := range inputs {
		for max := 0; max <= len(s)+2; max++ {
			got := TruncateRuneSafe(s, max)
			if !utf8.ValidString(got) {
				t.Errorf("TruncateRuneSafe(%q, %d) = %q, not valid UTF-8", s, max, got)
			}
			if !strings.HasPrefix(s, got) {
				t.Errorf("TruncateRuneSafe(%q, %d) = %q, not a prefix", s, max, got)
			}
			if len(got) > max {
				t.Errorf("TruncateRuneSafe(%q, %d) = %q, length %d exceeds limit", s, max, got, len(got))
			}
		}
	}
}

// TestTruncateRuneSafeZeroAndNegativeMax checks the zero and negative limits.
func TestTruncateRuneSafeZeroAndNegativeMax(t *testing.T) {
	for _, s := range []string{"", "a", "héllo", "日本語"} {
		if got := TruncateRuneSafe(s, 0); got != "" {
			t.Errorf("TruncateRuneSafe(%q, 0) = %q, want empty", s, got)
		}
		if got := TruncateRuneSafe(s, -1); got != "" {
			t.Errorf("TruncateRuneSafe(%q, -1) = %q, want empty", s, got)
		}
	}
}

// TestTruncateTailCutAtRuneBoundary checks that a tail cut that lands exactly
// on a UTF-8 rune start keeps the whole rune.
func TestTruncateTailCutAtRuneBoundary(t *testing.T) {
	cases := []struct {
		s    string
		max  int
		want string
	}{
		{"héllo", 2, "lo"}, // last 2 bytes start at a rune boundary
		{"日本語", 3, "語"},    // last 3 bytes are one 3-byte rune
		{"日本語", 6, "本語"},
		{"a🙂b", 5, "🙂b"}, // 4-byte rune + 1 byte = 5 bytes
	}
	for _, c := range cases {
		if got := TruncateTail(c.s, c.max); got != c.want {
			t.Errorf("TruncateTail(%q, %d) = %q, want %q", c.s, c.max, got, c.want)
		}
	}
}

// TestTruncateTailMidRune checks that a tail window starting inside a rune
// advances forward to a rune start: valid UTF-8 and a byte suffix.
func TestTruncateTailMidRune(t *testing.T) {
	cases := []struct {
		s    string
		max  int
		want string
	}{
		{"héllo", 3, "llo"}, // window starts inside é; advance to l
		{"日本語", 5, "語"},     // window starts inside 本; advance to 語
		{"日本語", 2, ""},      // window holds only continuation bytes; empty suffix
		{"a🙂b", 3, "b"},     // window starts inside 🙂; advance to b
	}
	for _, c := range cases {
		got := TruncateTail(c.s, c.max)
		if got != c.want {
			t.Errorf("TruncateTail(%q, %d) = %q, want %q", c.s, c.max, got, c.want)
		}
		if !utf8.ValidString(got) {
			t.Errorf("TruncateTail(%q, %d) = %q, not valid UTF-8", c.s, c.max, got)
		}
		if !strings.HasSuffix(c.s, got) {
			t.Errorf("TruncateTail(%q, %d) = %q, not a suffix of the original", c.s, c.max, got)
		}
	}
}

// TestTruncateTailShortStringUnchanged checks tail behavior at or below the
// byte limit, plus zero and negative limits.
func TestTruncateTailShortStringUnchanged(t *testing.T) {
	for _, s := range []string{"", "a", "héllo", "日本語"} {
		if got := TruncateTail(s, len(s)); got != s {
			t.Errorf("TruncateTail(%q, %d) = %q, want unchanged", s, len(s), got)
		}
		if got := TruncateTail(s, len(s)+5); got != s {
			t.Errorf("TruncateTail(%q, %d) = %q, want unchanged", s, len(s)+5, got)
		}
		if got := TruncateTail(s, 0); got != "" {
			t.Errorf("TruncateTail(%q, 0) = %q, want empty", s, got)
		}
		if got := TruncateTail(s, -1); got != "" {
			t.Errorf("TruncateTail(%q, -1) = %q, want empty", s, got)
		}
	}
}

// TestTruncateTailProperties checks invariants across every byte limit: valid
// UTF-8, a suffix of the input, and never longer than the limit.
func TestTruncateTailProperties(t *testing.T) {
	inputs := []string{"", "a", "abc", "héllo", "héllo wörld", "日本語", "a🙂b", "e\u0301"}
	for _, s := range inputs {
		for max := 0; max <= len(s)+2; max++ {
			got := TruncateTail(s, max)
			if !utf8.ValidString(got) {
				t.Errorf("TruncateTail(%q, %d) = %q, not valid UTF-8", s, max, got)
			}
			if !strings.HasSuffix(s, got) {
				t.Errorf("TruncateTail(%q, %d) = %q, not a suffix", s, max, got)
			}
			if len(got) > max {
				t.Errorf("TruncateTail(%q, %d) = %q, length %d exceeds limit", s, max, got, len(got))
			}
		}
	}
}
