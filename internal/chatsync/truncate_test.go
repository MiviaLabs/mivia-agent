package chatsync

import (
	"strings"
	"testing"
)

// TestTruncateStringInvalidByteEarlyKeepsBudget pins the boundary rule:
// truncation must only back off across the rune at the CUT BOUNDARY. A
// single invalid byte early in the input must not collapse the kept prefix,
// because the wire contract (event-contract.md section 5) reports `kept` as
// the truth about how much content survived. Validating the whole prefix
// instead of the boundary rune amputates the payload and reports the
// amputation as a legitimate budget cut.
func TestTruncateStringInvalidByteEarlyKeepsBudget(t *testing.T) {
	const maxBytes = BudgetToolOutput
	s := strings.Repeat("A", 10) + "\xff" + strings.Repeat("B", 20*1024)

	kept, keptLen, totalLen, truncated := truncateString(s, maxBytes)
	if !truncated {
		t.Fatal("truncated = false, want true")
	}
	// The counts are in ESCAPED bytes - the unit the store measures in - so
	// they are compared against escapedLen, not against len. The invalid byte
	// at offset 10 costs 3 there, because json.Marshal writes U+FFFD for it.
	if totalLen != escapedLen(s) {
		t.Errorf("totalLen = %d, want %d", totalLen, escapedLen(s))
	}
	if keptLen != escapedLen(kept) {
		t.Errorf("keptLen = %d, want %d", keptLen, escapedLen(kept))
	}
	// The cut boundary lands inside a run of ASCII 'B', so nothing at the
	// boundary needs trimming: the full budget must survive.
	if keptLen != maxBytes {
		t.Errorf("keptLen = %d, want %d (an invalid byte at offset 10 must not "+
			"trim the kept prefix back to it)", keptLen, maxBytes)
	}
}

// TestTruncateStringBoundaryRuneOnly asserts the boundary back-off still
// happens: a multi-byte rune split by the budget is dropped whole.
func TestTruncateStringBoundaryRuneOnly(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		maxBytes int
		wantKept string
	}{
		{name: "split 3-byte rune", in: "世界世界", maxBytes: 5, wantKept: "世"},
		{name: "exact rune boundary", in: "世界", maxBytes: 3, wantKept: "世"},
		{name: "split 4-byte rune", in: "a\U0001F600b", maxBytes: 3, wantKept: "a"},
		{name: "ascii cut", in: "abcdef", maxBytes: 3, wantKept: "abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kept, keptLen, totalLen, truncated := truncateString(tc.in, tc.maxBytes)
			if !truncated {
				t.Fatal("truncated = false, want true")
			}
			if totalLen != len(tc.in) {
				t.Errorf("totalLen = %d, want %d", totalLen, len(tc.in))
			}
			if kept != tc.wantKept || keptLen != len(tc.wantKept) {
				t.Errorf("kept = %q (%d bytes), want %q (%d bytes)",
					kept, keptLen, tc.wantKept, len(tc.wantKept))
			}
		})
	}
}

// TestTruncateStringTrailingReplacementCharKept guards the boundary check
// against confusing a real U+FFFD the producer wrote with a decode failure.
func TestTruncateStringTrailingReplacementCharKept(t *testing.T) {
	// "a" + U+FFFD (3 bytes) = 4 bytes; budget 4 with more content after.
	s := "a�tail"
	kept, keptLen, _, truncated := truncateString(s, 4)
	if !truncated {
		t.Fatal("truncated = false, want true")
	}
	if kept != "a�" || keptLen != 4 {
		t.Errorf("kept = %q (%d bytes), want %q (4 bytes)", kept, keptLen, "a�")
	}
}
