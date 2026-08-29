package agent

// The turn-shaping ordering gate waits for the call index before its own to
// finish shaping. It advances only from inside turnShapeWrapper.Run, so it
// assumes every dispatched index reaches Run. The SDK guarantees no such
// thing: decodeAndRun rejects a call before the registry ever sees it (unknown
// name, scope denied, schema validation failure), and executeCalls skips a
// plan marked duplicate outright. Either leaves a hole in the index sequence,
// and a later call then waits on a slot nothing will ever fill - the turn
// falls silent after a tool ran and dies at the request deadline.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-ai-sdk/toolcallctx"
	sdktools "github.com/MiviaLabs/mivia-ai-sdk/tools"
)

// runShapedCall drives one wrapper to completion and reports whether it
// returned before limit. It never leaks a failing goroutine into a later test:
// the wrapper is released by the caller's cancel.
func runShapedCall(t *testing.T, w *turnShapeWrapper, ctx context.Context, id string, index int, limit time.Duration) bool {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = w.Run(toolcallctx.WithToolCall(ctx, sdkToolCallFor(id, index)), sdktools.InOut{Value: json.RawMessage(`{}`)})
	}()
	select {
	case <-done:
		return true
	case <-time.After(limit):
		return false
	}
}

func shapingWrapper(counter *turnShapeCounter, tool sdktools.Tool) *turnShapeWrapper {
	return &turnShapeWrapper{
		inner: tool, toolName: "blocking_tool", counter: counter,
		env: newShapeEnv(nil, "s"), cap: 64 << 10, budget: 64 << 10,
	}
}

// The duplicate-skip shape: index 0 ran and advanced the counter to 1, index 1
// was dropped as a duplicate and never reached Run, and index 2 must still be
// able to shape. Nothing else is in flight, so no broadcast can ever arrive.
func TestSDKTurnShaping_SkippedIndexDoesNotStrandLaterCall(t *testing.T) {
	counter := newTurnShapeCounter()
	counter.nextIndex = 1 // index 0 completed; index 1 was skipped

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := shapingWrapper(counter, &blockingTool{})
	if !runShapedCall(t, w, ctx, "c2", 2, 2*time.Second) {
		t.Fatal("a call whose predecessor index never runs must not wait for it")
	}
}

// The pre-dispatch rejection shape: the very first call of a batch is rejected
// for schema validation, so index 0 never runs and index 1 is alone.
func TestSDKTurnShaping_RejectedFirstCallDoesNotStrandBatch(t *testing.T) {
	counter := newTurnShapeCounter()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := shapingWrapper(counter, &blockingTool{})
	if !runShapedCall(t, w, ctx, "c1", 1, 2*time.Second) {
		t.Fatal("a batch whose first call is rejected before dispatch must not strand the second")
	}
}

// Two survivors of one hole must both finish. Neither can be woken by the
// other's predecessor, so an implementation that only lets the lowest waiter
// through still hangs the rest.
func TestSDKTurnShaping_TwoStrandedCallsBothComplete(t *testing.T) {
	counter := newTurnShapeCounter()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan int, 2)
	for _, index := range []int{1, 2} {
		w := shapingWrapper(counter, &blockingTool{})
		go func(idx int) {
			_, _ = w.Run(toolcallctx.WithToolCall(ctx, sdkToolCallFor("c", idx)), sdktools.InOut{Value: json.RawMessage(`{}`)})
			done <- idx
		}(index)
	}
	for got := 0; got < 2; got++ {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatalf("stranded calls hung; completed %d of 2", got)
		}
	}
}

// Guard against over-correcting: while a lower index is genuinely still
// running, a higher index must WAIT for it, so parallel batches keep charging
// the turn budget in call order.
func TestSDKTurnShaping_StillOrdersWhileLowerIndexRuns(t *testing.T) {
	counter := newTurnShapeCounter()
	slow := &blockingTool{gateDone: make(chan struct{})}
	fast := &blockingTool{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	shaped := make(chan int, 2)
	go func() {
		_, _ = shapingWrapper(counter, slow).Run(
			toolcallctx.WithToolCall(ctx, sdkToolCallFor("c0", 0)), sdktools.InOut{Value: json.RawMessage(`{}`)})
		shaped <- 0
	}()
	// Let the slow call enter Run and be counted in flight.
	deadline := time.Now().Add(2 * time.Second)
	for slow.entered.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("slow tool never entered Run")
		}
		time.Sleep(time.Millisecond)
	}
	go func() {
		_, _ = shapingWrapper(counter, fast).Run(
			toolcallctx.WithToolCall(ctx, sdkToolCallFor("c1", 1)), sdktools.InOut{Value: json.RawMessage(`{}`)})
		shaped <- 1
	}()

	select {
	case idx := <-shaped:
		t.Fatalf("index %d shaped while index 0 was still running; ordering was dropped", idx)
	case <-time.After(250 * time.Millisecond):
		// Correct: the higher index is waiting on the running lower one.
	}

	close(slow.gateDone)
	first := <-shaped
	second := <-shaped
	if first != 0 || second != 1 {
		t.Fatalf("shaping order = %d then %d, want 0 then 1", first, second)
	}
}
