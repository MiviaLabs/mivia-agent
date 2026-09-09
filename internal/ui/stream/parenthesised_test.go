package stream

import "testing"

// TestParenthesised_EmptyReasonYieldsEmpty pins the empty-reason guard
// directly.
func TestParenthesised_EmptyReasonYieldsEmpty(t *testing.T) {
	if got := parenthesised(""); got != "" {
		t.Fatalf("parenthesised(\"\") = %q, want \"\"", got)
	}
}
