package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

type sdkApprovalTestTool struct {
	name    string
	class   tools.ExecutionClass
	started *atomic.Int32
}

func (t *sdkApprovalTestTool) Name() string        { return t.name }
func (t *sdkApprovalTestTool) Description() string { return "test tool" }
func (t *sdkApprovalTestTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}
func (t *sdkApprovalTestTool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: t.class, ResourceKey: "path:" + t.name}
}
func (t *sdkApprovalTestTool) Execute(context.Context, json.RawMessage) (string, error) {
	if t.started != nil {
		t.started.Add(1)
	}
	return "tool executed successfully", nil
}

func TestSDKLoopApprovalWriteClassPromptsAndApprovedCallRuns(t *testing.T) {
	started := &atomic.Int32{}
	tool := &sdkApprovalTestTool{name: "write_tool", class: tools.ExecutionWrite, started: started}
	reg := tools.NewRegistry()
	reg.Register(tool)

	var gateCalls []string
	var pendingEvents []Event
	var mu sync.Mutex

	gate := func(_ context.Context, name string, args json.RawMessage) sdkadapter.ApprovalResult {
		mu.Lock()
		gateCalls = append(gateCalls, name+"("+string(args)+")")
		mu.Unlock()
		return sdkadapter.ApprovalResult{Approved: true}
	}

	comp := &scriptedTurnCompleter{
		steps: []provider.Response{
			{
				FinishReason: "tool_calls",
				ToolCalls:    []provider.ToolCall{tc("call-1", "write_tool", `{"target":"foo"}`)},
			},
			{
				Content:      "final answer",
				FinishReason: "stop",
			},
		},
	}

	loop := &Loop{Completer: comp, Tools: reg}
	opts := Options{
		Model:        "test-model",
		MaxSteps:     3,
		ApprovalGate: gate,
		OnEvent: func(e Event) {
			if e.Kind == EventToolPending {
				mu.Lock()
				pendingEvents = append(pendingEvents, e)
				mu.Unlock()
			}
		},
	}

	res, err := loop.Run(context.Background(), "do it", opts)
	if err != nil {
		t.Fatalf("loop.Run failed: %v", err)
	}
	if res != "final answer" {
		t.Fatalf("got %q, want %q", res, "final answer")
	}
	if started.Load() != 1 {
		t.Fatalf("tool started %d times, want 1", started.Load())
	}
	if len(gateCalls) != 1 {
		t.Fatalf("gate called %d times, want 1", len(gateCalls))
	}
	if len(pendingEvents) != 1 {
		t.Fatalf("pending events count = %d, want 1", len(pendingEvents))
	}
	if pendingEvents[0].Name != "write_tool" || pendingEvents[0].Detail != "write" {
		t.Fatalf("pending event = %+v, want Name: write_tool, Detail: write", pendingEvents[0])
	}
}

func TestSDKLoopApprovalDenialFailsToolTaskWithDenialText(t *testing.T) {
	started := &atomic.Int32{}
	tool := &sdkApprovalTestTool{name: "write_tool", class: tools.ExecutionWrite, started: started}
	reg := tools.NewRegistry()
	reg.Register(tool)

	gate := func(_ context.Context, name string, args json.RawMessage) sdkadapter.ApprovalResult {
		return sdkadapter.ApprovalResult{Approved: false, Err: "forbidden by user"}
	}

	comp := &scriptedTurnCompleter{
		steps: []provider.Response{
			{
				FinishReason: "tool_calls",
				ToolCalls:    []provider.ToolCall{tc("call-1", "write_tool", `{"target":"foo"}`)},
			},
			{
				Content:      "understood denial",
				FinishReason: "stop",
			},
		},
	}

	loop := &Loop{Completer: comp, Tools: reg}
	opts := Options{
		Model:        "test-model",
		MaxSteps:     3,
		ApprovalGate: gate,
	}

	res, err := loop.Run(context.Background(), "do it", opts)
	if err != nil {
		t.Fatalf("loop.Run failed: %v", err)
	}
	if res != "understood denial" {
		t.Fatalf("got %q, want %q", res, "understood denial")
	}
	if started.Load() != 0 {
		t.Fatalf("tool ran %d times after denial, want 0", started.Load())
	}

	// Verify the tool result recorded in history carries the denial string.
	var foundToolResult bool
	for _, m := range loop.Messages {
		if m.Role == provider.RoleTool {
			foundToolResult = true
			if !strings.Contains(m.Content, "tool call denied by user: forbidden by user") {
				t.Fatalf("tool message content = %q, want denial notice", m.Content)
			}
		}
	}
	if !foundToolResult {
		t.Fatal("no RoleTool message found in loop.Messages")
	}
}

func TestSDKLoopApprovalReadClassSkipsGate(t *testing.T) {
	started := &atomic.Int32{}
	tool := &sdkApprovalTestTool{name: "read_tool", class: tools.ExecutionRead, started: started}
	reg := tools.NewRegistry()
	reg.Register(tool)

	var gateCalls int
	gate := func(_ context.Context, name string, args json.RawMessage) sdkadapter.ApprovalResult {
		gateCalls++
		return sdkadapter.ApprovalResult{Approved: false} // would deny if called
	}

	comp := &scriptedTurnCompleter{
		steps: []provider.Response{
			{
				FinishReason: "tool_calls",
				ToolCalls:    []provider.ToolCall{tc("call-1", "read_tool", `{}`)},
			},
			{
				Content:      "done read",
				FinishReason: "stop",
			},
		},
	}

	loop := &Loop{Completer: comp, Tools: reg}
	opts := Options{
		Model:        "test-model",
		MaxSteps:     3,
		ApprovalGate: gate,
	}

	res, err := loop.Run(context.Background(), "read", opts)
	if err != nil {
		t.Fatalf("loop.Run failed: %v", err)
	}
	if res != "done read" {
		t.Fatalf("got %q, want %q", res, "done read")
	}
	if gateCalls != 0 {
		t.Fatalf("gate called %d times for read-class tool, want 0", gateCalls)
	}
	if started.Load() != 1 {
		t.Fatalf("read tool started %d times, want 1", started.Load())
	}
}

func TestSDKLoopApprovalStandingAllowShortCircuitsGate(t *testing.T) {
	started := &atomic.Int32{}
	tool := &sdkApprovalTestTool{name: "write_tool", class: tools.ExecutionWrite, started: started}
	reg := tools.NewRegistry()
	reg.Register(tool)

	standing := sdkadapter.NewApprovalStanding()
	// The key names the CALL: this tool declares a resource key, so the
	// decision generalizes across that resource's other arguments and no
	// further. Seeding by name alone no longer matches anything.
	standing.Allow(sdkadapter.StandingKey{
		Name: "write_tool", Class: tools.ExecutionWrite, ResourceKey: "path:write_tool",
	})

	var gateCalls int
	gate := func(_ context.Context, name string, args json.RawMessage) sdkadapter.ApprovalResult {
		gateCalls++
		return sdkadapter.ApprovalResult{Approved: false}
	}

	comp := &scriptedTurnCompleter{
		steps: []provider.Response{
			{
				FinishReason: "tool_calls",
				ToolCalls:    []provider.ToolCall{tc("call-1", "write_tool", `{}`)},
			},
			{
				Content:      "done write",
				FinishReason: "stop",
			},
		},
	}

	loop := &Loop{Completer: comp, Tools: reg}
	opts := Options{
		Model:            "test-model",
		MaxSteps:         3,
		ApprovalGate:     gate,
		ApprovalStanding: standing,
	}

	res, err := loop.Run(context.Background(), "write", opts)
	if err != nil {
		t.Fatalf("loop.Run failed: %v", err)
	}
	if res != "done write" {
		t.Fatalf("got %q, want %q", res, "done write")
	}
	if gateCalls != 0 {
		t.Fatalf("gate called %d times under standing allow, want 0", gateCalls)
	}
	if started.Load() != 1 {
		t.Fatalf("tool started %d times, want 1", started.Load())
	}
}

func TestSDKLoopApprovalStandingDenyRejectsWithoutGate(t *testing.T) {
	started := &atomic.Int32{}
	tool := &sdkApprovalTestTool{name: "write_tool", class: tools.ExecutionWrite, started: started}
	reg := tools.NewRegistry()
	reg.Register(tool)

	standing := sdkadapter.NewApprovalStanding()
	// The key names the CALL: this tool declares a resource key, so the
	// decision generalizes across that resource's other arguments and no
	// further. Seeding by name alone no longer matches anything.
	standing.Deny(sdkadapter.StandingKey{
		Name: "write_tool", Class: tools.ExecutionWrite, ResourceKey: "path:write_tool",
	})

	var gateCalls int
	gate := func(_ context.Context, name string, args json.RawMessage) sdkadapter.ApprovalResult {
		gateCalls++
		return sdkadapter.ApprovalResult{Approved: true}
	}

	comp := &scriptedTurnCompleter{
		steps: []provider.Response{
			{
				FinishReason: "tool_calls",
				ToolCalls:    []provider.ToolCall{tc("call-1", "write_tool", `{}`)},
			},
			{
				Content:      "denied acknowledged",
				FinishReason: "stop",
			},
		},
	}

	loop := &Loop{Completer: comp, Tools: reg}
	opts := Options{
		Model:            "test-model",
		MaxSteps:         3,
		ApprovalGate:     gate,
		ApprovalStanding: standing,
	}

	res, err := loop.Run(context.Background(), "write", opts)
	if err != nil {
		t.Fatalf("loop.Run failed: %v", err)
	}
	if res != "denied acknowledged" {
		t.Fatalf("got %q, want %q", res, "denied acknowledged")
	}
	if gateCalls != 0 {
		t.Fatalf("gate called %d times under standing deny, want 0", gateCalls)
	}
	if started.Load() != 0 {
		t.Fatalf("tool started %d times after standing deny, want 0", started.Load())
	}
}
