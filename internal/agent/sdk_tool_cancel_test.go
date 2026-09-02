package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
	"github.com/MiviaLabs/mivia-ai-sdk/toolcallctx"
	sdktools "github.com/MiviaLabs/mivia-ai-sdk/tools"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// --- sdkTurnState.cancels registry: unit tests -----------------------------

func TestSDKTurnStateCancelRegistry(t *testing.T) {
	t.Run("register then cancel returns true and invokes the func", func(t *testing.T) {
		turn := newSDKTurnState()
		var invoked bool
		turn.registerCancel("call-1", func() { invoked = true })
		if !turn.cancelCall("call-1") {
			t.Fatal("cancelCall reported no match for a registered call ID")
		}
		if !invoked {
			t.Fatal("cancelCall did not invoke the registered CancelFunc")
		}
	})

	t.Run("cancel on an unknown ID returns false", func(t *testing.T) {
		turn := newSDKTurnState()
		if turn.cancelCall("never-registered") {
			t.Fatal("cancelCall reported a match for an ID that was never registered")
		}
	})

	t.Run("cancel is idempotent: the second call finds nothing", func(t *testing.T) {
		turn := newSDKTurnState()
		var calls int
		turn.registerCancel("call-1", func() { calls++ })
		if !turn.cancelCall("call-1") {
			t.Fatal("first cancelCall should have found the registered entry")
		}
		if turn.cancelCall("call-1") {
			t.Fatal("second cancelCall found an entry that was already removed")
		}
		if calls != 1 {
			t.Fatalf("the underlying CancelFunc ran %d times, want 1", calls)
		}
	})

	t.Run("deregisterCancel removes the entry without invoking it", func(t *testing.T) {
		turn := newSDKTurnState()
		var invoked bool
		turn.registerCancel("call-1", func() { invoked = true })
		turn.deregisterCancel("call-1")
		if invoked {
			t.Fatal("deregisterCancel must not invoke the CancelFunc")
		}
		if turn.cancelCall("call-1") {
			t.Fatal("cancelCall found an entry deregisterCancel already removed")
		}
	})

	t.Run("deregisterCancel is safe on a missing key", func(t *testing.T) {
		turn := newSDKTurnState()
		turn.deregisterCancel("never-registered") // must not panic
	})

	t.Run("a blank callID is a no-op on every method", func(t *testing.T) {
		turn := newSDKTurnState()
		turn.registerCancel("", func() { t.Fatal("must not be stored") })
		turn.deregisterCancel("") // must not panic
		if turn.cancelCall("") {
			t.Fatal("cancelCall must report false for a blank callID")
		}
	})

	t.Run("a nil turn is safe on every method", func(t *testing.T) {
		var turn *sdkTurnState
		turn.registerCancel("call-1", func() {})
		turn.deregisterCancel("call-1")
		if turn.cancelCall("call-1") {
			t.Fatal("cancelCall on a nil turn must report false")
		}
	})
}

// --- blockingCLITool: a CLI tool whose Execute blocks until its context is ----
// --- done, signalling on entered once it has started so a test can wait ---
// --- for registration to have happened before it cancels.                --

type blockingCLITool struct {
	name    string
	entered chan struct{}
	once    sync.Once
}

func newBlockingCLITool(name string) *blockingCLITool {
	return &blockingCLITool{name: name, entered: make(chan struct{})}
}

func (b *blockingCLITool) Name() string               { return b.name }
func (b *blockingCLITool) Description() string        { return "blocks until canceled" }
func (b *blockingCLITool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (b *blockingCLITool) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	b.once.Do(func() { close(b.entered) })
	<-ctx.Done()
	return "", ctx.Err()
}

// quickTool is a CLI tool that returns immediately, used as the sibling call
// that must survive another call's cancellation untouched.
type quickTool struct{ name string }

func (q quickTool) Name() string               { return q.name }
func (q quickTool) Description() string        { return "returns immediately" }
func (q quickTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (q quickTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "sibling-ok", nil
}

// TestCancelCallDoesNotAbortSiblingCalls is the architecture-review-flagged
// regression test: canceling ONE tool call, mid-batch, while another call is
// concurrently in flight in the SAME turn, must not abort or error the
// sibling call, and the canceled call's own Run must still honour the
// existing contract - a nil error, with the failure folded into the string
// result - exactly as it already does for a timeout. The external SDK's
// executeCalls (mivia-ai-sdk/agentloop/toolcall.go) aborts the WHOLE
// concurrent batch if any call in it returns a non-nil error, so a canceled
// call returning an error here would break every sibling call in the batch,
// not just itself - this is the boundary this repo owns and can assert
// without the SDK's own worker pool.
// newSiblingCancelShims wires two dispatcherShims sharing one sdkTurnState -
// a slow, blocking tool and a quick one - so a test can cancel the slow
// call mid-flight and assert the quick sibling is unaffected.
func newSiblingCancelShims(t *testing.T) (shimSlow, shimQuick *dispatcherShim, turn *sdkTurnState, slow *blockingCLITool) {
	t.Helper()
	slow = newBlockingCLITool("slow-tool")
	quick := quickTool{name: "quick-tool"}

	cliReg := tools.NewRegistry()
	cliReg.Register(slow)
	cliReg.Register(quick)

	turn = newSDKTurnState()
	opts := Options{Dispatcher: governedDispatcher(t, cliReg), SessionID: "sess-cancel"}

	shimSlow = &dispatcherShim{
		inner: &sdkToolForName{name: slow.Name()},
		cli:   slow,
		opts:  opts,
		turn:  turn,
	}
	shimQuick = &dispatcherShim{
		inner: &sdkToolForName{name: quick.Name()},
		cli:   quick,
		opts:  opts,
		turn:  turn,
	}
	return shimSlow, shimQuick, turn, slow
}

func TestCancelCallDoesNotAbortSiblingCalls(t *testing.T) {
	shimSlow, shimQuick, turn, slow := newSiblingCancelShims(t)

	type outcome struct {
		out sdktools.Out
		err error
	}
	slowDone := make(chan outcome, 1)
	quickDone := make(chan outcome, 1)

	slowCtx := toolcallctx.WithToolCall(context.Background(), sdkshape.ToolCall{ID: "call-slow", Name: slow.Name()})
	quickCtx := toolcallctx.WithToolCall(context.Background(), sdkshape.ToolCall{ID: "call-quick", Name: "quick-tool"})

	go func() {
		out, err := shimSlow.Run(slowCtx, sdktools.InOut{Value: map[string]any{}})
		slowDone <- outcome{out, err}
	}()
	go func() {
		out, err := shimQuick.Run(quickCtx, sdktools.InOut{Value: map[string]any{}})
		quickDone <- outcome{out, err}
	}()

	select {
	case <-slow.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the slow tool never entered Execute; cancelCall would race registration")
	}
	if !turn.cancelCall("call-slow") {
		t.Fatal("cancelCall reported no match for the in-flight slow call")
	}

	var slowOut, quickOut outcome
	select {
	case slowOut = <-slowDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the canceled call never returned")
	}
	select {
	case quickOut = <-quickDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the sibling call never returned")
	}

	if slowOut.err != nil {
		t.Fatalf("a canceled call's Run must return a nil error (the fold-into-string "+
			"contract), got %v", slowOut.err)
	}
	slowBody, _ := slowOut.out.Value.(string)
	if !strings.Contains(slowBody, "error") {
		t.Fatalf("the canceled call's body must carry the failure, got %q", slowBody)
	}

	if quickOut.err != nil {
		t.Fatalf("the sibling call must not be aborted by the other call's "+
			"cancellation, got error %v", quickOut.err)
	}
	quickBody, _ := quickOut.out.Value.(string)
	if quickBody != "sibling-ok" {
		t.Fatalf("the sibling call's result was affected by the other call's "+
			"cancellation: got %q, want %q", quickBody, "sibling-ok")
	}

	// The registry entry must not leak past the call's own completion.
	if turn.cancelCall("call-slow") {
		t.Fatal("the cancel registry still held an entry for a call that already finished")
	}
}

// TestRunUnadmittedToolSharesTheCancelRegistry is the second
// architecture-review-flagged regression test: a tool call executed through
// RunUnadmittedTool (the deferred/unadmitted path, which builds its OWN
// throwaway *dispatcherShim per call) must be reachable through the SAME
// sdkTurnState cancel registry an admitted call registers into - a registry
// that lived only on the per-tool shim instance would silently miss this
// path, which is exactly the gap an earlier draft of this design had.
func TestRunUnadmittedToolSharesTheCancelRegistry(t *testing.T) {
	slow := newBlockingCLITool("deferred-slow-tool")
	cliReg := tools.NewRegistry()
	cliReg.Register(slow)

	turn := newSDKTurnState()
	opts := Options{Dispatcher: governedDispatcher(t, cliReg), SessionID: "sess-cancel-deferred"}

	ctx := toolcallctx.WithToolCall(context.Background(), sdkshape.ToolCall{ID: "call-deferred", Name: slow.Name()})

	type outcome struct {
		body string
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		body, err := RunUnadmittedTool(ctx, opts, turn, slow, json.RawMessage(`{}`))
		done <- outcome{body, err}
	}()

	select {
	case <-slow.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the deferred call never entered Execute; cancelCall would race registration")
	}

	// This is the crux of the regression: cancel through the TURN'S
	// registry, not through anything scoped to the throwaway shim
	// RunUnadmittedTool built. If registration happened somewhere other
	// than the one shared Run method, this would find nothing.
	if !turn.cancelCall("call-deferred") {
		t.Fatal("cancelCall found no match for the deferred call - it did not " +
			"register into the turn's shared cancel registry")
	}

	var got outcome
	select {
	case got = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the canceled deferred call never returned")
	}
	if got.err != nil {
		t.Fatalf("RunUnadmittedTool must fold a cancellation into the body, not "+
			"return it as an error, got %v", got.err)
	}
	if !strings.Contains(got.body, "error") {
		t.Fatalf("the canceled deferred call's body must carry the failure, got %q", got.body)
	}
}
