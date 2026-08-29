package agent

// The verify-main determinism flake (F4): under a parallel worker pool a
// later call can reach the ordering gate before an earlier call's goroutine
// has been SCHEDULED at all. The inFlight-blocked counts then look exactly
// like a skipped-index hole, the waiter escapes, and the shared budget is
// charged in scheduling order instead of index order - identical batches
// produce different kept-bytes splits depending on scheduling luck
// (TestIntegration_ShapedBatchIsDeterministic saw 102251 vs 1899 kept bytes
// for the same call on the same inputs).

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-ai-sdk/toolcallctx"
	sdktools "github.com/MiviaLabs/mivia-ai-sdk/tools"
)

// bigBodyTool returns a fixed large string body immediately.
type bigBodyTool struct{ body string }

func (*bigBodyTool) Name() string            { return "big_body_tool" }
func (*bigBodyTool) ParameterSchema() []byte { return []byte(`{"type":"object"}`) }
func (*bigBodyTool) DecodeArguments(raw []byte) (sdktools.InOut, error) {
	return sdktools.InOut{Value: raw}, nil
}
func (b *bigBodyTool) Run(context.Context, sdktools.InOut) (sdktools.Out, error) {
	return sdktools.Out{Value: b.body}, nil
}

// TestSDKTurnShaping_LateEnteringPredecessorStillChargesFirst pins charging
// order against the scheduling race: index 1 reaches the gate while index
// 0's goroutine has not entered Run yet (a 50ms injected scheduling gap,
// far inside the hole-grace window). Index 1 must wait; index 0 charges
// the budget first; the whole-body survivor is index 0 on every run.
func TestSDKTurnShaping_LateEnteringPredecessorStillChargesFirst(t *testing.T) {
	counter := newTurnShapeCounter()
	body := strings.Repeat("x", 48<<10)
	budget := 64 << 10 // fits exactly one whole body; the other degrades

	mkWrapper := func() *turnShapeWrapper {
		return &turnShapeWrapper{
			inner: &bigBodyTool{body: body}, toolName: "big_body_tool",
			counter: counter, env: newShapeEnv(nil, "s"), budget: budget,
		}
	}

	outs := make([]string, 2)
	var wg sync.WaitGroup
	runCall := func(index int, delay time.Duration) {
		defer wg.Done()
		time.Sleep(delay)
		ctx := toolcallctx.WithToolCall(context.Background(), sdkToolCallFor(fmt.Sprintf("c%d", index), index))
		out, err := mkWrapper().Run(ctx, sdktools.InOut{Value: json.RawMessage(`{}`)})
		if err != nil {
			t.Error(err)
			return
		}
		outs[index], _ = out.Value.(string)
	}
	wg.Add(2)
	go runCall(1, 0)                   // index 1 arrives at the gate first
	go runCall(0, 50*time.Millisecond) // index 0's goroutine is scheduled late
	wg.Wait()

	if len(outs[0]) != len(body) {
		t.Fatalf("index 0 kept %d bytes, want the whole %d: a later call charged the budget first (scheduling order, not index order)", len(outs[0]), len(body))
	}
	if len(outs[1]) >= len(body) {
		t.Fatalf("index 1 kept %d bytes, want degraded (index 0 spent the budget)", len(outs[1]))
	}
}
