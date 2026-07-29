package agent

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Tool results are truncated by byte budget, but the truncated body goes into
// model-visible history and is persisted. Slicing a byte offset can land inside
// a multi-byte rune, so the last character becomes invalid UTF-8 — coerced to
// U+FFFD by the JSON encoder, and rejected outright by providers that validate.
func TestCapToolResultNeverSplitsARune(t *testing.T) {
	cases := []struct {
		name string
		body string
		max  int
	}{
		{"euro tail", strings.Repeat("a", 3900) + strings.Repeat("€", 100), 4000},
		{"all multibyte", strings.Repeat("€", 4000), 4000},
		{"emoji tail", strings.Repeat("x", 50) + strings.Repeat("🙂", 40), 64},
		{"cjk", strings.Repeat("漢", 500), 137},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, truncated := capToolResult(tc.body, tc.max, 0)
			if !truncated {
				t.Fatalf("expected truncation")
			}
			if !utf8.ValidString(got) {
				t.Fatalf("truncated result is not valid UTF-8 (tail %q)", got[max(0, len(got)-24):])
			}
			if len(got) > tc.max {
				t.Fatalf("result %d bytes exceeds max %d", len(got), tc.max)
			}
		})
	}
}
