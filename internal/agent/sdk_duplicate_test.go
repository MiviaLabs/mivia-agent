package agent

// SDK-path duplicate-delivery tool_end vocabulary parity: the
// dispatcher shim surfaces duplicateDeliveryNotice on the
// model-visible body when the dedup cache serves the second of two
// identical calls, but the matching EventToolEnd Detail must also
// carry the legacy "(duplicate)" suffix the legacy path emits
// (loop_tools.go toolEndDetail). A duplicate of a successful call
// reports "completed (duplicate)"; a duplicate of a failed call
// reports "failed (duplicate)". A plain non-duplicate call stays
// "completed" (regression guard).

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// dupPayloadTool is a write-class tool that returns a fixed body. Two
// identical calls in one step dedup at the dispatcher; the second call
// is the duplicate under test.
type dupPayloadTool struct{}

func (dupPayloadTool) Name() string               { return "dup-payload" }
func (dupPayloadTool) Description() string        { return "dup payload" }
func (dupPayloadTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (dupPayloadTool) Capability(json.RawMessage) tools.Capability {
	// ExecutionWrite dedups; ExecutionRead does not. Set the write
	// class so two identical calls in one step exercise the
	// duplicate-cache branch.
	return tools.Capability{Class: tools.ExecutionWrite, ResourceKey: "path:dup-payload", MaxResultBytes: 1024}
}
func (dupPayloadTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "ok-payload", nil
}

// dupFailedRunCommandTool is the run_command duplicate: the result body
// carries "exit=1" so toolResultBodyFailed (loop_tools.go:211) reports
// failure when scanned against the ORIGINAL body the dedup cache served.
// The Execute call returns nil so the failure lives only in the body
// header (the run_command contract the helper was built for).
type dupFailedRunCommandTool struct{}

func (dupFailedRunCommandTool) Name() string               { return "run_command" }
func (dupFailedRunCommandTool) Description() string        { return "dup failing run" }
func (dupFailedRunCommandTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (dupFailedRunCommandTool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionWrite, ResourceKey: "path:dup-run", MaxResultBytes: 1024}
}
func (dupFailedRunCommandTool) Execute(context.Context, json.RawMessage) (string, error) {
	// Header shape mirrors run_command's composeResult output (tools/run.go):
	// the "exit=N" line is what toolResultBodyFailed scans for.
	return "command: /bin/false\ncwd: .\nexit=1\n", nil
}

// captureSDKToolEndEvents runs one SDK turn whose first step issues the
// given calls (identical calls with fresh IDs, in one step) and returns
// only the EventToolEnd events in arrival order.
func captureSDKToolEndEvents(t *testing.T, reg *tools.Registry, calls []provider.ToolCall) []Event {
	t.Helper()
	steps := []provider.Response{
		{ToolCalls: calls, FinishReason: "tool_calls"},
		{Content: "final answer", FinishReason: "stop"},
	}
	comp := &scriptCompleter{steps: steps}
	l := &Loop{Completer: comp, Tools: reg}
	var mu sync.Mutex
	var got []Event
	opts := Options{
		Model:    "m",
		MaxSteps: 5,
		OnEvent: func(e Event) {
			mu.Lock()
			got = append(got, e)
			mu.Unlock()
		},
	}
	if _, err := l.Run(context.Background(), "go", opts); err != nil {
		t.Fatalf("Run(sdk): %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	return toolEventsOf(got, EventToolEnd)
}

// TestRunAgentLoopOnce_DuplicateEmitsCompletedDuplicateDetail pins the
// SDK path's duplicate-vocabulary contract for a SUCCESSFUL
// duplicated call: the FIRST tool_end Detail stays "completed"; the
// SECOND tool_end Detail (the dedup-cache-served call) carries
// "completed (duplicate)" with the second call's ToolCallID.
func TestRunAgentLoopOnce_DuplicateEmitsCompletedDuplicateDetail(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(dupPayloadTool{})
	ends := captureSDKToolEndEvents(t, reg, []provider.ToolCall{
		tc("dup-1", "dup-payload", `{"k":"v"}`),
		tc("dup-2", "dup-payload", `{"k":"v"}`),
	})
	if len(ends) != 2 {
		t.Fatalf("tool_end count = %d, want 2", len(ends))
	}
	first, second := ends[0], ends[1]
	if first.ToolCallID != "dup-1" {
		t.Fatalf("first tool_end ToolCallID = %q, want dup-1", first.ToolCallID)
	}
	if first.Detail != "completed" {
		t.Fatalf("first tool_end Detail = %q, want completed", first.Detail)
	}
	if second.ToolCallID != "dup-2" {
		t.Fatalf("second tool_end ToolCallID = %q, want dup-2", second.ToolCallID)
	}
	if second.Detail != "completed (duplicate)" {
		t.Fatalf("second tool_end Detail = %q, want completed (duplicate)", second.Detail)
	}
}

// TestRunAgentLoopOnce_DuplicateOfFailedRunCommandMarksFailedDuplicate
// pins the FAILED side of the duplicate vocabulary: the dispatcher
// shim must carry the OWNER's original body (the run_command header
// with exit=1) through to the EventToolEnd render, so
// toolResultBodyFailed scans it and toolEndDetail emits
// "failed (duplicate)".
func TestRunAgentLoopOnce_DuplicateOfFailedRunCommandMarksFailedDuplicate(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(dupFailedRunCommandTool{})
	ends := captureSDKToolEndEvents(t, reg, []provider.ToolCall{
		tc("frun-1", "run_command", `{"argv":["false"]}`),
		tc("frun-2", "run_command", `{"argv":["false"]}`),
	})
	if len(ends) != 2 {
		t.Fatalf("tool_end count = %d, want 2", len(ends))
	}
	first, second := ends[0], ends[1]
	if first.ToolCallID != "frun-1" {
		t.Fatalf("first tool_end ToolCallID = %q, want frun-1", first.ToolCallID)
	}
	// Owner of a run_command body with exit=1 still emits "failed"
	// (the failure is in the header the dispatcher sees; toolEndDetail
	// hits toolResultBodyFailed on the model-visible body).
	if first.Detail != "failed" {
		t.Fatalf("first tool_end Detail = %q, want failed", first.Detail)
	}
	if second.ToolCallID != "frun-2" {
		t.Fatalf("second tool_end ToolCallID = %q, want frun-2", second.ToolCallID)
	}
	if second.Detail != "failed (duplicate)" {
		t.Fatalf("second tool_end Detail = %q, want failed (duplicate)", second.Detail)
	}
	// Regression guard: uiadapter's ok derivation treats any Detail that
	// does NOT start with "failed" as success. Confirm the
	// "(duplicate)" suffix preserves that property.
	if strings.HasPrefix(second.Detail, "failed") == strings.HasPrefix(second.Detail, "failed (duplicate)") {
		// tautology sanity check; the real assertion is above.
	}
}
