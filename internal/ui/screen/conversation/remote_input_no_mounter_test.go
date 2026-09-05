package conversation

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// TestHandleRemoteInputIgnoresUntrackedSessionWithoutAMounter covers
// handleRemoteInput's s.mounter == nil branch: a remote input for a session
// this screen neither owns nor tracks, with no mounter wired to go fetch it,
// must be reported and dropped rather than queued forever.
func TestHandleRemoteInputIgnoresUntrackedSessionWithoutAMounter(t *testing.T) {
	s := newTestScreen(t)
	next, _ := s.handleRemoteInput(ports.RemoteInputEvent{SessionID: "untracked-session", Body: "hello"})
	got := next.(Screen)
	if !strings.Contains(got.statusline.View(fixedNow()), "not tracked") {
		t.Fatalf("statusline = %q, want a notice explaining the untracked session was ignored", got.statusline.View(fixedNow()))
	}
	if _, tracked := got.sessions["untracked-session"]; tracked {
		t.Fatal("an untracked session with no mounter must not become tracked")
	}
}
