package provider

import "testing"

// The elision marker is host prose, not model output. Replaying it as
// reasoning_content tells the model it once thought a sentence it never
// thought, and a preserved-thinking dialect (z.ai GLM, clear_thinking:false)
// feeds that fabricated block back into its own reasoning context on every
// later turn of a compacted session. These tests pin who gets it and who
// does not.

// elidedToolCallHistory is the shape compaction leaves behind: a prior
// assistant tool-call turn whose reasoning was elided, its tool result, and a
// live turn that still carries real reasoning.
func elidedToolCallHistory() []Message {
	return []Message{
		{Role: RoleUser, Content: "start"},
		{
			Role:             RoleAssistant,
			ReasoningContent: ReasoningElisionMarker,
			ToolCalls:        []ToolCall{{ID: "call_1", Type: "function"}},
		},
		{Role: RoleTool, ToolCallID: "call_1", Content: "result"},
		{Role: RoleAssistant, Content: "answer", ReasoningContent: "real thinking"},
		{Role: RoleUser, Content: "continue"},
	}
}

// TestReplayOnlyProviderDropsElisionMarker is the fix: z.ai (replay without
// the reject gate) must receive no reasoning field for an elided turn, not a
// fabricated one.
func TestReplayOnlyProviderDropsElisionMarker(t *testing.T) {
	out := toAPIMessages(elidedToolCallHistory(), true, false)
	if len(out) != 5 {
		t.Fatalf("history should survive intact, got %d messages", len(out))
	}
	if out[1].ReasoningContent != "" {
		t.Fatalf("elided reasoning reached the wire: %q", out[1].ReasoningContent)
	}
	if len(out[1].ToolCalls) != 1 || out[2].ToolCallID != "call_1" {
		t.Fatal("dropping the marker must not disturb the tool-call pairing")
	}
	if out[3].ReasoningContent != "real thinking" {
		t.Fatalf("genuine reasoning must still replay, got %q", out[3].ReasoningContent)
	}
}

// TestRejectReasoningLessProviderKeepsElisionMarker pins the documented
// exception: for DeepSeek an assistant tool-call turn with no
// reasoning_content is a 400, so a fabricated block beats a failed request.
func TestRejectReasoningLessProviderKeepsElisionMarker(t *testing.T) {
	out := toAPIMessages(elidedToolCallHistory(), true, true)
	found := false
	for _, m := range out {
		if m.ReasoningContent == ReasoningElisionMarker {
			found = true
		}
	}
	if !found {
		t.Fatal("a reject-reasoning-less provider must still receive the marker")
	}
}

// TestNonReplayProviderNeverSendsReasoning pins that the marker cannot leak
// through a provider that does not replay reasoning at all.
func TestNonReplayProviderNeverSendsReasoning(t *testing.T) {
	for _, m := range toAPIMessages(elidedToolCallHistory(), false, false) {
		if m.ReasoningContent != "" {
			t.Fatalf("non-replay provider sent reasoning: %q", m.ReasoningContent)
		}
	}
}

// TestReplayableReasoning states the rule directly, including the empty case
// the caller already guards.
func TestReplayableReasoning(t *testing.T) {
	for _, tc := range []struct {
		name             string
		reasoningContent string
		reject           bool
		want             bool
	}{
		{"real reasoning replays", "thinking", false, true},
		{"real reasoning replays under reject", "thinking", true, true},
		{"marker is dropped for replay-only", ReasoningElisionMarker, false, false},
		{"marker is kept for reject", ReasoningElisionMarker, true, true},
		{"empty never replays", "", false, false},
		{"empty never replays under reject", "", true, false},
		// Exact equality is deliberate: an elision marker EMBEDDED in
		// otherwise genuine reasoning still replays. Dropping the whole
		// block there would lose real chain-of-thought to remove a
		// fragment, which is the disposition DC-23's probe asks for.
		{"embedded marker still replays", "real thinking " + ReasoningElisionMarker, false, true},
	} {
		if got := replayableReasoning(tc.reasoningContent, tc.reject); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestElidedReasoningOnlyTurnIsDropped pins the shape that only exists once
// the marker stops reaching the wire: a reasoning-only assistant turn (a
// provider that hit its output cap mid-thought) whose reasoning was then
// elided would otherwise serialize as {"role":"assistant","content":""} -
// a turn that says nothing and replays on every later request forever.
func TestElidedReasoningOnlyTurnIsDropped(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "start"},
		{Role: RoleAssistant, ReasoningContent: ReasoningElisionMarker},
		{Role: RoleUser, Content: "continue"},
	}
	out := toAPIMessages(msgs, true, false)
	if len(out) != 2 {
		t.Fatalf("the content-free turn must be dropped, got %d messages: %+v", len(out), out)
	}

	// A reject-reasoning-less provider keeps the marker, so the turn still
	// carries something and must survive.
	if kept := toAPIMessages(msgs, true, true); len(kept) != 3 {
		t.Fatalf("a reject-reasoning-less provider must keep the turn, got %d", len(kept))
	}

	// A reasoning-only turn with GENUINE reasoning is untouched: it still
	// carries the chain-of-thought the provider produced.
	genuine := []Message{
		{Role: RoleUser, Content: "start"},
		{Role: RoleAssistant, ReasoningContent: "real thinking"},
	}
	if out := toAPIMessages(genuine, true, false); len(out) != 2 || out[1].ReasoningContent != "real thinking" {
		t.Fatalf("a genuine reasoning-only turn must survive intact: %+v", out)
	}

	// A turn with tool calls is never dropped by this rule, whatever its
	// reasoning: its tool results reference the call id.
	withCalls := []Message{
		{Role: RoleUser, Content: "start"},
		{Role: RoleAssistant, ReasoningContent: ReasoningElisionMarker, ToolCalls: []ToolCall{{ID: "call_1", Type: "function"}}},
		{Role: RoleTool, ToolCallID: "call_1", Content: "result"},
	}
	if out := toAPIMessages(withCalls, true, false); len(out) != 3 {
		t.Fatalf("a tool-call turn must never be dropped, got %d: %+v", len(out), out)
	}
}
