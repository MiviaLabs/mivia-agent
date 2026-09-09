package composition

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestBoundedRedactedInputKeepsBudgetAfterEarlyInvalidByte pins DC-6: the
// rune-boundary repair after the byte cut must inspect ONLY the rune at the
// cut. Validating the whole prefix (utf8.ValidString) makes one invalid byte
// early in the redacted text amputate every byte after it, so an operator sees
// a near-empty hook input labelled as a 512-byte cut.
func TestBoundedRedactedInputKeepsBudgetAfterEarlyInvalidByte(t *testing.T) {
	const marker = "...(truncated)"

	tests := []struct {
		name  string
		input string
		want  int
	}{
		{
			name:  "early invalid byte does not amputate later content",
			input: `{"a":"` + "\xff" + strings.Repeat("x", 1024) + `"}`,
			want:  maxHookRunInputBytes,
		},
		{
			name: "partial rune at the cut boundary is trimmed",
			input: `{"a":"` + strings.Repeat("x", maxHookRunInputBytes-7) + "é" +
				strings.Repeat("z", 64) + `"}`,
			want: maxHookRunInputBytes - 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := boundedRedactedInput(json.RawMessage(tc.input))
			kept := strings.TrimSuffix(got, marker)
			if len(kept) != tc.want {
				t.Fatalf("boundedRedactedInput kept %d bytes, want %d (got %q)",
					len(kept), tc.want, kept)
			}
		})
	}
}
