package agent

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// fakeTool is a trivial tool that immediately returns "ok" for every call.
type fakeTool struct {
	name string
}

func (f *fakeTool) Name() string               { return f.name }
func (f *fakeTool) Description() string        { return "fake tool for testing" }
func (f *fakeTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (f *fakeTool) Capability(_ json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionRead, ResourceKey: "path:."}
}
func (f *fakeTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "ok", nil
}

// countingCompleter is a scriptCompleter that emits tool calls for N steps,
// then returns text "done" on the next call. It also counts total ChatTurn
// invocations via an atomic counter so the test can assert how many loop
// iterations actually ran.
type countingCompleter struct {
	scriptCompleter
	totalCalls atomic.Int64
}

func (c *countingCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	n := c.totalCalls.Add(1)
	// After exhausting scripted steps, return "done" so the loop terminates.
	if int(n) > len(c.steps) {
		return &provider.Response{Content: "done", FinishReason: "stop"}, nil
	}
	return c.scriptCompleter.ChatTurn(ctx, req)
}

// TestLoopUnlimitedStepsRunsPast100 verifies that a loop with MaxSteps=0
// (unlimited) can run more than 100 iterations without being cut short by a
// max-steps guard.
func TestLoopUnlimitedStepsRunsPast100(t *testing.T) {
	const numToolSteps = 110

	// Build 110 scripted responses that each return a tool call.
	steps := make([]provider.Response, numToolSteps)
	for i := 0; i < numToolSteps; i++ {
		steps[i] = provider.Response{
			FinishReason: "tool_calls",
			ToolCalls:    []provider.ToolCall{tc("1", "fake_list", `{}`)},
		}
	}

	comp := &countingCompleter{
		scriptCompleter: scriptCompleter{steps: steps},
	}

	// Create a minimal registry with a single fake tool.
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
	reg.Register(&fakeTool{name: "fake_list"})

	// Count tool executions to verify the loop really ran.
	var toolExecs atomic.Int64
	var mu sync.Mutex
	var events []Event

	loop := &Loop{Completer: comp, Tools: reg}
	text, err := loop.Run(context.Background(), "run many steps", Options{Model: "m",
		MaxSteps: 0, // unlimited
		OnEvent: func(e Event) {
			mu.Lock()
			events = append(events, e)
			mu.Unlock()
			if e.Kind == EventToolEnd {
				toolExecs.Add(1)
			}
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "done" {
		t.Fatalf("reply: got %q, want %q", text, "done")
	}
	if got := toolExecs.Load(); got < 100 {
		t.Fatalf("tool executions: got %d, want >= 100 (loop did not run enough steps)", got)
	}

	// The completer should have been called for all 110 tool steps plus one
	// final "done" call = 111 total.
	if got := comp.totalCalls.Load(); got != int64(numToolSteps+1) {
		t.Fatalf("total ChatTurn calls: got %d, want %d", got, numToolSteps+1)
	}
}
