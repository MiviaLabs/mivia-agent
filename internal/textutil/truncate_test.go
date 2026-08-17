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

// TestTruncateRuneSafeLongestPrefix checks that TruncateRuneSafe returns the
// longest valid UTF-8 prefix within the byte limit. For every input in the
// curated set and every byte limit, it asserts either the budget is filled
// exactly or the next byte would land inside a rune.
func TestTruncateRuneSafeLongestPrefix(t *testing.T) {
	inputs := []string{"", "a", "abcdef", "héllo", "héllo wörld", "日本語", "a🙂b", "e\u0301"}
	for _, s := range inputs {
		for max := 0; max <= len(s)+2; max++ {
			got := TruncateRuneSafe(s, max)

			// Basic invariants.
			if !utf8.ValidString(got) {
				t.Errorf("TruncateRuneSafe(%q, %d) = %q, not valid UTF-8", s, max, got)
			}
			if !strings.HasPrefix(s, got) {
				t.Errorf("TruncateRuneSafe(%q, %d) = %q, not a prefix", s, max, got)
			}
			if len(got) > max {
				t.Errorf("TruncateRuneSafe(%q, %d) = %q, length %d exceeds limit", s, max, got, len(got))
			}

			// Optimality: either filled the budget or the next byte would
			// split a rune. When len(got) == len(s) the entire input is
			// consumed — trivially optimal.
			if len(got) == max || len(got) == len(s) {
				continue
			}
			// len(got) < max and there is more input beyond got.
			// The byte at s[len(got)] is a rune start.  Decode the
			// rune; a longer valid prefix exists only when the full
			// rune fits within the remaining budget.
			if len(got) < len(s) && utf8.RuneStart(s[len(got)]) {
				_, runeLen := utf8.DecodeRuneInString(s[len(got):])
				if len(got)+runeLen <= max {
					t.Errorf("TruncateRuneSafe(%q, %d) = %q (%d bytes): next rune %q needs %d bytes, so a longer prefix %d ≤ %d exists",
						s, max, got, len(got), s[len(got):len(got)+runeLen], runeLen, len(got)+runeLen, max)
				}
			}
		}
	}
}

// TestTruncateTailLongestSuffix checks that TruncateTail returns the longest
// valid UTF-8 suffix within the byte limit. For every input in the curated set
// and every byte limit, it asserts either the budget is filled exactly, the
// entire input was consumed, the window consists entirely of continuation
// bytes (got is empty), or the byte just before the suffix would split a rune.
func TestTruncateTailLongestSuffix(t *testing.T) {
	inputs := []string{"", "a", "abcdef", "héllo", "héllo wörld", "日本語", "a🙂b", "e\u0301"}
	for _, s := range inputs {
		for max := 0; max <= len(s)+2; max++ {
			got := TruncateTail(s, max)

			// Basic invariants.
			if !utf8.ValidString(got) {
				t.Errorf("TruncateTail(%q, %d) = %q, not valid UTF-8", s, max, got)
			}
			if !strings.HasSuffix(s, got) {
				t.Errorf("TruncateTail(%q, %d) = %q, not a suffix", s, max, got)
			}
			if len(got) > max {
				t.Errorf("TruncateTail(%q, %d) = %q, length %d exceeds limit", s, max, got, len(got))
			}

			// Optimality.
			if len(got) == max {
				continue // filled the budget exactly.
			}
			if len(got) == len(s) {
				continue // consumed all input — trivially optimal.
			}
			if got == "" {
				// The window consisted entirely of continuation
				// bytes; empty is the longest valid suffix.
				continue
			}
			// len(got) < max, got is non-empty, and there is input
			// before got. The byte just before the suffix,
			// s[len(s)-len(got)-1], must not be a rune start.
			if len(s)-len(got)-1 >= 0 && utf8.RuneStart(s[len(s)-len(got)-1]) {
				t.Errorf("TruncateTail(%q, %d) = %q (%d bytes): byte before suffix at offset %d %q is a rune start, so a longer suffix %d ≤ %d exists",
					s, max, got, len(got), len(s)-len(got)-1, string(s[len(s)-len(got)-1]), len(got)+1, max)
			}
		}
	}
}

// TestTruncateEllipsisShortStringUnchanged checks that input at or below the
// byte limit is returned unchanged with no ellipsis marker.
func TestTruncateEllipsisShortStringUnchanged(t *testing.T) {
	cases := []struct {
		s   string
		max int
	}{
		{"", 5},
		{"a", 3},
		{"héllo", 10},
		{"日本語", 9},
		{"exact", 5},
	}
	for _, c := range cases {
		got := TruncateEllipsis(c.s, c.max)
		if got != c.s {
			t.Errorf("TruncateEllipsis(%q, %d) = %q, want unchanged %q", c.s, c.max, got, c.s)
		}
		if strings.HasSuffix(got, ellipsisMarker) {
			t.Errorf("TruncateEllipsis(%q, %d) = %q, must not add ellipsis on a no-op", c.s, c.max, got)
		}
	}
}

// TestTruncateEllipsisCutAppendsEllipsis checks that a real cut appends the
// single-rune ellipsis marker, backs off mid-rune, stays valid UTF-8, never
// exceeds the budget, and keeps the text portion a byte prefix of the input.
func TestTruncateEllipsisCutAppendsEllipsis(t *testing.T) {
	cases := []struct {
		s    string
		max  int
		want string
	}{
		{"abcdef", 5, "ab…"},
		{"héllo", 5, "h…"}, // cut inside é backs off to h; 1+3 = 4 bytes
		{"héllo wörld", 6, "hé…"},
		{"日本語", 7, "日…"},
		{"a🙂bcde", 8, "a🙂…"}, // 9 bytes: a(1)+🙂(4)+bcde(4); cut at 5 leaves a🙂 + marker
	}
	for _, c := range cases {
		got := TruncateEllipsis(c.s, c.max)
		if got != c.want {
			t.Errorf("TruncateEllipsis(%q, %d) = %q, want %q", c.s, c.max, got, c.want)
		}
		if !utf8.ValidString(got) {
			t.Errorf("TruncateEllipsis(%q, %d) = %q, not valid UTF-8", c.s, c.max, got)
		}
		if !strings.HasSuffix(got, ellipsisMarker) {
			t.Errorf("TruncateEllipsis(%q, %d) = %q, missing ellipsis marker", c.s, c.max, got)
		}
		if len(got) > c.max {
			t.Errorf("TruncateEllipsis(%q, %d) = %q, length %d exceeds limit", c.s, c.max, got, len(got))
		}
		if !strings.HasPrefix(c.s, strings.TrimSuffix(got, ellipsisMarker)) {
			t.Errorf("TruncateEllipsis(%q, %d) = %q, text prefix is not a byte prefix of the input", c.s, c.max, got)
		}
	}
}

// TestTruncateEllipsisTinyBudgetFallback checks that a budget below the marker
// length falls back to plain rune-safe truncation with no marker. The expected
// values are the exact TruncateRuneSafe outputs, so the fallback is pinned.
func TestTruncateEllipsisTinyBudgetFallback(t *testing.T) {
	cases := []struct {
		s    string
		max  int
		want string
	}{
		{"abcdef", 1, "a"},
		{"héllo", 2, "h"},
		{"日本語", 2, ""},
		{"a🙂b", 2, "a"},
	}
	for _, c := range cases {
		got := TruncateEllipsis(c.s, c.max)
		if got != c.want {
			t.Errorf("TruncateEllipsis(%q, %d) = %q, want fallback %q", c.s, c.max, got, c.want)
		}
		if got != TruncateRuneSafe(c.s, c.max) {
			t.Errorf("TruncateEllipsis(%q, %d) = %q, want TruncateRuneSafe output %q", c.s, c.max, got, TruncateRuneSafe(c.s, c.max))
		}
		if strings.HasSuffix(got, ellipsisMarker) {
			t.Errorf("TruncateEllipsis(%q, %d) = %q, must not add ellipsis when it cannot fit", c.s, c.max, got)
		}
	}
}

// TestTruncateEllipsisExactBudgetThree checks that a budget of exactly 3 bytes
// yields the marker alone on a real cut, and leaves at-or-below-budget input
// unchanged with no added marker.
func TestTruncateEllipsisExactBudgetThree(t *testing.T) {
	cases := []struct {
		s    string
		max  int
		want string
	}{
		{"abcdef", 3, "…"},
		{"日本語", 3, "…"},
		{"a", 3, "a"},
		{"…", 3, "…"},
	}
	for _, c := range cases {
		if got := TruncateEllipsis(c.s, c.max); got != c.want {
			t.Errorf("TruncateEllipsis(%q, %d) = %q, want %q", c.s, c.max, got, c.want)
		}
	}
}

// TestTruncateEllipsisZeroAndNegativeMax checks the zero and negative limits.
func TestTruncateEllipsisZeroAndNegativeMax(t *testing.T) {
	for _, s := range []string{"", "a", "héllo", "日本語"} {
		if got := TruncateEllipsis(s, 0); got != "" {
			t.Errorf("TruncateEllipsis(%q, 0) = %q, want empty", s, got)
		}
		if got := TruncateEllipsis(s, -1); got != "" {
			t.Errorf("TruncateEllipsis(%q, -1) = %q, want empty", s, got)
		}
	}
}

// TestTruncateEllipsisOversizedInput checks multi-KiB and ~896 KiB inputs
// against small budgets: bounded length, marker present, valid UTF-8, and a
// byte prefix preserved.
func TestTruncateEllipsisOversizedInput(t *testing.T) {
	cases := []struct {
		s   string
		max int
	}{
		{strings.Repeat("ab", 4096), 16},      // 8 KiB
		{strings.Repeat("héllo ", 1<<17), 32}, // ~896 KiB
	}
	for _, c := range cases {
		got := TruncateEllipsis(c.s, c.max)
		if len(got) > c.max {
			t.Errorf("TruncateEllipsis(len %d, %d) = %q, length %d exceeds limit", len(c.s), c.max, got, len(got))
		}
		if !strings.HasSuffix(got, ellipsisMarker) {
			t.Errorf("TruncateEllipsis(len %d, %d) = %q, missing ellipsis marker", len(c.s), c.max, got)
		}
		if !utf8.ValidString(got) {
			t.Errorf("TruncateEllipsis(len %d, %d) = %q, not valid UTF-8", len(c.s), c.max, got)
		}
		if !strings.HasPrefix(c.s, strings.TrimSuffix(got, ellipsisMarker)) {
			t.Errorf("TruncateEllipsis(len %d, %d) = %q, text prefix is not a byte prefix of the input", len(c.s), c.max, got)
		}
	}
}

// TestTruncateEllipsisMalformedUTF8 checks invalid UTF-8 seeds across several
// budgets: no panic, budget respected, fallback equality below 3 bytes, marker
// plus byte-prefix behavior in the ellipsis branch, and the valid-UTF-8
// guarantee asserted only for valid input.
func TestTruncateEllipsisMalformedUTF8(t *testing.T) {
	seeds := []string{"\xff\xfe\x80", "\x80\x80\x80", "\xff\xfe\x80\xff\xfe\x80"}
	for _, s := range seeds {
		for _, max := range []int{1, 2, 3, 5} {
			got := TruncateEllipsis(s, max) // must not panic
			if len(got) > max {
				t.Errorf("TruncateEllipsis(%q, %d) = %q, length %d exceeds limit", s, max, got, len(got))
			}
			if max < 3 {
				if got != TruncateRuneSafe(s, max) {
					t.Errorf("TruncateEllipsis(%q, %d) = %q, want fallback %q", s, max, got, TruncateRuneSafe(s, max))
				}
			} else if len(s) > max {
				if !strings.HasSuffix(got, ellipsisMarker) {
					t.Errorf("TruncateEllipsis(%q, %d) = %q, missing ellipsis marker", s, max, got)
				}
				if !strings.HasPrefix(s, strings.TrimSuffix(got, ellipsisMarker)) {
					t.Errorf("TruncateEllipsis(%q, %d) = %q, text prefix is not a byte prefix of the input", s, max, got)
				}
			} else if got != s {
				t.Errorf("TruncateEllipsis(%q, %d) = %q, want unchanged %q", s, max, got, s)
			}
			if utf8.ValidString(s) && !utf8.ValidString(got) {
				t.Errorf("TruncateEllipsis(%q, %d) = %q, not valid UTF-8", s, max, got)
			}
		}
	}
}

// TestTruncateEllipsisMarkerIsSingleRune pins the DC-9/security claim that the
// marker is exactly the single rune U+2026 (3 bytes), never an ASCII "...".
func TestTruncateEllipsisMarkerIsSingleRune(t *testing.T) {
	if utf8.RuneCountInString(ellipsisMarker) != 1 {
		t.Errorf("ellipsisMarker %q has %d runes, want exactly 1", ellipsisMarker, utf8.RuneCountInString(ellipsisMarker))
	}
	if len(ellipsisMarker) != 3 {
		t.Errorf("ellipsisMarker %q is %d bytes, want 3", ellipsisMarker, len(ellipsisMarker))
	}
	if ellipsisMarker == "..." {
		t.Error("ellipsisMarker must not be the ASCII \"...\" marker")
	}
	if got := TruncateEllipsis("abcdef", 5); !strings.HasSuffix(got, "\u2026") {
		t.Errorf("truncated output %q must end in U+2026, not an ASCII marker", got)
	}
}

// TestTruncateEllipsisProperties checks invariants across every byte limit for
// a curated input set: valid UTF-8 for valid input, budget respected, unchanged
// result exactly when no truncation happens, honest marker exactly when a cut
// happens and the budget fits it, fallback equality below 3 bytes, oracle
// equality of the text prefix, and the byte-prefix property. Nothing is parsed
// or decoded, so duplicate-input coverage does not apply; the matrix asserts
// repeated-invocation determinism instead.
func TestTruncateEllipsisProperties(t *testing.T) {
	inputs := []string{
		"", "a", "abc", "héllo", "héllo wörld", "日本語", "a🙂b", "e\u0301",
		"\xff\xfe\x80", "\x80\x80", "\xff\xfe\x80\xff\xfe\x80",
	}
	for _, s := range inputs {
		for max := 0; max <= len(s)+2; max++ {
			got := TruncateEllipsis(s, max)
			if utf8.ValidString(s) && !utf8.ValidString(got) {
				t.Errorf("TruncateEllipsis(%q, %d) = %q, not valid UTF-8", s, max, got)
			}
			if len(got) > max {
				t.Errorf("TruncateEllipsis(%q, %d) = %q, length %d exceeds limit", s, max, got, len(got))
			}
			if len(s) <= max {
				if got != s {
					t.Errorf("TruncateEllipsis(%q, %d) = %q, want unchanged", s, max, got)
				}
				continue
			}
			// len(s) > max: truncation happened.
			if max < 3 {
				if got != TruncateRuneSafe(s, max) {
					t.Errorf("TruncateEllipsis(%q, %d) = %q, want fallback %q", s, max, got, TruncateRuneSafe(s, max))
				}
				continue
			}
			if !strings.HasSuffix(got, ellipsisMarker) {
				t.Errorf("TruncateEllipsis(%q, %d) = %q, missing ellipsis marker", s, max, got)
			}
			prefix := strings.TrimSuffix(got, ellipsisMarker)
			if prefix != TruncateRuneSafe(s, max-3) {
				t.Errorf("TruncateEllipsis(%q, %d) = %q, text prefix %q != TruncateRuneSafe(s, %d) = %q",
					s, max, got, prefix, max-3, TruncateRuneSafe(s, max-3))
			}
			if !strings.HasPrefix(s, prefix) {
				t.Errorf("TruncateEllipsis(%q, %d) = %q, text prefix is not a byte prefix of the input", s, max, got)
			}
		}
	}
}

// TestTruncateEllipsisLongestTextPrefix checks the text portion independently
// of the TruncateRuneSafe oracle: when a cut appends the ellipsis, the text
// prefix is the longest rune-safe prefix within maxBytes-3. Valid inputs only,
// because the decode-next-rune optimality argument assumes well-formed UTF-8.
func TestTruncateEllipsisLongestTextPrefix(t *testing.T) {
	inputs := []string{"", "a", "abcdef", "héllo", "héllo wörld", "日本語", "a🙂b", "e\u0301"}
	for _, s := range inputs {
		for max := 0; max <= len(s)+2; max++ {
			got := TruncateEllipsis(s, max)
			if len(s) <= max || max < 3 {
				continue // no ellipsis case; optimality applies to the text prefix only
			}
			prefix := strings.TrimSuffix(got, ellipsisMarker)
			b := max - 3
			if len(prefix) == b || len(prefix) == len(s) {
				continue
			}
			// len(prefix) < b and there is more input beyond prefix. The byte at
			// s[len(prefix)] is a rune start; a longer rune-safe prefix exists
			// only when the full rune fits within the remaining budget.
			if len(prefix) < len(s) && utf8.RuneStart(s[len(prefix)]) {
				_, runeLen := utf8.DecodeRuneInString(s[len(prefix):])
				if len(prefix)+runeLen <= b {
					t.Errorf("TruncateEllipsis(%q, %d) = %q: text prefix %q (%d bytes) is not longest within %d: next rune needs %d bytes",
						s, max, got, prefix, len(prefix), b, runeLen)
				}
			}
		}
	}
}

// FuzzTruncateEllipsisProperties checks the same invariants as the property
// matrix over arbitrary input and a bounded budget. maxBytes is derived from
// the fuzz word modulo len(s)+5, so every call stays inside a tiny budget and
// no unbounded allocation is possible.
func FuzzTruncateEllipsisProperties(f *testing.F) {
	for _, s := range []string{
		"", "a", "abc", "héllo", "héllo wörld", "日本語", "a🙂b", "e\u0301",
		"\xff\xfe\x80", "\x80\x80", "\xff\xfe\x80\xff\xfe\x80",
	} {
		f.Add(s, uint32(0))
	}
	f.Fuzz(func(t *testing.T, s string, m uint32) {
		maxBytes := int(m % uint32(len(s)+5))
		got := TruncateEllipsis(s, maxBytes)
		if utf8.ValidString(s) && !utf8.ValidString(got) {
			t.Fatalf("TruncateEllipsis(%q, %d) = %q, not valid UTF-8", s, maxBytes, got)
		}
		if len(got) > maxBytes {
			t.Fatalf("TruncateEllipsis(%q, %d) = %q, length %d exceeds limit", s, maxBytes, got, len(got))
		}
		if len(s) <= maxBytes {
			if got != s {
				t.Fatalf("TruncateEllipsis(%q, %d) = %q, want unchanged", s, maxBytes, got)
			}
			return
		}
		if maxBytes < 3 {
			if got != TruncateRuneSafe(s, maxBytes) {
				t.Fatalf("TruncateEllipsis(%q, %d) = %q, want fallback %q", s, maxBytes, got, TruncateRuneSafe(s, maxBytes))
			}
			return
		}
		if !strings.HasSuffix(got, ellipsisMarker) {
			t.Fatalf("TruncateEllipsis(%q, %d) = %q, missing ellipsis marker", s, maxBytes, got)
		}
		prefix := strings.TrimSuffix(got, ellipsisMarker)
		if !strings.HasPrefix(s, prefix) {
			t.Fatalf("TruncateEllipsis(%q, %d) = %q, text prefix is not a byte prefix of the input", s, maxBytes, got)
		}
	})
}

// TestTruncateRuneSafeSingleByteBudgetWithMultiByteLead pins the DC-6
// "truncation must produce a valid value of its own type" probe at the
// extreme: a one-byte budget against a leading rune wider than one byte must
// yield the empty string, never a split rune and never a byte prefix that is
// not valid UTF-8. The audited edge is the byte/runewidth boundary: an
// ASCII-first or multi-byte-first input must behave identically with respect
// to the byte budget.
func TestTruncateRuneSafeSingleByteBudgetWithMultiByteLead(t *testing.T) {
	cases := []struct {
		s    string
		want string
	}{
		{"éx", ""},  // 2-byte lead rune: 1 byte cannot hold it
		{"日x", ""},  // 3-byte lead rune
		{"🙂x", ""},  // 4-byte lead rune
		{"ax", "a"}, // 1-byte lead rune fits exactly
		{"xé", "x"},
		{"x日", "x"},
		{"x🙂", "x"},
	}
	for _, c := range cases {
		got := TruncateRuneSafe(c.s, 1)
		if got != c.want {
			t.Errorf("TruncateRuneSafe(%q, 1) = %q, want %q", c.s, got, c.want)
		}
		if !utf8.ValidString(got) {
			t.Errorf("TruncateRuneSafe(%q, 1) = %q, not valid UTF-8", c.s, got)
		}
		if len(got) > 1 {
			t.Errorf("TruncateRuneSafe(%q, 1) = %q, length %d exceeds budget", c.s, got, len(got))
		}
		if !strings.HasPrefix(c.s, got) {
			t.Errorf("TruncateRuneSafe(%q, 1) = %q, not a prefix of the input", c.s, got)
		}
	}
}

// TestTruncateTailSingleByteBudgetWithMultiByteTail pins the tail twin of the
// one-byte budget probe: a trailing rune wider than one byte cannot fit, so
// the longest valid suffix is empty or the leading ASCII byte at the tail,
// never a split rune.
func TestTruncateTailSingleByteBudgetWithMultiByteTail(t *testing.T) {
	cases := []struct {
		s    string
		want string
	}{
		{"xé", ""}, // trailing 2-byte rune cannot fit in 1 byte
		{"x日", ""}, // trailing 3-byte rune
		{"x🙂", ""}, // trailing 4-byte rune
		{"x", "x"}, // single ASCII byte fits exactly
		{"éx", "x"},
		{"日x", "x"},
		{"🙂x", "x"},
	}
	for _, c := range cases {
		got := TruncateTail(c.s, 1)
		if got != c.want {
			t.Errorf("TruncateTail(%q, 1) = %q, want %q", c.s, got, c.want)
		}
		if !utf8.ValidString(got) {
			t.Errorf("TruncateTail(%q, 1) = %q, not valid UTF-8", c.s, got)
		}
		if len(got) > 1 {
			t.Errorf("TruncateTail(%q, 1) = %q, length %d exceeds budget", c.s, got, len(got))
		}
		if !strings.HasSuffix(c.s, got) {
			t.Errorf("TruncateTail(%q, 1) = %q, not a suffix of the input", c.s, got)
		}
	}
}

// TestTruncateEllipsisMarkerBudgetBoundaries pins the ellipsis-fit boundary:
// the marker alone fills a 3-byte budget; with a multi-byte lead rune even a
// 4- or 5-byte budget yields the marker alone when the rune cannot fit beside
// it; a lead rune that does fit shares the budget. The result never exceeds
// the budget and always carries the marker when a real cut happens.
func TestTruncateEllipsisMarkerBudgetBoundaries(t *testing.T) {
	cases := []struct {
		s    string
		max  int
		want string
	}{
		{"日本語", 3, "…"},    // marker alone exactly fills a 3-byte budget
		{"日本語x", 5, "…"},   // 3-byte lead rune cannot share the 2 free bytes
		{"🙂ab", 4, "…"},    // 4-byte lead rune cannot share the 1 free byte
		{"🙂ab", 5, "…"},    // 4-byte lead rune cannot share the 2 free bytes
		{"éabcd", 5, "é…"}, // 2-byte lead rune shares the 2 free bytes
		{"éabcd", 4, "…"},  // 2-byte lead rune cannot share the 1 free byte
	}
	for _, c := range cases {
		got := TruncateEllipsis(c.s, c.max)
		if got != c.want {
			t.Errorf("TruncateEllipsis(%q, %d) = %q, want %q", c.s, c.max, got, c.want)
		}
		if len(got) > c.max {
			t.Errorf("TruncateEllipsis(%q, %d) = %q, length %d exceeds budget", c.s, c.max, got, len(got))
		}
		if !utf8.ValidString(got) {
			t.Errorf("TruncateEllipsis(%q, %d) = %q, not valid UTF-8", c.s, c.max, got)
		}
		if !strings.HasSuffix(got, ellipsisMarker) {
			t.Errorf("TruncateEllipsis(%q, %d) = %q, missing ellipsis marker", c.s, c.max, got)
		}
	}
}

// TestTruncateEllipsisTwoByteBudgetNoMarker pins the maxBytes < 3 fallback: a
// two-byte budget never appends the marker, equals TruncateRuneSafe exactly,
// and stays rune-safe even when the first rune is wider than the budget.
func TestTruncateEllipsisTwoByteBudgetNoMarker(t *testing.T) {
	cases := []struct {
		s    string
		max  int
		want string
	}{
		{"éx", 2, "é"}, // 2-byte rune exactly fills the fallback budget
		{"ab", 2, "ab"},
		{"日x", 2, ""}, // 3-byte rune cannot fit in 2 bytes
		{"🙂x", 2, ""}, // 4-byte rune cannot fit in 2 bytes
	}
	for _, c := range cases {
		got := TruncateEllipsis(c.s, c.max)
		if got != c.want {
			t.Errorf("TruncateEllipsis(%q, %d) = %q, want %q", c.s, c.max, got, c.want)
		}
		if got != TruncateRuneSafe(c.s, c.max) {
			t.Errorf("TruncateEllipsis(%q, %d) = %q, want TruncateRuneSafe output %q", c.s, c.max, got, TruncateRuneSafe(c.s, c.max))
		}
		if strings.HasSuffix(got, ellipsisMarker) {
			t.Errorf("TruncateEllipsis(%q, %d) = %q, must not append the marker when it cannot fit", c.s, c.max, got)
		}
		if !utf8.ValidString(got) {
			t.Errorf("TruncateEllipsis(%q, %d) = %q, not valid UTF-8", c.s, c.max, got)
		}
	}
}
