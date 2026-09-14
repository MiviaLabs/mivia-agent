package render

import (
	"testing"
)

func TestClipSuffix_TruncateAndUnderMarker(t *testing.T) {
	// room <= markW
	if got := clipSuffix("long suffix", 1); got != "" {
		t.Fatalf("expected empty string when room <= markW, got %q", got)
	}
	// Truncate branch
	got := clipSuffix("a long suffix that exceeds room", 10)
	if got == "" {
		t.Fatal("expected non-empty truncated suffix")
	}
}
