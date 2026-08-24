package agent

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
	"github.com/MiviaLabs/mivia-ai-sdk/toolcallctx"
	sdktools "github.com/MiviaLabs/mivia-ai-sdk/tools"
)

// sdkToolCallFor builds the SDK-shaped ToolCall used by the wrapper to
// read the call index from context.
func sdkToolCallFor(id string, index int) sdkshape.ToolCall {
	return sdkshape.ToolCall{ID: id, Name: "blocking_tool", Index: index, Arguments: []byte(`{}`)}
}

// blockingTool is a fixture SDK tool whose Run blocks on a gate so a
// caller can stage the race the wrapper's cond-wait has to survive.
// gateDone unlocks the inner Run so the wrapper enters the cond wait
// after Run returns.
type blockingTool struct {
	entered    atomic.Int32
	gateDone   chan struct{}
	gateClosed atomic.Bool
}

func (*blockingTool) Name() string { return "blocking_tool" }
func (*blockingTool) ParameterSchema() []byte {
	return []byte(`{"type":"object"}`)
}
func (*blockingTool) DecodeArguments(raw []byte) (sdktools.InOut, error) {
	return sdktools.InOut{Value: raw}, nil
}
func (b *blockingTool) Run(ctx context.Context, _ sdktools.InOut) (sdktools.Out, error) {
	b.entered.Add(1)
	if b.gateDone != nil {
		select {
		case <-b.gateDone:
		case <-ctx.Done():
			return sdktools.Out{Value: "cancelled"}, ctx.Err()
		}
	}
	return sdktools.Out{Value: "ok"}, nil
}

// TestSDKTurnShaping_CtxCancelWakesCondWait drives turnShapeWrapper.Run
// directly: two parallel calls, both gated, with the higher-Index call's
// gate opened first so the wrapper reaches the cond-wait while the
// lower-Index call is still blocked. Cancelling ctx must release the
// cond-wait; sync.Cond.Wait has no context awareness, so a buggy
// implementation sleeps forever on the wait.
func TestSDKTurnShaping_CtxCancelWakesCondWait(t *testing.T) {
	counter := newTurnShapeCounter()
	t0 := &blockingTool{}
	t1 := &blockingTool{}
	// Two separate gates so the test can release the high-Index call
	// first and observe the cond-wait under cancellation.
	gateHigh := make(chan struct{})
	gateLow := make(chan struct{})
	t1.gateDone = gateHigh
	t0.gateDone = gateLow

	w0 := &turnShapeWrapper{inner: t0, toolName: "blocking_tool", counter: counter, env: newShapeEnv(nil, "s"), cap: 64 << 10, budget: 64 << 10}
	w1 := &turnShapeWrapper{inner: t1, toolName: "blocking_tool", counter: counter, env: newShapeEnv(nil, "s"), cap: 64 << 10, budget: 64 << 10}

	ctx, cancel := context.WithCancel(context.Background())

	type result struct {
		idx  int
		body string
		err  error
	}
	results := make(chan result, 2)
	go func() {
		ctx0 := toolcallctx.WithToolCall(ctx, sdkToolCallFor("c0", 0))
		body, err := w0.Run(ctx0, sdktools.InOut{Value: json.RawMessage(`{}`)})
		results <- result{idx: 0, body: stringFromOut(body), err: err}
	}()
	go func() {
		ctx1 := toolcallctx.WithToolCall(ctx, sdkToolCallFor("c1", 1))
		body, err := w1.Run(ctx1, sdktools.InOut{Value: json.RawMessage(`{}`)})
		results <- result{idx: 1, body: stringFromOut(body), err: err}
	}()

	// Wait for both to enter Run.
	deadline := time.Now().Add(2 * time.Second)
	for t0.entered.Load() == 0 || t1.entered.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("tools did not enter Run within 2s")
		}
		time.Sleep(time.Millisecond)
	}
	// Release the high-Index tool: its wrapper takes the lock, sees
	// nextIndex (0) < callIndex (1), and parks on cond.Wait.
	close(gateHigh)
	// Give the wrapper time to park in the cond wait.
	time.Sleep(100 * time.Millisecond)
	cancel()

	got := 0
	timeout := time.After(3 * time.Second)
	for got < 2 {
		select {
		case r := <-results:
			got++
			_ = r
		case <-timeout:
			t.Fatalf("turnShapeWrapper cond-wait hung on ctx cancel; got=%d entered=%d", got, t0.entered.Load()+t1.entered.Load())
		}
	}
}

func stringFromOut(out sdktools.Out) string {
	if s, ok := out.Value.(string); ok {
		return s
	}
	return ""
}

// TestSDKTurnShaping_ResetInstallsFreshChannel pins the reset path:
// after a wrapper has drained (or aborted) the previous signal channel,
// resetIterationShaping must install a fresh one so a new iteration's
// waiters do not observe a permanently-open pipe that never wakes
// them. The test drives a full two-iteration flow: iteration 1
// aborts, iteration 2 spawns two parallel wrappers and the second one
// must wake when the first one completes (a normal advance on the
// fresh channel). A buggy reset that closes the new channel or fails
// to swap would leave the second wrapper parked.
func TestSDKTurnShaping_ResetInstallsFreshChannel(t *testing.T) {
	counter := newTurnShapeCounter()

	// Burn iteration 1: close the signal channel via abort (simulates a
	// teardown) and assert the next reset makes a fresh one.
	counter.abort()
	if !counter.aborted {
		t.Fatal("abort did not flip aborted")
	}
	select {
	case <-counter.signal:
	default:
		t.Fatal("signal channel not closed after abort")
	}

	resetIterationShapingInto(counter)
	if counter.aborted {
		t.Fatal("reset must clear aborted")
	}
	if counter.nextIndex != 0 {
		t.Fatalf("reset nextIndex=%d, want 0", counter.nextIndex)
	}
	select {
	case <-counter.signal:
		t.Fatal("post-reset channel must not be closed")
	default:
	}

	// Iteration 2: two wrappers, indices 0 and 1. The first one must
	// complete normally and advance the counter; the second must
	// observe the broadcast and proceed.
	done := runResetIterationPair(t, counter)
	if len(done) != 2 {
		t.Fatalf("post-reset iteration hung; got=%d", len(done))
	}
}

// runResetIterationPair runs two blockingTool wrappers against
// the supplied counter and waits for both to finish. Extracted from
// TestSDKTurnShaping_ResetInstallsFreshChannel so the test function
// stays under the 80-LOC soft ceiling.
func runResetIterationPair(t *testing.T, counter *turnShapeCounter) []int {
	t.Helper()
	t0 := &blockingTool{}
	t1 := &blockingTool{}
	t0.gateDone = make(chan struct{})
	t1.gateDone = make(chan struct{})
	w0 := &turnShapeWrapper{inner: t0, toolName: "blocking_tool", counter: counter, env: newShapeEnv(nil, "s"), cap: 64 << 10, budget: 64 << 10}
	w1 := &turnShapeWrapper{inner: t1, toolName: "blocking_tool", counter: counter, env: newShapeEnv(nil, "s"), cap: 64 << 10, budget: 64 << 10}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 2)
	go func() {
		_, _ = w0.Run(toolcallctx.WithToolCall(ctx, sdkToolCallFor("c0", 0)), sdktools.InOut{Value: json.RawMessage(`{}`)})
		done <- 0
	}()
	go func() {
		_, _ = w1.Run(toolcallctx.WithToolCall(ctx, sdkToolCallFor("c1", 1)), sdktools.InOut{Value: json.RawMessage(`{}`)})
		done <- 1
	}()
	waitEnter := func(target int32, msg string) {
		deadline := time.Now().Add(2 * time.Second)
		for t0.entered.Load() < target || t1.entered.Load() < target {
			if time.Now().After(deadline) {
				t.Fatal(msg)
			}
			time.Sleep(time.Millisecond)
		}
	}
	waitEnter(1, "tools did not enter Run within 2s")
	// Release the index-0 wrapper first; it shapes and advances
	// nextIndex, broadcasting on the fresh channel. The index-1
	// wrapper is parked on the same channel and wakes.
	close(t0.gateDone)
	time.Sleep(50 * time.Millisecond)
	close(t1.gateDone)
	got := 0
	var finished []int
	timeout := time.After(3 * time.Second)
	for got < 2 {
		select {
		case idx := <-done:
			got++
			finished = append(finished, idx)
		case <-timeout:
			t.Fatalf("post-reset iteration hung; got=%d entered=%d", got, t0.entered.Load()+t1.entered.Load())
		}
	}
	return finished
}

// resetIterationShapingInto is the test seam for the turn state
// reset; production resets live on sdkTurnState. Mirrors the
// production double-close guard.
func resetIterationShapingInto(c *turnShapeCounter) {
	c.mu.Lock()
	c.nextIndex = 0
	c.aborted = false
	old := c.signal
	c.signal = make(chan struct{})
	owned := !c.closedByAbort
	c.closedByAbort = false
	c.mu.Unlock()
	if owned {
		close(old)
	}
}
