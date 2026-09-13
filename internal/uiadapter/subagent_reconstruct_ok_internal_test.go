package uiadapter

import (
	"testing"
)

// C7's sibling guard (see .agents/memories/sibling-implementations-drift):
// toolCallSummariesToPortsToolCalls is the SECOND producer of history
// ToolCalls - it serves both the resumed-session reconstruction path
// (registerDispatchedTask) and the resolved-ref path (applyResolvedToolCalls).
// If only the live event path recorded ToolCall.OK, every resumed dispatch
// row would silently render all of its child calls as successes.
func TestToolCallSummariesToPortsToolCallsRecordsOK(t *testing.T) {
	toolCalls := toolCallSummariesToPortsToolCalls([]toolCallSummary{
		{ToolCallID: "c1", Name: "read_file", Input: `{"path":"a.go"}`, Output: "48 lines"},
		{ToolCallID: "c2", Name: "edit", Input: `{"path":"b.go"}`, Output: "error: permission denied"},
	})
	if len(toolCalls) != 2 {
		t.Fatalf("got %d tool calls, want 2", len(toolCalls))
	}
	if !toolCalls[0].OK {
		t.Errorf("a summary with a clean output recorded OK = false: %+v", toolCalls[0])
	}
	if toolCalls[1].OK {
		t.Errorf("a summary with an error output recorded OK = true: %+v", toolCalls[1])
	}
}
