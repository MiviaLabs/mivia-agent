// remote_cancel_foreground_notice_test.go proves a remote cancel's DEFERRED
// notice (the one written later, from the tea.Cmd's result message) reaches
// the SESSION targeted, not a stale Screen copy.
//
// bubbletea's Update loop is a two-generation handoff: handleKey /
// handleRemoteTargetedCancel runs on generation N and returns the next
// Screen value, generation N+1. The tea.Cmd it also returns runs later and
// produces a result message that generation N+1 (or a later one) handles.
// A fixture that starts and finishes the whole cancel on the SAME Screen
// value never exercises that handoff and would pass even if the notice
// pointer captured on generation N had gone stale by N+1 - which is exactly
// what this file's target bug does.
package conversation

import (
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// erroringThreads reports a failure from CancelSubagentToolCall, so its
// caller reaches the error leg of the deferred notice rather than the
// success leg TestRemoteCancelTaskNoticeReachesLiveForegroundScreen already
// covers.
type erroringThreads struct {
	stubThreads
}

func (erroringThreads) CancelSubagentToolCall(string, string) (bool, error) {
	return false, errors.New("boom")
}

// TestRemoteCancelTaskNoticeReachesLiveForegroundScreen models the real
// dispatch across two Screen generations: run the handler on generation N
// (s) to get generation N+1 (next) and the tea.Cmd, run that Cmd to get the
// result message, then deliver the message to N+1 - never back to N. Only
// a notice pointer that survives into N+1 makes this pass.
//
// A remote "cancel_task" targeting the session already on screen resolves
// its statusline seam through resolveRemoteCancelSeams's FOREGROUND branch
// (sessionID == s.convID()), which is the exact path the bug lives on.
func TestRemoteCancelTaskNoticeReachesLiveForegroundScreen(t *testing.T) {
	threads := &recordingThreads{stubThreads: stubThreads{}, ok: true}
	s := remoteCancelScreen(t, "sess-1", threads)

	next, cmd := s.handleRemoteTargetedCancel(ports.RemoteInputEvent{
		SessionID: "sess-1", Kind: "cancel_task", Body: "call-1:task-a",
	})
	live, ok := next.(Screen)
	if !ok {
		t.Fatalf("handleRemoteTargetedCancel returned %T, want Screen", next)
	}

	msg := runCmd(t, cmd)
	res, ok := msg.(subagentTaskCancelResultMsg)
	if !ok {
		t.Fatalf("Cmd reported %T, want subagentTaskCancelResultMsg", msg)
	}

	// Deliver the result to the LIVE generation (live), matching how
	// bubbletea actually dispatches it - never back to s, which the real
	// runtime has already discarded by the time the Cmd resolves.
	after, _ := live.handleSubagentTaskCancelResult(res)
	assertNotice(t, after, "cancelling call-1:task-a")
}

// TestRemoteCancelToolCallFailureNoticeReachesLiveForegroundScreen is the
// same two-generation proof for the error leg of the two-part
// "cancel_tool_call" path, which reports through
// threadToolCallCancelResultMsg instead.
func TestRemoteCancelToolCallFailureNoticeReachesLiveForegroundScreen(t *testing.T) {
	threads := &erroringThreads{stubThreads: stubThreads{}}
	s := remoteCancelScreen(t, "sess-1", threads)

	next, cmd := s.handleRemoteTargetedCancel(ports.RemoteInputEvent{
		SessionID: "sess-1", Kind: "cancel_tool_call", Body: "call-1:task-a tc-9",
	})
	live, ok := next.(Screen)
	if !ok {
		t.Fatalf("handleRemoteTargetedCancel returned %T, want Screen", next)
	}

	msg := runCmd(t, cmd)
	res, ok := msg.(threadToolCallCancelResultMsg)
	if !ok {
		t.Fatalf("Cmd reported %T, want threadToolCallCancelResultMsg", msg)
	}
	if res.err == nil {
		t.Fatalf("result = %+v, want a reported error", res)
	}

	after, _ := live.handleThreadToolCallCancelResult(res)
	assertNotice(t, after, "cancel tool call failed: boom")
}
