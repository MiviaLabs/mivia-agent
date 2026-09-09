package conversation

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// TestRemoteCancelToolCall_UntrackedSessionIsANoop pins
// resolveRemoteCancelSeams' own ok==false branch reached through
// remoteCancelToolCall: a remote cancel_tool_call naming a sessionID this
// screen does not track (not the foreground conversation, not in
// s.sessions) must be a silent no-op rather than a panic.
func TestRemoteCancelToolCall_UntrackedSessionIsANoop(t *testing.T) {
	s := sized(t, 1)
	ev := ports.RemoteInputEvent{Kind: "cancel_tool_call", SessionID: "untracked-session"}
	target := remoteCancelTarget{id: "call-1"}

	next, cmd := s.remoteCancelToolCall(ev, target)
	if cmd != nil {
		t.Fatal("remoteCancelToolCall returned a non-nil Cmd for an untracked session")
	}
	if _, ok := next.(Screen); !ok {
		t.Fatalf("remoteCancelToolCall returned %T, want Screen", next)
	}
}
