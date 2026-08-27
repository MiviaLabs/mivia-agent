package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
)

type concurrencyTrackingTool struct {
	name       string
	inFlight   atomic.Int64
	peakFlight atomic.Int64
	release    chan struct{}
	entered    chan struct{}
}

func (c *concurrencyTrackingTool) Name() string        { return c.name }
func (c *concurrencyTrackingTool) Description() string { return "concurrency tracking test tool" }
func (c *concurrencyTrackingTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id": map[string]any{"type": "string"},
		},
	}
}

func (c *concurrencyTrackingTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	curr := c.inFlight.Add(1)
	for {
		peak := c.peakFlight.Load()
		if curr <= peak || c.peakFlight.CompareAndSwap(peak, curr) {
			break
		}
	}
	if c.entered != nil {
		select {
		case c.entered <- struct{}{}:
		default:
		}
	}
	if c.release != nil {
		select {
		case <-c.release:
		case <-ctx.Done():
		}
	}
	c.inFlight.Add(-1)
	return fmt.Sprintf("result from %s", c.name), nil
}

func TestMaxConcurrentToolsSDKParallelDispatch(t *testing.T) {
	releaseCh := make(chan struct{})
	defer close(releaseCh)

	toolA := &concurrencyTrackingTool{name: "tool_a", release: releaseCh}
	toolB := &concurrencyTrackingTool{name: "tool_b", release: releaseCh}
	toolC := &concurrencyTrackingTool{name: "tool_c", release: releaseCh}

	reg := tools.NewRegistry()
	reg.Register(toolA)
	reg.Register(toolB)
	reg.Register(toolC)

	comp := &scriptCompleter{steps: []provider.Response{
		{
			ToolCalls: []provider.ToolCall{
				tc("call_1", "tool_a", `{}`),
				tc("call_2", "tool_b", `{}`),
				tc("call_3", "tool_c", `{}`),
			},
			FinishReason: "tool_calls",
		},
		{Content: "done", FinishReason: "stop"},
	}}

	loop := &Loop{Completer: comp, Tools: reg}
	var eventsMu sync.Mutex
	var toolStarts []Event
	var toolEnds []Event

	opts := Options{
		Model:              "test-model",
		MaxSteps:           5,
		MaxConcurrentTools: 3,
		OnEvent: func(e Event) {
			eventsMu.Lock()
			defer eventsMu.Unlock()
			if e.Kind == EventToolStart {
				toolStarts = append(toolStarts, e)
			} else if e.Kind == EventToolEnd {
				toolEnds = append(toolEnds, e)
			}
		},
	}

	res, err := RunAgentLoopOnce(context.Background(), loop, opts, []provider.Message{
		{Role: provider.RoleUser, Content: "run parallel tools"},
	})
	if err != nil {
		t.Fatalf("RunAgentLoopOnce: %v", err)
	}

	if res.Stop == "" {
		t.Fatal("expected non-empty stop reason")
	}

	eventsMu.Lock()
	defer eventsMu.Unlock()

	// 3 tool calls * (1 queued start + 1 running start) = 6 tool_start events
	if len(toolStarts) != 6 {
		t.Fatalf("expected 6 tool_start events (queued+running), got %d: %+v", len(toolStarts), toolStarts)
	}
	// 3 tool_end events
	if len(toolEnds) != 3 {
		t.Fatalf("expected 3 tool_end events, got %d: %+v", len(toolEnds), toolEnds)
	}

	// Verify all tool call IDs are present
	endIDs := make(map[string]bool)
	for _, e := range toolEnds {
		endIDs[e.ToolCallID] = true
	}
	for _, id := range []string{"call_1", "call_2", "call_3"} {
		if !endIDs[id] {
			t.Errorf("missing tool_end for call ID %s", id)
		}
	}
}

func TestMaxConcurrentToolsSDKWithTurnShaping(t *testing.T) {
	toolA := &concurrencyTrackingTool{name: "tool_a"}
	toolB := &concurrencyTrackingTool{name: "tool_b"}

	reg := tools.NewRegistry()
	reg.Register(toolA)
	reg.Register(toolB)

	comp := &scriptCompleter{steps: []provider.Response{
		{
			ToolCalls: []provider.ToolCall{
				tc("call_1", "tool_a", `{}`),
				tc("call_2", "tool_b", `{}`),
			},
			FinishReason: "tool_calls",
		},
		{Content: "done", FinishReason: "stop"},
	}}

	loop := &Loop{Completer: comp, Tools: reg}
	opts := Options{
		Model:                  "test-model",
		MaxSteps:               5,
		MaxConcurrentTools:     2,
		BatchResultBudgetBytes: 1000,
	}

	res, err := RunAgentLoopOnce(context.Background(), loop, opts, []provider.Message{
		{Role: provider.RoleUser, Content: "run tools"},
	})
	if err != nil {
		t.Fatalf("RunAgentLoopOnce: %v", err)
	}

	var toolResultCount int
	for _, m := range res.History {
		if m.Role == sdkshape.RoleTool {
			toolResultCount++
		}
	}
	if toolResultCount != 2 {
		t.Fatalf("expected 2 RoleTool messages in history, got %d", toolResultCount)
	}
}
