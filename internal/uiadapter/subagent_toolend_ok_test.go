package uiadapter_test

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// C7 (docs/design/chat-tui-crush-comparison.md §3, Phase 8) renders a
// dispatch row's child tree from
// ports.SubagentThreads.Thread(callID).History()[].ToolCalls - history, not
// the event stream, because the root transcript screen owns that read. The
// screen can only mark a failed child "x" if the outcome is recorded ON the
// ToolCall, so applyEvent's KindToolEnd arm must copy ToolEndBody.OK onto
// the recorded call the way it already copies Result and Diff.
func TestSubagentTranscriptConversation_ToolEndRecordsOK(t *testing.T) {
	record := func(t *testing.T, ok bool, result string) ports.ToolCall {
		t.Helper()
		conv := uiadapter.NewSubagentTranscriptConversation("worker", ports.ModelInfo{}, nil)
		conv.RecordEvent(uievent.Event{Kind: uievent.KindTurnStart, Body: uievent.TurnStartBody{Input: "edit foo"}})
		conv.RecordEvent(uievent.Event{
			Kind: uievent.KindToolStart,
			Body: uievent.ToolStartBody{ToolCallID: "call_1", Name: "replace_file_content"},
		})
		conv.RecordEvent(uievent.Event{
			Kind: uievent.KindToolEnd,
			Body: uievent.ToolEndBody{
				ToolCallID: "call_1", Name: "replace_file_content",
				OK: ok, Result: result,
			},
		})
		hist := conv.History()
		for _, m := range hist {
			for _, c := range m.ToolCalls {
				if c.ID == "call_1" {
					return c
				}
			}
		}
		t.Fatalf("tool call call_1 not found in history: %+v", hist)
		return ports.ToolCall{}
	}

	t.Run("failed child tool.end records OK false", func(t *testing.T) {
		tc := record(t, false, "error: permission denied")
		if tc.OK {
			t.Error("a failed child tool.end recorded OK = true; the child tree would render '+' for a failure")
		}
	})

	t.Run("successful child tool.end records OK true", func(t *testing.T) {
		tc := record(t, true, "updated foo/bar.go")
		if !tc.OK {
			t.Error("a successful child tool.end recorded OK = false; the child tree would render 'x' for a success")
		}
	})
}
