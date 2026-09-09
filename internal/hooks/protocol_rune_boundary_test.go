package hooks

import "testing"

// TestDeclaredRuneLen covers every branch of the lead-byte length table
// directly.
func TestDeclaredRuneLen(t *testing.T) {
	cases := []struct {
		name string
		b    byte
		want int
	}{
		{"ascii", 'a', 1},
		{"two-byte lead", 0xC2, 2},
		{"three-byte lead", 0xE0, 3},
		{"four-byte lead", 0xF0, 4},
		{"invalid lead", 0xFF, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := declaredRuneLen(tc.b); got != tc.want {
				t.Errorf("declaredRuneLen(%#x) = %d, want %d", tc.b, got, tc.want)
			}
		})
	}
}

// TestTruncateAtRuneBoundary_InvalidButCompleteSequenceIsLeftAsIs pins
// the branch where a lead byte's declared length FITS within the bytes
// left (as opposed to the incomplete-sequence trim case), yet the
// sequence still fails to decode - an overlong/invalid encoding rather
// than a torn one, which the function deliberately leaves untouched
// (it repairs truncation, not pre-existing invalid bytes).
func TestTruncateAtRuneBoundary_InvalidButCompleteSequenceIsLeftAsIs(t *testing.T) {
	// 0xE0 0x80 0x80 is a three-byte-lead sequence, fully present (3
	// bytes for a declared length of 3), but it is an overlong encoding
	// of U+0000 - invalid, and NOT a torn multi-byte rune.
	s := "a" + string([]byte{0xE0, 0x80, 0x80})
	got := truncateAtRuneBoundary(s, len(s))
	if got != s {
		t.Fatalf("truncateAtRuneBoundary() = %q, want the invalid-but-complete tail left unchanged: %q", got, s)
	}
}
