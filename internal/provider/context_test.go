package provider

import (
	"fmt"
	"strings"
	"testing"
)

func TestEstimateTokens(t *testing.T) {
	cases := []struct {
		input string
		want  int // rough range
	}{
		{"", 0},
		{"a", 1},
		{"hello world", 2}, // 11 chars / 4 = 2
		{"a quick brown fox jumps over the lazy dog", 10}, // ~41/4
		{string(make([]byte, 1000)), 250},
	}
	for _, c := range cases {
		got := estimateTokens(c.input)
		if got != c.want {
			t.Errorf("estimateTokens(%q) = %d, want %d", truncStr(c.input, 20), got, c.want)
		}
	}
}

func truncStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func TestMessageTokens(t *testing.T) {
	m := Message{
		Role:    RoleAssistant,
		Content: "hello world",
		ToolCalls: []ToolCall{
			{Function: struct {
				Name      string "json:\"name\""
				Arguments string "json:\"arguments\""
			}{Name: "read_file", Arguments: `{"path":"a.go"}`}},
		},
	}
	tokens := MessageTokens(m)
	if tokens < 3 || tokens > 20 {
		t.Fatalf("MessageTokens=%d, expected ~5-10", tokens)
	}
}

func TestMessagesTokens(t *testing.T) {
	msgs := []Message{
		{Role: RoleSystem, Content: "you are a bot"},
		{Role: RoleUser, Content: "hi"},
		{Role: RoleAssistant, Content: "hello"},
	}
	tokens := MessagesTokens(msgs)
	if tokens < 3 || tokens > 20 {
		t.Fatalf("MessagesTokens=%d", tokens)
	}
}

func TestPruneMessagesKeepTurnsUnderBudget(t *testing.T) {
	msgs := []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleUser, Content: "hi"},
		{Role: RoleAssistant, Content: "hello"},
	}
	pruned := PruneMessagesKeepTurns(msgs, 999999)
	if len(pruned) != len(msgs) {
		t.Fatalf("under budget should not prune: len=%d", len(pruned))
	}
}

func TestPruneMessagesKeepTurnsDropsOldTurns(t *testing.T) {
	bigMsg := string(make([]byte, 200)) // ~50 tokens each
	msgs := []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleUser, Content: bigMsg},      // turn 1: ~50 tokens
		{Role: RoleAssistant, Content: bigMsg}, // turn 1
		{Role: RoleUser, Content: bigMsg},      // turn 2: ~50 tokens
		{Role: RoleAssistant, Content: bigMsg}, // turn 2
		{Role: RoleUser, Content: "small last turn"},
		{Role: RoleAssistant, Content: "small response"},
	}
	// Budget ~70 tokens: should keep system + most recent turn(s).
	pruned := PruneMessagesKeepTurns(msgs, 70)
	if len(pruned) >= len(msgs) {
		t.Fatalf("expected pruning, len=%d", len(pruned))
	}
	// System should be first.
	if pruned[0].Role != RoleSystem {
		t.Fatalf("first must be system, got %s", pruned[0].Role)
	}
	// Should contain the small messages (last turn).
	hasSmall := false
	for _, m := range pruned {
		if m.Content == "small last turn" || m.Content == "small response" {
			hasSmall = true
		}
	}
	if !hasSmall {
		t.Fatal("pruned should contain the small last turn")
	}
}

func TestPruneMessagesKeepTurnsNoSystem(t *testing.T) {
	bigMsg := string(make([]byte, 200))
	msgs := []Message{
		{Role: RoleUser, Content: bigMsg},
		{Role: RoleAssistant, Content: bigMsg},
		{Role: RoleUser, Content: "recent"},
	}
	pruned := PruneMessagesKeepTurns(msgs, 20)
	if len(pruned) == 0 {
		t.Fatal("expected at least 1 message")
	}
	if pruned[len(pruned)-1].Content != "recent" {
		t.Fatalf("expected 'recent', got %q", pruned[len(pruned)-1].Content)
	}
}

func TestPruneMessagesKeepTurnsSingleMessage(t *testing.T) {
	msgs := []Message{{Role: RoleUser, Content: "hi"}}
	pruned := PruneMessagesKeepTurns(msgs, 1)
	if len(pruned) != 1 {
		t.Fatalf("single message should not be pruned, len=%d", len(pruned))
	}
}

func TestMessagesTokensWithToolCalls(t *testing.T) {
	msgs := []Message{
		{Role: RoleSystem, Content: "sys prompt"},
		{
			Role:    RoleAssistant,
			Content: "let me check",
			ToolCalls: []ToolCall{
				{Function: struct {
					Name      string "json:\"name\""
					Arguments string "json:\"arguments\""
				}{Name: "read_file", Arguments: `{"path":"main.go"}`}},
				{Function: struct {
					Name      string "json:\"name\""
					Arguments string "json:\"arguments\""
				}{Name: "grep", Arguments: `{"pattern":"foo"}`}},
			},
		},
		{Role: RoleTool, Name: "read_file", Content: "package main"},
		{Role: RoleUser, Content: "thanks"},
	}
	tokens := MessagesTokens(msgs)
	if tokens < 5 {
		t.Fatalf("expected at least 5 tokens, got %d", tokens)
	}
}

func TestEstimatorRetainsPairingCompatibility(t *testing.T) {
	healthy := []Message{{Role: RoleUser, Content: "inspect"}}
	healthy = append(healthy, toolCallMsg("call-1", "result")...)
	if err := ValidateToolPairing(healthy); err != nil {
		t.Fatalf("healthy tool history rejected: %v", err)
	}

	broken := append([]Message(nil), healthy[:len(healthy)-1]...)
	if err := ValidateToolPairing(broken); err == nil {
		t.Fatal("unterminated tool call was accepted")
	}
	repaired := RepairToolPairing(broken)
	if len(repaired) != 1 || repaired[0].Role != RoleUser {
		t.Fatalf("legacy repair changed unexpectedly: %+v", repaired)
	}
	if err := ValidateToolPairing(repaired); err != nil {
		t.Fatalf("repaired legacy history is not valid: %v", err)
	}

	base, err := EstimateRequestCost([]Message{{Role: RoleUser, Content: "inspect"}}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	call := toolCallMsg("call-2", "result")
	call[0].Name = "assistant-name"
	call[1].Name = "read_file"
	withFields, err := EstimateRequestCost(call, []ToolSpec{{"type": "function", "function": map[string]any{"name": "read_file", "parameters": map[string]any{"type": "object"}}}}, 32)
	if err != nil {
		t.Fatal(err)
	}
	if withFields <= base {
		t.Fatalf("request cost did not include call/schema/reserve fields: base=%d with=%d", base, withFields)
	}
	if got, err := RequestTokens(Request{Messages: []Message{{Role: RoleUser, Content: strings.Repeat("x", 8)}}, MaxTokens: intPtr(7)}); err != nil || got <= 7 {
		t.Fatalf("RequestTokens=%d, err=%v; output reserve was not included", got, err)
	}
}

func intPtr(value int) *int { return &value }

func TestPruneMessagesKeepTurnsWithToolCalls(t *testing.T) {
	bigMsg := string(make([]byte, 300)) // ~75 tokens
	msgs := []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleUser, Content: bigMsg}, // turn 1
		{
			Role:    RoleAssistant,
			Content: "checking",
			ToolCalls: []ToolCall{{Function: struct {
				Name      string "json:\"name\""
				Arguments string "json:\"arguments\""
			}{Name: "read_file", Arguments: `{"path":"x.go"}`}}},
		},
		{Role: RoleTool, Name: "read_file", Content: bigMsg},
		{Role: RoleUser, Content: "next query"}, // turn 2
		{Role: RoleAssistant, Content: "done"},
	}

	// Budget ~100 tokens - should drop turn 1.
	pruned := PruneMessagesKeepTurns(msgs, 100)
	if len(pruned) >= len(msgs) {
		t.Fatalf("expected pruning, len=%d", len(pruned))
	}
	// Should still have system + turn 2.
	if len(pruned) < 3 {
		t.Fatalf("expected at least 3 messages (sys + turn 2), got %d", len(pruned))
	}
	if pruned[0].Role != RoleSystem {
		t.Fatalf("first must be system, got %s", pruned[0].Role)
	}
	t.Logf("pruned to %d messages", len(pruned))
}

func TestPruneMessagesKeepTurnsEdgeCaseBudgetExact(t *testing.T) {
	msgs := []Message{
		{Role: RoleSystem, Content: "s"},
		{Role: RoleUser, Content: "u1"},
		{Role: RoleAssistant, Content: "a1"},
		{Role: RoleUser, Content: "u2"},
	}
	tokens := MessagesTokens(msgs)
	pruned := PruneMessagesKeepTurns(msgs, tokens)
	if len(pruned) != 4 {
		t.Fatalf("exact budget should keep all, len=%d", len(pruned))
	}
}

func TestPruneMessagesKeepTurns_SystemExceedsBudget(t *testing.T) {
	// Edge case: system prompt alone exceeds maxTokens.
	// Should still keep the system message (minimum viable context).
	msgs := []Message{
		{Role: RoleSystem, Content: "You are a helpful assistant with a very long system prompt that exceeds the token budget by a significant margin."},
		{Role: RoleUser, Content: "hi"},
	}
	budget := 1 // artificially tiny
	pruned := PruneMessagesKeepTurns(msgs, budget)
	if len(pruned) == 0 {
		t.Fatal("PruneMessagesKeepTurns returned empty - system message should always be kept")
	}
	if pruned[0].Role != RoleSystem {
		t.Errorf("first message should be system, got %v", pruned[0].Role)
	}
}

func TestPruneMessagesKeepTurns_ZeroBudget(t *testing.T) {
	// Zero budget should still keep system message.
	msgs := []Message{
		{Role: RoleSystem, Content: "system"},
		{Role: RoleUser, Content: "user"},
	}
	pruned := PruneMessagesKeepTurns(msgs, 0)
	if len(pruned) == 0 {
		t.Fatal("PruneMessagesKeepTurns returned empty with zero budget")
	}
	if pruned[0].Role != RoleSystem {
		t.Errorf("first message should be system, got %v", pruned[0].Role)
	}
}

// toolCallMsg builds an assistant turn announcing a single call, plus the tool
// result answering it - the unit an agentic loop appends per step.
func toolCallMsg(id, payload string) []Message {
	call := ToolCall{ID: id, Type: "function"}
	call.Function.Name = "read_file"
	call.Function.Arguments = `{"path":"` + id + `.go"}`
	return []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{call}},
		{Role: RoleTool, ToolCallID: id, Name: "read_file", Content: payload},
	}
}

// A tool loop appends one user message and then only assistant/tool messages,
// so the whole run is a single turn. Pruning that only drops whole turns has
// nothing to drop and the prompt grows until the provider rejects it mid-run.
func TestPruneMessagesKeepTurnsPrunesInsideNewestTurn(t *testing.T) {
	payload := string(make([]byte, 400)) // ~100 tokens per step
	msgs := []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleUser, Content: "go"},
	}
	for i := 0; i < 20; i++ {
		msgs = append(msgs, toolCallMsg(fmt.Sprintf("call_%d", i), payload)...)
	}

	pruned := PruneMessagesKeepTurns(msgs, 300)
	if got := MessagesTokens(pruned); got > 300 {
		t.Fatalf("pruner left %d tokens over a 300 budget (%d messages)", got, len(pruned))
	}
	if pruned[0].Role != RoleSystem || pruned[1].Role != RoleUser {
		t.Fatalf("turn header lost: %s, %s", pruned[0].Role, pruned[1].Role)
	}
	// The newest exchange must survive: it is the one the model is answering.
	last := pruned[len(pruned)-1]
	if last.Role != RoleTool || last.ToolCallID != "call_19" {
		t.Fatalf("newest exchange dropped, last=%+v", last)
	}
}

// Dropping an assistant tool_call without its results (or the reverse) makes
// the API reject the entire request, so an exchange has to move as one unit.
func TestPruneMessagesKeepTurnsNeverSplitsToolExchanges(t *testing.T) {
	payload := string(make([]byte, 400))
	msgs := []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleUser, Content: "go"},
	}
	for i := 0; i < 12; i++ {
		msgs = append(msgs, toolCallMsg(fmt.Sprintf("call_%d", i), payload)...)
	}

	pruned := PruneMessagesKeepTurns(msgs, 250)
	announced := map[string]bool{}
	for _, m := range pruned {
		for _, c := range m.ToolCalls {
			announced[c.ID] = true
		}
	}
	answered := map[string]bool{}
	for _, m := range pruned {
		if m.Role != RoleTool {
			continue
		}
		if !announced[m.ToolCallID] {
			t.Fatalf("orphaned tool result %q survived pruning", m.ToolCallID)
		}
		answered[m.ToolCallID] = true
	}
	for id := range announced {
		if !answered[id] {
			t.Fatalf("tool call %q kept without its result", id)
		}
	}
}

// End to end over the wire shape: the payload actually sent must stay inside
// the budget and stay well-formed after pruning.
func TestPruneMessagesKeepTurnsWireShapeStaysValid(t *testing.T) {
	payload := string(make([]byte, 400))
	msgs := []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleUser, Content: "go"},
	}
	for i := 0; i < 40; i++ {
		msgs = append(msgs, toolCallMsg(fmt.Sprintf("call_%d", i), payload)...)
	}

	pruned := PruneMessagesKeepTurns(msgs, 500)
	if MessagesTokens(pruned) > 500 {
		t.Fatalf("prompt still over budget: %d tokens", MessagesTokens(pruned))
	}
	// RepairToolPairing is a no-op on a healthy history; any change means the
	// pruner emitted a shape the API would reject.
	if repaired := RepairToolPairing(pruned); len(repaired) != len(pruned) {
		t.Fatalf("pruned history needed repair: %d -> %d", len(pruned), len(repaired))
	}
	for _, am := range toAPIMessages(pruned) {
		if am.Role == RoleAssistant && len(am.ToolCalls) == 0 && (am.Content == nil || *am.Content == "") {
			t.Fatal("pruning produced a bare assistant message")
		}
	}
}
