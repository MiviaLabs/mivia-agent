// remote_cancel_async_test.go is the remote counterpart of
// cancel_subagent_async_test.go. The local key path once called the
// coordinator cancel seam INLINE from a handler bubbletea runs on its
// single Update goroutine; coordinator.CancelTask blocks for its whole wait
// budget (5 seconds), so the press froze rendering and every message,
// ctrl+c included (fixed in e19cd048). The remote path reaches the same
// seams, so it needs the same proof: handleRemoteInput must return while
// the seam is still blocked.
package conversation

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// remoteInputOffLoop calls handleRemoteInput from another goroutine and
// waits up to budget for it to return. A handler that still blocks inline
// never returns within the budget, which is the failure this file catches.
func remoteInputOffLoop(t *testing.T, s Screen, ev ports.RemoteInputEvent, releaseOnce func(), budget time.Duration) tea.Cmd {
	t.Helper()
	done := make(chan tea.Cmd, 1)
	go func() {
		_, cmd := s.handleRemoteInput(ev)
		done <- cmd
	}()
	select {
	case cmd := <-done:
		return cmd
	case <-time.After(budget):
		releaseOnce()
		t.Fatal("handleRemoteInput did not return while the cancel seam was blocked: the update goroutine is stalled")
		return nil
	}
}

// TestRemoteCancelTaskDoesNotBlockUpdate proves a remote "cancel_task"
// returns immediately even while CancelSubagentTask is blocked, and that
// the outcome arrives later as a message the screen already handles.
func TestRemoteCancelTaskDoesNotBlockUpdate(t *testing.T) {
	release := make(chan struct{})
	releaseOnce := releaser(release)
	defer releaseOnce()

	threads := &blockingCancelThreads{stubThreads: stubThreads{}, release: release}
	s := remoteCancelScreen(t, "sess-1", threads)

	cmd := remoteInputOffLoop(t, s, ports.RemoteInputEvent{
		SessionID: "sess-1", Kind: "cancel_task", Body: "call-1:task-a",
	}, releaseOnce, 2*time.Second)
	if cmd == nil {
		t.Fatal("handleRemoteInput returned no Cmd; the cancel would never run")
	}
	if threads.entered() != 0 {
		t.Fatalf("CancelSubagentTask was entered %d times on the update goroutine, want 0", threads.entered())
	}

	msgCh := make(chan tea.Msg, 1)
	go func() { msgCh <- cmd() }()
	releaseOnce()

	res, ok := (<-msgCh).(subagentTaskCancelResultMsg)
	if !ok {
		t.Fatal("the Cmd did not report a subagentTaskCancelResultMsg")
	}
	if !res.ok || res.err != nil {
		t.Fatalf("result = %+v, want ok with no error", res)
	}
}

// TestRemoteCancelToolCallInSubagentDoesNotBlockUpdate is the same proof
// for the two-part "cancel_tool_call", which crosses the same seam.
func TestRemoteCancelToolCallInSubagentDoesNotBlockUpdate(t *testing.T) {
	release := make(chan struct{})
	releaseOnce := releaser(release)
	defer releaseOnce()

	threads := &blockingCancelThreads{stubThreads: stubThreads{}, release: release}
	s := remoteCancelScreen(t, "sess-1", threads)

	cmd := remoteInputOffLoop(t, s, ports.RemoteInputEvent{
		SessionID: "sess-1", Kind: "cancel_tool_call", Body: "call-1:task-a tc-9",
	}, releaseOnce, 2*time.Second)
	if cmd == nil {
		t.Fatal("handleRemoteInput returned no Cmd; the cancel would never run")
	}
	if threads.entered() != 0 {
		t.Fatalf("CancelSubagentToolCall was entered %d times on the update goroutine, want 0", threads.entered())
	}

	msgCh := make(chan tea.Msg, 1)
	go func() { msgCh <- cmd() }()
	releaseOnce()

	res, ok := (<-msgCh).(threadToolCallCancelResultMsg)
	if !ok {
		t.Fatal("the Cmd did not report a threadToolCallCancelResultMsg")
	}
	if !res.ok || res.err != nil {
		t.Fatalf("result = %+v, want ok with no error", res)
	}
}
