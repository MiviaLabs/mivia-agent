package chat

import "testing"

// TestTurnErrorMessage_NilErrIsEmpty pins the nil-error guard directly.
func TestTurnErrorMessage_NilErrIsEmpty(t *testing.T) {
	if got := TurnErrorMessage(nil); got != "" {
		t.Fatalf("TurnErrorMessage(nil) = %q, want empty", got)
	}
}
