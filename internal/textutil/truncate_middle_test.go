package textutil

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestTruncateMiddle_SmokeE2E is the table-driven end-to-end test for
// TruncateMiddle. It covers the three required cases (input shorter than
// maxLen unchanged, input exactly maxLen unchanged, long input truncated with
// the ellipsis preserved) plus negative and edge rows: empty input, maxLen 0
// and negative, maxLen < 3 rune-safe prefix fallback, maxLen == 3 marker-only,
// multi-byte runes never split, malformed UTF-8 seeds, oversized multi-KiB
// input, and repeated-invocation determinism.
func TestTruncateMiddle_SmokeE2E(t *testing.T) {
	cases := []struct {
		name   string
		s      string
		maxLen int
		want   string
	}{
		// Required: input shorter than maxLen returns s unchanged.
		{"short ascii", "abc", 5, "abc"},
		{"short empty", "", 5, ""},
		{"short multi-byte", "日本語", 5, "日本語"},
		// Required: input exactly maxLen returns s unchanged.
		{"exact ascii", "abc", 3, "abc"},
		{"exact multi-byte", "日本語", 3, "日本語"},
		// Required: long input truncates with the ellipsis preserved. The
		// remaining maxLen-3 = 4 runes split evenly: prefix gets ceil(4/2) = 2
		// runes from the start ("ab"), suffix gets floor(4/2) = 2 runes from
		// the END ("gh" — "fg" is not a suffix of the input), so the
		// contract-correct result is "ab...gh".
		{"long truncated", "abcdefgh", 7, "ab...gh"},
		// Empty input stays empty at any maxLen.
		{"empty at three", "", 3, ""},
		// maxLen 0 and negative return "".
		{"zero maxLen", "abc", 0, ""},
		{"negative maxLen", "abc", -1, ""},
		// maxLen < 3: the marker cannot fit, so keep a rune-safe prefix with
		// no marker.
		{"tiny budget two", "abcdefgh", 2, "ab"},
		{"tiny budget one", "abcdefgh", 1, "a"},
		// maxLen == 3: the marker alone fills the budget.
		{"marker only", "abcdefgh", 3, "..."},
		// Multi-byte runes never split across the prefix/suffix boundary.
		{"multi-byte middle cut", "あいうえおかきくけこ", 6, "あい...こ"},
		{"four-byte rune whole", "日本語🙂a", 4, "日..."},
		// Malformed UTF-8 seeds: no panic, budget respected.
		{"malformed unchanged", "\xff\xfe\x80", 5, "\xff\xfe\x80"},
		{"malformed truncated", "\xff\xfe\x80\xff\xfe\x80", 5, "\xff...\x80"},
		// Oversized input bounded to maxLen runes.
		{"oversized ascii", strings.Repeat("ab", 4096), 16, "abababa...ababab"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := TruncateMiddle(c.s, c.maxLen)
			if got != c.want {
				t.Fatalf("TruncateMiddle(%q, %d) = %q, want %q", c.s, c.maxLen, got, c.want)
			}
			// Valid UTF-8 in, valid UTF-8 out.
			if utf8.ValidString(c.s) && !utf8.ValidString(got) {
				t.Errorf("TruncateMiddle(%q, %d) = %q, not valid UTF-8", c.s, c.maxLen, got)
			}
			// The rune budget is never exceeded. The "at most maxLen" contract
			// only applies to a positive budget; a non-positive maxLen means
			// the empty result.
			if c.maxLen > 0 && utf8.RuneCountInString(got) > c.maxLen {
				t.Errorf("TruncateMiddle(%q, %d) = %q, rune count %d exceeds limit",
					c.s, c.maxLen, got, utf8.RuneCountInString(got))
			}
			if got != c.s {
				// Truncation happened: the marker is present exactly when
				// maxLen >= 3, and the result fills the budget.
				if strings.Contains(got, middleMarker) != (c.maxLen >= 3) {
					t.Errorf("TruncateMiddle(%q, %d) = %q, marker present = %v, want %v",
						c.s, c.maxLen, got, strings.Contains(got, middleMarker), c.maxLen >= 3)
				}
				if c.maxLen >= 3 && utf8.RuneCountInString(got) != c.maxLen {
					t.Errorf("TruncateMiddle(%q, %d) = %q, rune count %d != maxLen",
						c.s, c.maxLen, got, utf8.RuneCountInString(got))
				}
			}
			// Repeated invocation is deterministic.
			if again := TruncateMiddle(c.s, c.maxLen); again != got {
				t.Errorf("TruncateMiddle(%q, %d) is not deterministic: %q then %q",
					c.s, c.maxLen, got, again)
			}
		})
	}
}

// TestTruncateMiddleMarkerIsASCII pins the marker contract in reverse of
// TestTruncateEllipsisMarkerIsSingleRune: middleMarker is exactly the ASCII
// "..." (3 runes, 3 bytes) and is distinct from the U+2026 ellipsisMarker.
func TestTruncateMiddleMarkerIsASCII(t *testing.T) {
	if middleMarker != "..." {
		t.Errorf("middleMarker %q must be exactly the ASCII \"...\" marker", middleMarker)
	}
	if utf8.RuneCountInString(middleMarker) != 3 {
		t.Errorf("middleMarker %q has %d runes, want exactly 3", middleMarker, utf8.RuneCountInString(middleMarker))
	}
	if len(middleMarker) != 3 {
		t.Errorf("middleMarker %q is %d bytes, want 3", middleMarker, len(middleMarker))
	}
	if middleMarker == ellipsisMarker {
		t.Error("middleMarker must not equal the U+2026 ellipsisMarker")
	}
	if got := TruncateMiddle("abcdefgh", 7); !strings.Contains(got, middleMarker) {
		t.Errorf("TruncateMiddle(%q, 7) = %q, must contain the ASCII \"...\" marker", "abcdefgh", got)
	}
	if got := TruncateMiddle("abcdefgh", 7); got == TruncateEllipsis("abcdefgh", 7) {
		t.Errorf("TruncateMiddle(%q, 7) = %q must use ASCII \"...\", not the U+2026 marker", "abcdefgh", got)
	}
}

// TestTruncateMiddleOversizedInput checks multi-KiB ASCII and ~896 KiB
// multi-byte inputs against small budgets: the result fills maxLen runes, has
// the marker, stays valid UTF-8, and preserves the input head and tail.
func TestTruncateMiddleOversizedInput(t *testing.T) {
	cases := []struct {
		s      string
		maxLen int
	}{
		{strings.Repeat("ab", 4096), 16},      // 8 KiB ASCII
		{strings.Repeat("héllo ", 1<<17), 32}, // ~896 KiB multi-byte
	}
	for _, c := range cases {
		got := TruncateMiddle(c.s, c.maxLen)
		if utf8.RuneCountInString(got) > c.maxLen {
			t.Errorf("TruncateMiddle(len %d, %d) = %q, rune count %d exceeds limit",
				len(c.s), c.maxLen, got, utf8.RuneCountInString(got))
		}
		if !utf8.ValidString(got) {
			t.Errorf("TruncateMiddle(len %d, %d) = %q, not valid UTF-8", len(c.s), c.maxLen, got)
		}
		if !strings.Contains(got, middleMarker) {
			t.Errorf("TruncateMiddle(len %d, %d) = %q, missing middle marker", len(c.s), c.maxLen, got)
		}
		if utf8.RuneCountInString(got) != c.maxLen {
			t.Errorf("TruncateMiddle(len %d, %d) = %q, rune count %d != maxLen",
				len(c.s), c.maxLen, got, utf8.RuneCountInString(got))
		}
		text := c.maxLen - 3
		prefixRunes := (text + 1) / 2
		suffixRunes := text / 2
		if firstRunes(got, prefixRunes) != firstRunes(c.s, prefixRunes) {
			t.Errorf("TruncateMiddle(len %d, %d) = %q, head does not match the input head",
				len(c.s), c.maxLen, got)
		}
		if lastRunes(got, suffixRunes) != lastRunes(c.s, suffixRunes) {
			t.Errorf("TruncateMiddle(len %d, %d) = %q, tail does not match the input tail",
				len(c.s), c.maxLen, got)
		}
	}
}

// TestTruncateMiddleDeterminism checks that repeated calls on the same input
// return identical results. Nothing is parsed or decoded, so duplicate-input
// coverage does not apply; the matrix asserts repeated-invocation equality.
func TestTruncateMiddleDeterminism(t *testing.T) {
	cases := []struct {
		s      string
		maxLen int
	}{
		{"", 5},
		{"abc", 3},
		{"abcdefgh", 5},
		{"日本語🙂a", 4},
		{"\xff\xfe\x80\xff\xfe\x80", 5},
		{strings.Repeat("ab", 4096), 16},
	}
	for _, c := range cases {
		first := TruncateMiddle(c.s, c.maxLen)
		second := TruncateMiddle(c.s, c.maxLen)
		if first != second {
			t.Errorf("TruncateMiddle(%q, %d) = %q then %q, not deterministic", c.s, c.maxLen, first, second)
		}
	}
}

// TestTruncateMiddleProperties checks invariants across every rune budget for
// a curated input set: valid UTF-8 for valid input, budget respected,
// unchanged result exactly when no truncation happens, marker present exactly
// when truncation and maxLen >= 3, head and tail preserved rune-safely, and
// repeated-invocation determinism.
func TestTruncateMiddleProperties(t *testing.T) {
	inputs := []string{
		"", "a", "abc", "héllo", "héllo wörld", "日本語", "a🙂b", "e\u0301",
		"\xff\xfe\x80", "\x80\x80", "\xff\xfe\x80\xff\xfe\x80",
	}
	for _, s := range inputs {
		n := utf8.RuneCountInString(s)
		for maxLen := 0; maxLen <= n+2; maxLen++ {
			got := TruncateMiddle(s, maxLen)
			if utf8.ValidString(s) && !utf8.ValidString(got) {
				t.Errorf("TruncateMiddle(%q, %d) = %q, not valid UTF-8", s, maxLen, got)
			}
			if utf8.RuneCountInString(got) > maxLen {
				t.Errorf("TruncateMiddle(%q, %d) = %q, rune count %d exceeds limit",
					s, maxLen, got, utf8.RuneCountInString(got))
			}
			if n <= maxLen {
				if got != s {
					t.Errorf("TruncateMiddle(%q, %d) = %q, want unchanged", s, maxLen, got)
				}
				continue
			}
			if maxLen < 3 {
				if got != firstRunes(s, maxLen) {
					t.Errorf("TruncateMiddle(%q, %d) = %q, want first %d runes %q",
						s, maxLen, got, maxLen, firstRunes(s, maxLen))
				}
				if strings.Contains(got, middleMarker) {
					t.Errorf("TruncateMiddle(%q, %d) = %q, must have no marker below 3", s, maxLen, got)
				}
				continue
			}
			if !strings.Contains(got, middleMarker) {
				t.Errorf("TruncateMiddle(%q, %d) = %q, missing middle marker", s, maxLen, got)
			}
			if utf8.RuneCountInString(got) != maxLen {
				t.Errorf("TruncateMiddle(%q, %d) = %q, rune count %d != maxLen",
					s, maxLen, got, utf8.RuneCountInString(got))
			}
			text := maxLen - 3
			prefixRunes := (text + 1) / 2
			suffixRunes := text / 2
			if firstRunes(got, prefixRunes) != firstRunes(s, prefixRunes) {
				t.Errorf("TruncateMiddle(%q, %d) = %q, head %q != input head %q",
					s, maxLen, got, firstRunes(got, prefixRunes), firstRunes(s, prefixRunes))
			}
			if lastRunes(got, suffixRunes) != lastRunes(s, suffixRunes) {
				t.Errorf("TruncateMiddle(%q, %d) = %q, tail %q != input tail %q",
					s, maxLen, got, lastRunes(got, suffixRunes), lastRunes(s, suffixRunes))
			}
			if again := TruncateMiddle(s, maxLen); again != got {
				t.Errorf("TruncateMiddle(%q, %d) is not deterministic", s, maxLen)
			}
		}
	}
}

// FuzzTruncateMiddleProperties checks the same invariants as the property
// matrix over arbitrary input and a bounded budget. maxLen is derived from the
// fuzz word modulo the rune count plus 3, so every call stays inside a tiny
// budget and no unbounded allocation is possible.
func FuzzTruncateMiddleProperties(f *testing.F) {
	for _, s := range []string{
		"", "a", "abc", "héllo", "héllo wörld", "日本語", "a🙂b", "e\u0301",
		"\xff\xfe\x80", "\x80\x80", "\xff\xfe\x80\xff\xfe\x80",
	} {
		f.Add(s, uint32(0))
	}
	f.Fuzz(func(t *testing.T, s string, m uint32) {
		maxLen := int(m % uint32(utf8.RuneCountInString(s)+3))
		got := TruncateMiddle(s, maxLen)
		if utf8.ValidString(s) && !utf8.ValidString(got) {
			t.Fatalf("TruncateMiddle(%q, %d) = %q, not valid UTF-8", s, maxLen, got)
		}
		if utf8.RuneCountInString(got) > maxLen {
			t.Fatalf("TruncateMiddle(%q, %d) = %q, rune count %d exceeds limit",
				s, maxLen, got, utf8.RuneCountInString(got))
		}
		if utf8.RuneCountInString(s) <= maxLen {
			if got != s {
				t.Fatalf("TruncateMiddle(%q, %d) = %q, want unchanged", s, maxLen, got)
			}
			return
		}
		if maxLen < 3 {
			if got != firstRunes(s, maxLen) {
				t.Fatalf("TruncateMiddle(%q, %d) = %q, want first %d runes %q",
					s, maxLen, got, maxLen, firstRunes(s, maxLen))
			}
			return
		}
		if !strings.Contains(got, middleMarker) {
			t.Fatalf("TruncateMiddle(%q, %d) = %q, missing middle marker", s, maxLen, got)
		}
		if utf8.RuneCountInString(got) != maxLen {
			t.Fatalf("TruncateMiddle(%q, %d) = %q, rune count %d != maxLen",
				s, maxLen, got, utf8.RuneCountInString(got))
		}
		text := maxLen - 3
		prefixRunes := (text + 1) / 2
		suffixRunes := text / 2
		if firstRunes(got, prefixRunes) != firstRunes(s, prefixRunes) {
			t.Fatalf("TruncateMiddle(%q, %d) = %q, head %q != input head %q",
				s, maxLen, got, firstRunes(got, prefixRunes), firstRunes(s, prefixRunes))
		}
		if lastRunes(got, suffixRunes) != lastRunes(s, suffixRunes) {
			t.Fatalf("TruncateMiddle(%q, %d) = %q, tail %q != input tail %q",
				s, maxLen, got, lastRunes(got, suffixRunes), lastRunes(s, suffixRunes))
		}
		if again := TruncateMiddle(s, maxLen); again != got {
			t.Fatalf("TruncateMiddle(%q, %d) is not deterministic", s, maxLen)
		}
	})
}
