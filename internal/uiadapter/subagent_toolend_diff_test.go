package uiadapter_test

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// TestSubagentTranscriptConversation_ToolEndCarriesDiff pins defect B
// (finding 4): recordToolEnd (folded into applyEvent's KindToolEnd case)
// sets only Output on the recorded ToolCall, never Diff, even though the
// live event it is folding (translateSubagentEnd -> translateToolEnd)
// already carries a gated diff. A successful edit's Diff must land on
// the ToolCall the same way it does in the main session; a failed
// call's body carries no Diff and the ToolCall must stay nil.
func TestSubagentTranscriptConversation_ToolEndCarriesDiff(t *testing.T) {
	successDiff := &uievent.Diff{Path: "foo/bar.go", Added: 1}

	t.Run("successful edit carries Diff", func(t *testing.T) {
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
				OK: true, Result: "updated foo/bar.go", Diff: successDiff,
			},
		})
		hist := conv.History()
		var tc ports.ToolCall
		var found bool
		for _, m := range hist {
			for _, c := range m.ToolCalls {
				if c.ID == "call_1" {
					tc, found = c, true
				}
			}
		}
		if !found {
			t.Fatalf("tool call call_1 not found in history: %+v", hist)
		}
		if tc.Diff == nil || tc.Diff.Path != "foo/bar.go" {
			t.Errorf("tool call Diff = %+v, want the diff carried by the tool_end body", tc.Diff)
		}
	})

	t.Run("failed call keeps Diff nil", func(t *testing.T) {
		conv := uiadapter.NewSubagentTranscriptConversation("worker", ports.ModelInfo{}, nil)
		conv.RecordEvent(uievent.Event{Kind: uievent.KindTurnStart, Body: uievent.TurnStartBody{Input: "edit foo"}})
		conv.RecordEvent(uievent.Event{
			Kind: uievent.KindToolStart,
			Body: uievent.ToolStartBody{ToolCallID: "call_2", Name: "replace_file_content"},
		})
		conv.RecordEvent(uievent.Event{
			Kind: uievent.KindToolEnd,
			Body: uievent.ToolEndBody{
				ToolCallID: "call_2", Name: "replace_file_content",
				OK: false, Result: "error: permission denied",
			},
		})
		hist := conv.History()
		var tc ports.ToolCall
		var found bool
		for _, m := range hist {
			for _, c := range m.ToolCalls {
				if c.ID == "call_2" {
					tc, found = c, true
				}
			}
		}
		if !found {
			t.Fatalf("tool call call_2 not found in history: %+v", hist)
		}
		if tc.Diff != nil {
			t.Errorf("tool call Diff = %+v, want nil for a failed call", tc.Diff)
		}
	})
}
