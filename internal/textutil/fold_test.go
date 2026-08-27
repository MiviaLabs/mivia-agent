package textutil

import (
	"strings"
	"testing"
)

// TestContainsFoldEmpty checks the empty-input contract: the empty string is
// contained in itself, an empty substr is contained in any string, and an
// empty s never contains a non-empty substr.
func TestContainsFoldEmpty(t *testing.T) {
	cases := []struct {
		s, substr string
		want      bool
	}{
		{"", "", true},
		{"hello", "", true},
		{"", "a", false},
		{"", "\u00e9", false},
	}
	for _, c := range cases {
		if got := ContainsFold(c.s, c.substr); got != c.want {
			t.Errorf("ContainsFold(%q, %q) = %v, want %v", c.s, c.substr, got, c.want)
		}
	}
}

// TestContainsFoldExact checks that byte-identical strings match.
func TestContainsFoldExact(t *testing.T) {
	cases := []struct {
		s, substr string
	}{
		{"hello", "hello"},
		{"h\u00e9llo", "h\u00e9llo"},
		{"a", "a"},
	}
	for _, c := range cases {
		if !ContainsFold(c.s, c.substr) {
			t.Errorf("ContainsFold(%q, %q) = false, want true", c.s, c.substr)
		}
	}
}

// TestContainsFoldCasePositive checks that ASCII case differences still match.
func TestContainsFoldCasePositive(t *testing.T) {
	cases := []struct {
		s, substr string
	}{
		{"Hello World", "WORLD"},
		{"HELLO", "hello"},
		{"MiXeD", "mixed"},
		{"abcXYZ", "xYz"},
		{"K", "k"},
	}
	for _, c := range cases {
		if !ContainsFold(c.s, c.substr) {
			t.Errorf("ContainsFold(%q, %q) = false, want true", c.s, c.substr)
		}
	}
}

// TestContainsFoldCaseNegative checks that a byte mismatch does not match and
// that a substr longer than s cannot match.
func TestContainsFoldCaseNegative(t *testing.T) {
	cases := []struct {
		s, substr string
	}{
		{"abc", "abd"},
		{"Hello", "world"},
		{"abc", "abcd"},
		{"a", "b"},
	}
	for _, c := range cases {
		if ContainsFold(c.s, c.substr) {
			t.Errorf("ContainsFold(%q, %q) = true, want false", c.s, c.substr)
		}
	}
}

// TestContainsFoldNonASCIIExactOnly checks that non-ASCII runes compare
// exactly: a case-differing non-ASCII pair never matches, while ASCII letters
// inside the same window still fold.
func TestContainsFoldNonASCIIExactOnly(t *testing.T) {
	cases := []struct {
		s, substr string
	}{
		{"\u00c9", "\u00e9"}, // U+00C9 vs U+00E9
		{"\u00e9", "\u00c9"}, // reversed direction
		{"\u1e9e", "\u00df"}, // U+1E9E vs U+00DF
		{"\u00df", "\u1e9e"},
		{"K", "\u212a"}, // Kelvin sign vs ASCII K
		{"\u212a", "K"},
		{"k", "\u212a"}, // Kelvin sign vs ASCII k
		{"\u212a", "k"},
		{"\u00c5", "\u00e5"}, // U+00C5 vs U+00E5
		{"\u00e5", "\u00c5"},
	}
	for _, c := range cases {
		if ContainsFold(c.s, c.substr) {
			t.Errorf("ContainsFold(%q, %q) = true, want false", c.s, c.substr)
		}
	}
	// ASCII folding still works inside a window whose non-ASCII bytes match
	// exactly.
	if !ContainsFold("K\u212a", "k\u212a") {
		t.Error("ContainsFold(\"K\\u212a\", \"k\\u212a\") = false, want true")
	}
}

// TestContainsFoldBoundaryOverlap checks windows at the start, at the end, at
// full length, for a single byte, and for repeated or overlapping patterns.
func TestContainsFoldBoundaryOverlap(t *testing.T) {
	cases := []struct {
		s, substr string
		want      bool
	}{
		{"abcdef", "abc", true}, // window at the start
		{"abcdef", "def", true}, // window at the end
		{"abc", "abc", true},    // window at full length
		{"a", "A", true},        // single byte, folded
		{"a", "b", false},       // single byte, no match
		{"aaaa", "aa", true},    // repeated overlapping windows
		{"aAaA", "AaA", true},   // overlapping folded window
		{"aAaA", "AaAa", true},  // full folded match
	}
	for _, c := range cases {
		if got := ContainsFold(c.s, c.substr); got != c.want {
			t.Errorf("ContainsFold(%q, %q) = %v, want %v", c.s, c.substr, got, c.want)
		}
	}
}

// TestContainsFoldMalformedUTF8 checks byte-exact semantics on invalid UTF-8:
// the call never panics, and only byte-equal sequences match.
func TestContainsFoldMalformedUTF8(t *testing.T) {
	cases := []struct {
		s, substr string
		want      bool
	}{
		{"\xff\xfe", "\xff", true},
		{"\xff\xfe", "\xfe", true},
		{"\xff\xfe", "\xfe\xff", false},
		{"\x80\x80", "\x80", true},
		{"A\xff", "a\xff", true}, // ASCII folds, malformed byte is exact
		{"A\xff", "a\xfe", false},
		{"\xc3\x89", "\xc3\xa9", false}, // byte-level U+00C9 vs U+00E9
	}
	for _, c := range cases {
		if got := ContainsFold(c.s, c.substr); got != c.want {
			t.Errorf("ContainsFold(%q, %q) = %v, want %v", c.s, c.substr, got, c.want)
		}
	}
}

// TestContainsFoldOneMiBSmoke checks a 1 MiB input with a folded positive and
// byte-absent negatives, keeping the substr short for linear time.
func TestContainsFoldOneMiBSmoke(t *testing.T) {
	s := strings.Repeat("abcd", 1<<18) // exactly 1 MiB
	if !ContainsFold(s, "BCD") {
		t.Error("ContainsFold(1MiB, \"BCD\") = false, want true")
	}
	if ContainsFold(s, "Q") {
		t.Error("ContainsFold(1MiB, \"Q\") = true, want false")
	}
	if ContainsFold(s, "xyz") {
		t.Error("ContainsFold(1MiB, \"xyz\") = true, want false")
	}
}

// TestContainsFoldReferenceOracle checks ContainsFold against an independent
// reference over an exhaustive small domain: the reference pre-folds each side
// byte-by-byte with the ASCII-only rule and delegates to strings.Contains.
func TestContainsFoldReferenceOracle(t *testing.T) {
	alphabet := []string{"a", "A", "b", "B", "\u00e9", "\u00c9", "\xff"}
	all := []string{""}
	cur := []string{""}
	for depth := 0; depth < 3; depth++ {
		var next []string
		for _, s := range cur {
			for _, r := range alphabet {
				next = append(next, s+r)
			}
		}
		cur = next
		all = append(all, cur...)
	}
	for _, s := range all {
		for _, sub := range all {
			if got := ContainsFold(s, sub); got != referenceContainsFold(s, sub) {
				t.Errorf("ContainsFold(%q, %q) = %v, want %v", s, sub, got, referenceContainsFold(s, sub))
			}
		}
	}
}

// referenceContainsFold pre-folds both sides with the ASCII-only rule and
// delegates to strings.Contains. It is independent of ContainsFold.
func referenceContainsFold(s, substr string) bool {
	return strings.Contains(asciiFoldOnly(s), asciiFoldOnly(substr))
}

// asciiFoldOnly lowercases ASCII letters byte-by-byte and passes every other
// byte through unchanged.
func asciiFoldOnly(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

// isASCII reports whether every byte of s is in the ASCII range.
func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// stdlibASCIIContainsFold reports whether s contains substr when only ASCII
// letters fold, computed with the stdlib (strings.ToLower followed by
// strings.Contains). On ASCII-only inputs stdlib lowercasing is exactly the
// ASCII-only rule, so this is an independent oracle for the same contract.
func stdlibASCIIContainsFold(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}

// FuzzContainsFoldASCII checks three invariants on arbitrary input: the
// empty-substr contract, agreement with the byte-level reference on any bytes,
// and agreement with the stdlib ToLower+Contains oracle on ASCII-only input
// where stdlib lowercasing equals ASCII lowercasing.
func FuzzContainsFoldASCII(f *testing.F) {
	for _, seed := range []struct{ s, substr string }{
		{"", ""},
		{"", "a"},
		{"hello", ""},
		{"hello", "hello"},
		{"Hello World", "WORLD"},
		{"HELLO", "hello"},
		{"abcXYZ", "xYz"},
		{"abc", "abd"},
		{"\u00c9", "\u00e9"},
		{"\u1e9e", "\u00df"},
		{"K\u212a", "k\u212a"},
		{"\u00c5", "\u00e5"},
		{"\xff\xfe", "\xfe"},
		{"\x80\x80", "\x80"},
		{"aAaA", "AaA"},
		{"aAaA", "AaAa"},
	} {
		f.Add(seed.s, seed.substr)
	}
	f.Fuzz(func(t *testing.T, s, substr string) {
		if substr == "" && !ContainsFold(s, substr) {
			t.Fatalf("ContainsFold(%q, %q) = false, want true", s, substr)
		}
		if got := ContainsFold(s, substr); got != referenceContainsFold(s, substr) {
			t.Fatalf("ContainsFold(%q, %q) = %v, want %v", s, substr, got, referenceContainsFold(s, substr))
		}
		if isASCII(s) && isASCII(substr) {
			if got := ContainsFold(s, substr); got != stdlibASCIIContainsFold(s, substr) {
				t.Fatalf("ASCII ContainsFold(%q, %q) = %v, want %v", s, substr, got, stdlibASCIIContainsFold(s, substr))
			}
		}
	})
}

// TestContainsFoldMidRuneWindows pins the byte-exact contract at rune
// boundaries: ContainsFold is a byte-window scan, so a window that starts
// inside a multi-byte rune still compares byte-for-byte, and ASCII folding
// still applies to ASCII bytes inside such a window. Non-ASCII bytes never
// fold, even when they sit next to an ASCII letter in the same window.
func TestContainsFoldMidRuneWindows(t *testing.T) {
	cases := []struct {
		s, substr string
		want      bool
	}{
		// Window at offset 1 inside the é rune (0xC3 0xA9): continuation
		// byte + 'a'. The 0x41 folds to 'a', the continuation byte never does.
		{"\u00e9a", "\xa9a", true},
		{"\u00e9A", "\xa9a", true},
		{"\u00e9A", "\xa9b", false},
		{"\u00e9a", "\xa9A", true},

		// Window at offset 1, at the very end of the string.
		{"a\u00e9", "\xa9", true},

		// Window crossing the rune boundary between two multi-byte runes.
		{"\u00e9\u00e9", "\xa9\xc3", true},
		{"\u00e9\u00c9", "\xa9\xc3", true},

		// Malformed lead byte is byte-exact, not decoded.
		{"\x80ab", "\x80a", true},

		// Windows starting at a rune start and covering whole runes.
		{"a\u00e9", "\x61\xc3", true},
		{"a\u00e9", "\xc3\xa9", true},

		// Full-string match; only the ASCII byte folds.
		{"\u00e9A", "\xc3\xa9a", true},
		{"\u00e9A", "\xc3\xa9b", false},
	}
	for _, c := range cases {
		if got := ContainsFold(c.s, c.substr); got != c.want {
			t.Errorf("ContainsFold(%q, %q) = %v, want %v", c.s, c.substr, got, c.want)
		}
	}
}

// TestContainsFoldLastWindowOffset checks that the scan reaches the final
// window start i == len(s)-len(substr) and never scans past it: a match only
// available at the last window is found, and a near-match that would only
// succeed past the last window is not.
func TestContainsFoldLastWindowOffset(t *testing.T) {
	cases := []struct {
		s, substr string
		want      bool
	}{
		// Only the last window (offset 2) matches, with both bytes folded.
		{"abcd", "CD", true},
		{"xyzzy", "zy", true},
		{"xyzzy", "yy", false},
		{"hello", "LO", true},
		{"hello", "LX", false},
		{"ab", "B", true},
		{"ab", "c", false},
	}
	for _, c := range cases {
		if got := ContainsFold(c.s, c.substr); got != c.want {
			t.Errorf("ContainsFold(%q, %q) = %v, want %v", c.s, c.substr, got, c.want)
		}
	}
}

// TestContainsFoldASCIIFoldBoundary checks that only 'A'-'Z' and 'a'-'z'
// fold: the bytes immediately outside the letter ranges (0x40 '@', 0x5B '[',
// 0x60 '`', 0x7B '{') and DEL (0x7F) never fold, in either direction.
func TestContainsFoldASCIIFoldBoundary(t *testing.T) {
	cases := []struct {
		s, substr string
		want      bool
	}{
		{"@", "a", false},
		{"a", "@", false},
		{"[", "b", false},
		{"b", "[", false},
		{"`", "A", false},
		{"A", "`", false},
		{"{", "z", false},
		{"z", "{", false},
		{"\x7f", "A", false},
		{"A", "\x7f", false},
	}
	for _, c := range cases {
		if got := ContainsFold(c.s, c.substr); got != c.want {
			t.Errorf("ContainsFold(%q, %q) = %v, want %v", c.s, c.substr, got, c.want)
		}
	}
}
