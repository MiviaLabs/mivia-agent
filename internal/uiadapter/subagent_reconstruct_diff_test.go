package uiadapter_test

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// TestPopulateFromToolCalls_DispatchTasksLegacyToolCallsCarryDiff pins
// defect B (finding 4) for the reconstruction path: registerDispatchedTask
// (via subagent_reconstruct.go) copies a legacy persisted tool_calls
// summary's Output into the reconstructed ToolCall but never computes its
// Diff, even though the live subagent path (translateSubagentEnd ->
// translateToolEnd) carries a gated diff for the same tool call. A
// successful edit's inline tool call must reconstruct with a non-nil Diff.
func TestPopulateFromToolCalls_DispatchTasksLegacyToolCallsCarryDiff(t *testing.T) {
	threads := uiadapter.NewSubagentThreads()
	msgs := []ports.Message{
		{
			Role: "assistant",
			ToolCalls: []ports.ToolCall{
				{
					ID:        "call_dispatch_diff",
					Name:      "dispatch_tasks",
					Arguments: `{"tasks":[{"id":"task-edit","prompt":"edit foo","agent":"editor"}]}`,
					Output: `[{"task_id":"task-edit","status":"completed","output":"done",` +
						`"tool_calls":[{"tool_call_id":"call_inner_1","name":"replace_file_content",` +
						`"input":"{\"path\": \"foo/bar.go\"}",` +
						`"output":"updated foo/bar.go (1 replacement, +1 -0)\n--- a/foo/bar.go\n+++ b/foo/bar.go\n@@ -1,1 +1,2 @@\n old\n+new"}]}]`,
				},
			},
		},
	}

	uiadapter.PopulateFromToolCalls(threads, msgs)

	conv, ok := threads.Thread("call_dispatch_diff:task-edit")
	if !ok || conv == nil {
		t.Fatalf("expected thread for task-edit")
	}
	hist := conv.History()
	var inner ports.ToolCall
	var found bool
	for _, m := range hist {
		for _, tc := range m.ToolCalls {
			if tc.ID == "call_inner_1" {
				inner, found = tc, true
			}
		}
	}
	if !found {
		t.Fatalf("reconstructed history missing inner tool call: %+v", hist)
	}
	if inner.Diff == nil || inner.Diff.Path != "foo/bar.go" {
		t.Errorf("reconstructed tool call Diff = %+v, want a diff for foo/bar.go", inner.Diff)
	}
}
