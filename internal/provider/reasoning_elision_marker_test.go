package provider

import "testing"

// compactionReasoningMarker mirrors contextmgr.reasoningElisionMarker. The
// planner replaces stale assistant reasoning with this constant instead of
// the empty string, exactly because of the D2 gate this test pins: with
// RejectReasoningLessToolTurns set, dropReasoningLessToolExchanges wire-drops
// a non-terminal tool-call turn with empty reasoning together with its tool
// results. The marker keeps compacted exchanges on the wire.
const compactionReasoningMarker = "[reasoning elided by context compaction]"

// TestRejectGateKeepsMarkerReasoningToolExchange pins the marker rationale:
// under reject-on, a NON-terminal assistant tool-call turn whose reasoning is
// the compaction marker survives toAPIMessages with its tool result, while
// the same turn with empty reasoning is dropped with its result.
func TestRejectGateKeepsMarkerReasoningToolExchange(t *testing.T) {
	oldCall := toolCall("c-old", "read_file", `{"path":"a"}`)
	newCall := toolCall("c-new", "read_file", `{"path":"b"}`)
	history := func(oldReasoning string) []Message {
		return []Message{
			{Role: RoleUser, Content: "read a then b"},
			// Non-terminal exchange: a later assistant turn follows it.
			{Role: RoleAssistant, ToolCalls: []ToolCall{oldCall}, ReasoningContent: oldReasoning},
			{Role: RoleTool, ToolCallID: "c-old", Name: "read_file", Content: "data-a"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{newCall}, ReasoningContent: "now read b"},
			{Role: RoleTool, ToolCallID: "c-new", Name: "read_file", Content: "data-b"},
		}
	}

	out := toAPIMessages(history(compactionReasoningMarker), true, true)
	if len(out) != 5 {
		t.Fatalf("marker reasoning: expected full history on the wire, got %d: %+v", len(out), out)
	}
	if out[1].ReasoningContent != compactionReasoningMarker || len(out[1].ToolCalls) != 1 || out[1].ToolCalls[0].ID != "c-old" {
		t.Fatalf("marker exchange lost or altered: %+v", out[1])
	}
	if out[2].ToolCallID != "c-old" {
		t.Fatalf("marker exchange tool result lost: %+v", out[2])
	}

	dropped := toAPIMessages(history(""), true, true)
	if len(dropped) != 3 {
		t.Fatalf("empty reasoning: expected user + terminal exchange only, got %d: %+v", len(dropped), dropped)
	}
	for _, am := range dropped {
		if am.ToolCallID == "c-old" || (len(am.ToolCalls) > 0 && am.ToolCalls[0].ID == "c-old") {
			t.Fatalf("empty-reasoning exchange must be dropped: %+v", am)
		}
	}
}
