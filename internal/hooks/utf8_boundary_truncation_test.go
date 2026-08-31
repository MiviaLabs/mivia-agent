package hooks

import (
	"strings"
	"testing"
)

// TestTruncateAtRuneBoundaryKeepsBudgetAfterEarlyInvalidByte pins DC-6. The
// helper exists to stop a byte-index cut from emitting a trailing partial
// rune. Its RuneStart scan then re-validated the WHOLE prefix
// (utf8.ValidString(s[:i])), so any invalid byte earlier in the hook's own
// bytes made it return the unrepaired mid-rune cut - exactly the output it
// exists to prevent. Only the rune at the boundary may decide the back-off.
func TestTruncateAtRuneBoundaryKeepsBudgetAfterEarlyInvalidByte(t *testing.T) {
	const limit = 64

	tests := []struct {
		name  string
		input string
		want  int
	}{
		{
			name:  "boundary partial rune is trimmed despite an early invalid byte",
			input: "\xff" + strings.Repeat("x", limit-2) + "é" + strings.Repeat("z", 32),
			want:  limit - 1,
		},
		{
			name:  "early invalid byte alone does not amputate content",
			input: "\xff" + strings.Repeat("x", 512),
			want:  limit,
		},
		{
			name:  "partial rune at the boundary is trimmed",
			input: strings.Repeat("x", limit-1) + "é" + strings.Repeat("z", 32),
			want:  limit - 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateAtRuneBoundary(tc.input, limit)
			if len(got) != tc.want {
				t.Fatalf("truncateAtRuneBoundary(%q, %d) kept %d bytes, want %d (got %q)",
					tc.input, limit, len(got), tc.want, got)
			}
		})
	}
}
