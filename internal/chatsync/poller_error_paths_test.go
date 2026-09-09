package chatsync

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestNewInputPoller_ClampsWaitSeconds pins both bounds: a non-positive
// value falls back to the default, and a value above the ceiling is capped
// rather than sent to the server verbatim.
func TestNewInputPoller_ClampsWaitSeconds(t *testing.T) {
	if p := NewInputPoller(nil, "sess-1", 0, nil); p.waitSeconds != defaultPollWaitSeconds {
		t.Errorf("waitSeconds for 0 = %d, want default %d", p.waitSeconds, defaultPollWaitSeconds)
	}
	if p := NewInputPoller(nil, "sess-1", 100000, nil); p.waitSeconds != maxPollWaitSeconds {
		t.Errorf("waitSeconds for an oversized value = %d, want capped %d", p.waitSeconds, maxPollWaitSeconds)
	}
}

// TestInputPoller_StartIsIdempotent mirrors HeartbeatRunner's own guard.
func TestInputPoller_StartIsIdempotent(t *testing.T) {
	p := NewInputPoller(nil, "sess-1", 1, fixedAuthorUserIDProvider("user-1"))
	ctx, cancel := context.WithCancel(context.Background())
	p.running = true // simulate already started without a live client
	p.Start(ctx)     // must be a no-op
	cancel()
}

// TestClearPendingInput_NoStateDirIsANoOp pins the guard directly.
func TestClearPendingInput_NoStateDirIsANoOp(t *testing.T) {
	(&InputPoller{}).clearPendingInput() // must not panic on the empty path
}

// TestRecoverPendingInput_MalformedFileIsRemoved pins the parse-failure
// branch: a pending_input.json that is not valid JSON is discarded rather
// than retried forever.
func TestRecoverPendingInput_MalformedFileIsRemoved(t *testing.T) {
	stateDir := t.TempDir()
	path := filepath.Join(stateDir, pendingInputFileName)
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &InputPoller{stateDir: stateDir, inputCh: make(chan RemoteInput, 1)}
	p.recoverPendingInput(context.Background())
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("recoverPendingInput left a malformed pending file in place")
	}
}

// TestRecoverPendingInput_RejectedInputIsRemovedAndReported pins the
// validateRemoteInput-refused branch: a recovered input the current
// validation rules reject is discarded, and onRejected is told why.
func TestRecoverPendingInput_RejectedInputIsRemovedAndReported(t *testing.T) {
	stateDir := t.TempDir()
	path := filepath.Join(stateDir, pendingInputFileName)
	state := pendingInputState{
		Consumed: true,
		Input:    &SessionInput{ID: "inp-1", SessionID: "sess-OTHER", Kind: "message", Body: "hi"},
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var reason string
	p := &InputPoller{
		stateDir: stateDir, sessionID: "sess-mine", inputCh: make(chan RemoteInput, 1),
		onRejected: func(id, sessID, r string) { reason = r },
	}
	p.recoverPendingInput(context.Background())
	if reason == "" {
		t.Fatal("onRejected was never called for a session-mismatched recovered input")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("recoverPendingInput left a rejected pending file in place")
	}
}

// TestInputPoller_LoopExitsOnStopChClosed pins loop's own <-p.stopCh case,
// distinct from the <-ctx.Done() case: with stopCh already closed before
// the loop's first iteration, it must return immediately (closing doneCh
// and inputCh) without ever calling pollOnce - so a nil client here never
// gets touched.
func TestInputPoller_LoopExitsOnStopChClosed(t *testing.T) {
	p := NewInputPoller(nil, "sess-1", 1, nil)
	close(p.stopCh)

	done := make(chan struct{})
	go func() {
		p.loop(context.Background())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not exit promptly on a closed stopCh")
	}
	if _, open := <-p.doneCh; open {
		t.Fatal("loop returned without closing doneCh")
	}
}
