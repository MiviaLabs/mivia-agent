package runtime

import (
	"strings"
	"testing"
)

// TestRuntimeTruncationKeepsBudgetAfterEarlyInvalidByte pins DC-6 for the two
// runtime byte-index cuts: the back-off must inspect ONLY the rune at the cut
// boundary. Validating the whole prefix (utf8.ValidString) makes one invalid
// byte early in the text amputate every byte after it.
func TestRuntimeTruncationKeepsBudgetAfterEarlyInvalidByte(t *testing.T) {
	const auditBudget = 256

	tests := []struct {
		name   string
		budget int
		input  string
		want   int
		cut    func(string) string
	}{
		{
			name:   "hook context keeps budget after early invalid byte",
			budget: MaxHookContextBytes,
			input:  "\xff" + strings.Repeat("x", MaxHookContextBytes*2),
			want:   MaxHookContextBytes,
			cut:    boundHookContext,
		},
		{
			name:   "hook context trims a partial rune at the boundary",
			budget: MaxHookContextBytes,
			input:  strings.Repeat("x", MaxHookContextBytes-1) + "é" + strings.Repeat("z", 64),
			want:   MaxHookContextBytes - 1,
			cut:    boundHookContext,
		},
		{
			name:   "audit preview keeps budget after early invalid byte",
			budget: auditBudget,
			input:  "\xff" + strings.Repeat("x", 1024),
			want:   auditBudget,
			cut:    truncateText,
		},
		{
			name:   "audit preview trims a partial rune at the boundary",
			budget: auditBudget,
			input:  strings.Repeat("x", auditBudget-1) + "é" + strings.Repeat("z", 64),
			want:   auditBudget - 1,
			cut:    truncateText,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.cut(tc.input)
			// boundHookContext appends a notice; measure the kept prefix only.
			kept := got
			if idx := strings.Index(got, "\n... hook context truncated"); idx >= 0 {
				kept = got[:idx]
			}
			if len(kept) != tc.want {
				t.Fatalf("cut kept %d bytes, want %d (got %q)", len(kept), tc.want, kept)
			}
		})
	}
}
