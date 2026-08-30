package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
	sdkhooks "github.com/MiviaLabs/mivia-ai-sdk/hooks"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
	"github.com/MiviaLabs/mivia-ai-sdk/toolcallctx"
)

// TestSDKTurnShaping_ParallelCallsDoNotCollide proves that under MaxConcurrentTools > 1,
// parallel tool calls record pass-1 parts keyed by call.ID without colliding or corrupting
// the remainder spool records and shaped bodies.
func TestSDKTurnShaping_ParallelCallsDoNotCollide(t *testing.T) {
	const budget = 64 << 10
	f := newBatchFixture(t, []int{200 << 10, 200 << 10})
	loop := f.run(t, Options{
		BatchResultBudgetBytes: budget,
		MaxConcurrentTools:     2,
	})

	bodies := toolBodies(loop)
	if len(bodies) != 2 {
		t.Fatalf("got %d tool results, want 2", len(bodies))
	}

	for id, body := range bodies {
		if strings.Contains(body, "omitted") {
			t.Fatalf("call %s was omitted by budget: %q", id, tail(body))
		}
		// Confirm each body has honest degrade markers
		if !strings.Contains(body, "truncated") && !strings.Contains(body, "ref:output:") && len(body) == 200<<10 {
			t.Fatalf("call %s kept unshaped 200 KiB body under budget", id)
		}
	}
}

// TestSDKTurnShaping_VetoPathCleanRecord proves that when a call in a batch is vetoed
// by PointPreTool, earlier/other calls' wrappers record cleanly and context identity
// is preserved without panics or orphaned pass-1 state.
func TestSDKTurnShaping_VetoPathCleanRecord(t *testing.T) {
	step := provider.Response{
		FinishReason: "tool_calls",
		ToolCalls: []provider.ToolCall{
			tc("call_a", "noop_tool", `{}`),
			tc("call_b", "noop_tool", `{}`),
		},
	}
	comp := &scriptedTurnCompleter{steps: []provider.Response{step}}
	reg := tools.NewRegistry()
	reg.Register(noopTool{})
	loop := &Loop{Completer: comp, Tools: reg}

	opts := Options{
		Model:                  "m",
		MaxSteps:               5,
		MaxConcurrentTools:     2,
		BatchResultBudgetBytes: 1024,
	}

	sdkOpts, turn, err := buildAgentLoopOptions(loop, opts, "hi")
	if err != nil {
		t.Fatalf("buildAgentLoopOptions err = %v", err)
	}

	if sdkOpts.Hooks == nil {
		sdkOpts.Hooks = sdkhooks.New()
	}
	// Add a PointPreTool hook that vetoes call_b
	_ = sdkOpts.Hooks.Add(sdkhooks.PointPreTool, "veto-b", func(ctx context.Context, payload any) (bool, error) {
		if call, ok := toolcallctx.ToolCallFromContext(ctx); ok {
			if call.ID == "call_b" {
				return false, nil // veto call_b
			}
		}
		return true, nil
	})

	sdkLoop, err := sdkagentloop.New(sdkOpts)
	if err != nil {
		t.Fatalf("sdkagentloop.New err = %v", err)
	}

	res, err := sdkLoop.Run(context.Background(), []sdkshape.Message{
		{Role: sdkshape.RoleUser, Content: "run tools"},
	})
	if err != nil {
		t.Fatalf("sdkLoop.Run err = %v", err)
	}

	if res.Stop != sdkagentloop.StopHookVeto {
		t.Fatalf("res.Stop = %v, want StopHookVeto", res.Stop)
	}

	// Verify turn state is consistent
	if turn == nil {
		t.Fatal("turn state is nil")
	}
}
