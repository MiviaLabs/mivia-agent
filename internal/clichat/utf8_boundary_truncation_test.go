package clichat

import (
	"strings"
	"testing"
)

// TestTruncatePreviewUTF8KeepsBudgetAfterEarlyInvalidByte pins DC-6: the
// back-off must inspect ONLY the rune at the cut boundary. Re-validating the
// whole prefix (utf8.ValidString(s[:maxBytes])) makes one invalid byte early
// in a tool preview amputate every byte after it, and costs O(n^2).
func TestTruncatePreviewUTF8KeepsBudgetAfterEarlyInvalidByte(t *testing.T) {
	const budget = 64

	tests := []struct {
		name  string
		input string
		want  int
	}{
		{
			name:  "early invalid byte does not amputate later content",
			input: "\xffabc" + strings.Repeat("x", 512),
			want:  budget,
		},
		{
			name:  "invalid byte in the middle does not amputate",
			input: strings.Repeat("x", 20) + "\xfe" + strings.Repeat("y", 512),
			want:  budget,
		},
		{
			name:  "partial rune at the cut boundary is trimmed",
			input: strings.Repeat("x", budget-1) + "é" + strings.Repeat("z", 64),
			want:  budget - 1,
		},
		{
			name:  "genuine replacement char at the boundary is kept",
			input: strings.Repeat("x", budget-3) + "�" + strings.Repeat("z", 64),
			want:  budget,
		},
		{
			name:  "under budget is returned whole",
			input: "\xffshort",
			want:  len("\xffshort"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := TruncatePreviewUTF8(tc.input, budget)
			if len(got) != tc.want {
				t.Fatalf("TruncatePreviewUTF8(%q, %d) kept %d bytes, want %d (got %q)",
					tc.input, budget, len(got), tc.want, got)
			}
		})
	}
}
