package agent

import (
	"strings"
	"testing"
)

// TestTruncatePreviewKeepsBudgetAfterEarlyInvalidByte pins DC-6: the back-off
// after a byte-index cut must inspect ONLY the rune at the cut boundary.
// Validating the whole prefix (utf8.ValidString) makes one invalid byte early
// in the string amputate every byte after it, reporting a budget-sized cut as
// a near-empty preview.
func TestTruncatePreviewKeepsBudgetAfterEarlyInvalidByte(t *testing.T) {
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
			got := truncatePreview(tc.input, budget)
			if len(got) != tc.want {
				t.Fatalf("truncatePreview(%q, %d) kept %d bytes, want %d (got %q)",
					tc.input, budget, len(got), tc.want, got)
			}
		})
	}
}
