package codeintel

import (
	"strings"
	"testing"
)

// TestCollapseLineKeepsBudgetAfterEarlyInvalidByte pins DC-6: the rune-boundary
// back-off must inspect ONLY the rune at the cut. Validating the whole prefix
// (utf8.ValidString) makes one invalid byte early in a rendered signature (a
// string constant holding raw bytes, for example) amputate the whole signature.
func TestCollapseLineKeepsBudgetAfterEarlyInvalidByte(t *testing.T) {
	const budget = 200
	const marker = " …"

	tests := []struct {
		name  string
		input string
		want  int
	}{
		{
			name:  "early invalid byte does not amputate the signature",
			input: `const s = "` + "\xff" + strings.Repeat("a", 512) + `"`,
			want:  budget,
		},
		{
			name:  "partial rune at the cut boundary is trimmed",
			input: strings.Repeat("a", budget-1) + "é" + strings.Repeat("b", 64),
			want:  budget - 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := collapseLine(tc.input)
			kept := strings.TrimSuffix(got, marker)
			if len(kept) != tc.want {
				t.Fatalf("collapseLine kept %d bytes, want %d (got %q)", len(kept), tc.want, kept)
			}
		})
	}
}
