package uiadapter

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// TestResumeErrorTextLiveSessionIsActionable pins the honest-refusal
// rendering: when another live process holds the session's lease, the user
// must see who has it and how long until retry succeeds - not a doubled-id
// wrapped error chain whose cause the header renderer then clips to "~".
func TestResumeErrorTextLiveSessionIsActionable(t *testing.T) {
	live := &contextstate.SessionLiveError{
		LeaseAge:   36 * time.Second,
		RetryAfter: 84 * time.Second,
	}
	wrapped := fmt.Errorf("resume context session: %w", live)

	got := resumeErrorText("UOYZ", wrapped)
	for _, want := range []string{"UOYZ", "another mivia process", "36s", "1m24s"} {
		if !strings.Contains(got, want) {
			t.Fatalf("resume error %q does not contain %q", got, want)
		}
	}
	if strings.Count(got, "UOYZ") != 1 {
		t.Fatalf("resume error repeats the session id: %q", got)
	}

	// Any other failure keeps the existing wrapped rendering.
	plain := resumeErrorText("UOYZ", errors.New("boom"))
	if !strings.Contains(plain, "failed to resume session") || !strings.Contains(plain, "boom") {
		t.Fatalf("plain resume error lost its cause: %q", plain)
	}
}
