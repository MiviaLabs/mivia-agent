package provider

import (
	"fmt"
	"strings"
	"testing"
	"time"
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

func TestEstimateMessageTokens(t *testing.T) {
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
	tokens := EstimateMessageTokens(m)
	// Must include messageFrameTokens (10) + role + content + tool call overhead
	if tokens < 10 || tokens > 30 {
		t.Fatalf("EstimateMessageTokens=%d, expected ~15-25", tokens)
	}
	empty := Message{Role: RoleUser, Content: ""}
	tokens = EstimateMessageTokens(empty)
	if tokens != messageFrameTokens+estimateTokens("user") {
		t.Fatalf("empty user msg: %d, want %d", tokens, messageFrameTokens+estimateTokens("user"))
	}
}

func TestEstimateToolSchemaCost(t *testing.T) {
	tools := []ToolSpec{
		{"type": "function", "function": map[string]any{"name": "read_file", "parameters": map[string]any{"type": "object"}}},
	}
	cost, err := EstimateToolSchemaCost(tools)
	if err != nil {
		t.Fatal(err)
	}
	if cost <= 0 {
		t.Fatalf("EstimateToolSchemaCost=%d, expected positive", cost)
	}
	// Empty tools should give 0
	cost0, err := EstimateToolSchemaCost(nil)
	if err != nil || cost0 != 0 {
		t.Fatalf("empty tools: cost=%d err=%v", cost0, err)
	}
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
	tokens := MessagesTokens(msgs, ContextAccountingProfile{})
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
	pruned := PruneMessagesKeepTurns(msgs, 999999, ContextAccountingProfile{})
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
	pruned := PruneMessagesKeepTurns(msgs, 70, ContextAccountingProfile{})
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
	pruned := PruneMessagesKeepTurns(msgs, 20, ContextAccountingProfile{})
	if len(pruned) == 0 {
		t.Fatal("expected at least 1 message")
	}
	if pruned[len(pruned)-1].Content != "recent" {
		t.Fatalf("expected 'recent', got %q", pruned[len(pruned)-1].Content)
	}
}

func TestPruneMessagesKeepTurnsSingleMessage(t *testing.T) {
	msgs := []Message{{Role: RoleUser, Content: "hi"}}
	pruned := PruneMessagesKeepTurns(msgs, 1, ContextAccountingProfile{})
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
	tokens := MessagesTokens(msgs, ContextAccountingProfile{})
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

	base, err := EstimateRequestCost([]Message{{Role: RoleUser, Content: "inspect"}}, nil, 0, ContextAccountingProfile{})
	if err != nil {
		t.Fatal(err)
	}
	call := toolCallMsg("call-2", "result")
	call[0].Name = "assistant-name"
	call[1].Name = "read_file"
	withFields, err := EstimateRequestCost(call, []ToolSpec{{"type": "function", "function": map[string]any{"name": "read_file", "parameters": map[string]any{"type": "object"}}}}, 32, ContextAccountingProfile{})
	if err != nil {
		t.Fatal(err)
	}
	if withFields <= base {
		t.Fatalf("request cost did not include call/schema/reserve fields: base=%d with=%d", base, withFields)
	}
	if got, err := RequestTokens(Request{Messages: []Message{{Role: RoleUser, Content: strings.Repeat("x", 8)}}, MaxTokens: intPtr(7)}, ContextAccountingProfile{}); err != nil || got <= 7 {
		t.Fatalf("RequestTokens=%d, err=%v; output reserve was not included", got, err)
	}
	if got, err := RequestTokens(Request{Messages: []Message{{Role: RoleUser, Content: strings.Repeat("x", 8)}}, MaxTokens: nil}, ContextAccountingProfile{}); err != nil || got <= 0 {
		t.Fatalf("RequestTokens nil MaxTokens=%d, err=%v", got, err)
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
	pruned := PruneMessagesKeepTurns(msgs, 100, ContextAccountingProfile{})
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
	tokens := MessagesTokens(msgs, ContextAccountingProfile{})
	pruned := PruneMessagesKeepTurns(msgs, tokens, ContextAccountingProfile{})
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
	pruned := PruneMessagesKeepTurns(msgs, budget, ContextAccountingProfile{})
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
	pruned := PruneMessagesKeepTurns(msgs, 0, ContextAccountingProfile{})
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

func BenchmarkEstimateMessageTokens(b *testing.B) {
	msg := Message{
		Role:    RoleAssistant,
		Content: string(make([]byte, 1024)),
		ToolCalls: []ToolCall{
			{Function: struct {
				Name      string "json:\"name\""
				Arguments string "json:\"arguments\""
			}{Name: "read_file", Arguments: `{"path":"main.go"}`}},
		},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		EstimateMessageTokens(msg)
	}
}

func BenchmarkEstimateToolSchemaCost(b *testing.B) {
	tools := make([]ToolSpec, 10)
	for i := range tools {
		tools[i] = ToolSpec{
			"type": "function",
			"function": map[string]any{
				"name":        fmt.Sprintf("tool_%d", i),
				"parameters":  map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}},
				"description": "A tool that does things with a path argument",
			},
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		EstimateToolSchemaCost(tools)
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

	pruned := PruneMessagesKeepTurns(msgs, 300, ContextAccountingProfile{})
	if got := MessagesTokens(pruned, ContextAccountingProfile{}); got > 300 {
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

// A plain assistant reply between two dropped tool exchanges is context, not
// part of any exchange: it must survive pruning when it fits the budget. The
// old contiguous-region cut removed the whole span between the first dropped
// block's start and the last dropped block's end, silently dropping such
// replies even when the budget had room for them.
func TestPruneMessagesKeepTurnsKeepsPlainAssistantBetweenDroppedExchanges(t *testing.T) {
	msgs := []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleUser, Content: "go"},
	}
	msgs = append(msgs, toolCallMsg("call_0", "data")...)
	msgs = append(msgs, Message{Role: RoleAssistant, Content: "interim note"})
	msgs = append(msgs, toolCallMsg("call_1", "data")...)
	msgs = append(msgs, toolCallMsg("call_2", "data")...)

	// Budget = system + user + the plain reply + the newest exchange. The two
	// older exchanges must be dropped, but the plain reply fits and must stay.
	budget := MessageTokens(msgs[0]) + MessageTokens(msgs[1]) + MessageTokens(msgs[4]) +
		MessageTokens(msgs[7]) + MessageTokens(msgs[8])
	pruned := PruneMessagesKeepTurns(msgs, budget, ContextAccountingProfile{})

	if got := MessagesTokens(pruned, ContextAccountingProfile{}); got > budget {
		t.Fatalf("pruner left %d tokens over a %d budget", got, budget)
	}
	if pruned[0].Role != RoleSystem || pruned[1].Role != RoleUser {
		t.Fatalf("turn header lost: %s, %s", pruned[0].Role, pruned[1].Role)
	}
	// The plain assistant reply between the dropped exchanges must survive.
	found := false
	for _, m := range pruned {
		if m.Role == RoleAssistant && len(m.ToolCalls) == 0 {
			if m.Content != "interim note" {
				t.Fatalf("unexpected plain assistant reply %q", m.Content)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("plain assistant reply between dropped exchanges was pruned")
	}
	// The newest exchange must survive: it is the one the model is answering.
	last := pruned[len(pruned)-1]
	if last.Role != RoleTool || last.ToolCallID != "call_2" {
		t.Fatalf("newest exchange dropped, last=%+v", last)
	}
	if err := ValidateToolPairing(pruned); err != nil {
		t.Fatalf("pruned history is not validly paired: %v", err)
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

	pruned := PruneMessagesKeepTurns(msgs, 250, ContextAccountingProfile{})
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

	pruned := PruneMessagesKeepTurns(msgs, 500, ContextAccountingProfile{})
	if MessagesTokens(pruned, ContextAccountingProfile{}) > 500 {
		t.Fatalf("prompt still over budget: %d tokens", MessagesTokens(pruned, ContextAccountingProfile{}))
	}
	// RepairToolPairing is a no-op on a healthy history; any change means the
	// pruner emitted a shape the API would reject.
	if repaired := RepairToolPairing(pruned); len(repaired) != len(pruned) {
		t.Fatalf("pruned history needed repair: %d -> %d", len(pruned), len(repaired))
	}
	for _, am := range toAPIMessages(pruned, false, false) {
		if am.Role == RoleAssistant && len(am.ToolCalls) == 0 && (am.Content == nil || *am.Content == "") {
			t.Fatal("pruning produced a bare assistant message")
		}
	}
}

func TestEstimateToolSchemaCostMarshalError(t *testing.T) {
	ch := make(chan int)
	tools := []ToolSpec{{"type": "function", "function": map[string]any{"name": "bad", "params": ch}}}
	_, err := EstimateToolSchemaCost(tools)
	if err == nil {
		t.Fatal("expected error for unmarshalable tool spec")
	}
}

func TestEstimateMessagesPromptCostMatchesEstimatePromptCost(t *testing.T) {
	messages := []Message{
		{Role: RoleSystem, Content: "you are a helpful assistant"},
		{Role: RoleUser, Content: "hello there"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{toolCallFor("c1", "read_file", `{"path":"a"}`)}},
		{Role: RoleTool, ToolCallID: "c1", Content: "file body"},
	}
	specs := []ToolSpec{
		{"type": "function", "function": map[string]any{"name": "read_file", "description": "Read a file", "parameters": map[string]any{"type": "object"}}},
		{"type": "function", "function": map[string]any{"name": "grep", "description": "Search", "parameters": map[string]any{"type": "object"}}},
	}
	want, err := EstimatePromptCost(messages, specs, ContextAccountingProfile{})
	if err != nil {
		t.Fatalf("EstimatePromptCost: %v", err)
	}
	schemaCost, err := EstimateToolSchemaCost(specs)
	if err != nil {
		t.Fatalf("EstimateToolSchemaCost: %v", err)
	}
	if got := EstimateMessagesPromptCost(messages, schemaCost, ContextAccountingProfile{}); got != want {
		t.Fatalf("hoisted cost = %d, want %d", got, want)
	}
	if got := EstimateMessagesPromptCost(messages, 0, ContextAccountingProfile{}); got >= want {
		t.Fatalf("zero schema cost = %d, want below the schema-charged %d", got, want)
	}
}

// A tool loop appends one user message and then only assistant/tool messages,
// so the whole run is a single turn. Pruning it must be linear in the history
// size: the old implementation re-scanned MessagesTokens and rebuilt the slice
// once per dropped exchange (O(k*n)), so a single-turn history of tens of
// thousands of exchanges took orders of magnitude longer than the linear
// rewrite. This test pins both correctness (budget respected, system+user
// header kept, pairing intact, newest exchange kept) and the linearity bound
// (5s wall clock): the O(k*n) loop exceeds that bound by an order of
// magnitude while the linear pass finishes in milliseconds.
func TestPruneMessagesKeepTurnsLinearInsideTurn(t *testing.T) {
	const exchanges = 80000
	payload := string(make([]byte, 400)) // ~100 tokens per result
	msgs := []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleUser, Content: "go"},
	}
	for i := 0; i < exchanges; i++ {
		msgs = append(msgs, toolCallMsg(fmt.Sprintf("call_%d", i), payload)...)
	}

	// Budget that fits the header plus exactly the newest exchange: nearly all
	// 80,000 exchanges must be dropped.
	header := MessagesTokens(msgs[:2], ContextAccountingProfile{})
	newest := MessageTokens(msgs[len(msgs)-2]) + MessageTokens(msgs[len(msgs)-1])
	budget := header + newest

	start := time.Now()
	pruned := PruneMessagesKeepTurns(msgs, budget, ContextAccountingProfile{})
	elapsed := time.Since(start)

	if got := MessagesTokens(pruned, ContextAccountingProfile{}); got > budget {
		t.Fatalf("pruner left %d tokens over a %d budget (%d messages)", got, budget, len(pruned))
	}
	if pruned[0].Role != RoleSystem || pruned[1].Role != RoleUser {
		t.Fatalf("turn header lost: %s, %s", pruned[0].Role, pruned[1].Role)
	}
	// The newest exchange must survive: it is the one the model is answering.
	if len(pruned) != 4 {
		t.Fatalf("pruned to %d messages, want 4 (system + user + newest exchange)", len(pruned))
	}
	last := pruned[len(pruned)-1]
	if last.Role != RoleTool || last.ToolCallID != fmt.Sprintf("call_%d", exchanges-1) {
		t.Fatalf("newest exchange dropped, last=%+v", last)
	}
	if err := ValidateToolPairing(pruned); err != nil {
		t.Fatalf("pruned history is not validly paired: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("pruning took %v (>5s): the pass is not linear (regression)", elapsed)
	}
	t.Logf("pruned %d exchanges to %d messages in %v", exchanges, len(pruned), elapsed)

	// Fast path: an under-budget history is returned unchanged.
	under := []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleUser, Content: "hi"},
		{Role: RoleAssistant, Content: "hello"},
	}
	if got := PruneMessagesKeepTurns(under, 999999, ContextAccountingProfile{}); len(got) != len(under) {
		t.Fatalf("under-budget input pruned: %d -> %d", len(under), len(got))
	}

	// A single turn with no tool exchanges cannot shrink below its header: the
	// pruner must return it unchanged rather than dropping the user message.
	plain := []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleUser, Content: string(make([]byte, 800))},
		{Role: RoleAssistant, Content: string(make([]byte, 800))},
	}
	if got := PruneMessagesKeepTurns(plain, 10, ContextAccountingProfile{}); len(got) != len(plain) {
		t.Fatalf("tool-less turn pruned: %d -> %d", len(plain), len(got))
	}
}

// toolCallFor builds a ToolCall whose Function field is an anonymous struct.
func toolCallFor(id, name, args string) ToolCall {
	call := ToolCall{ID: id, Type: "function"}
	call.Function.Name = name
	call.Function.Arguments = args
	return call
}

func TestEstimatorsCountReasoningContent(t *testing.T) {
	// A long ReasoningContent must increase every estimator used by prune,
	// budget, and preflight — not only the message-local MessageTokens helper.
	base := Message{
		Role:    RoleAssistant,
		Content: "short answer",
	}
	withReasoning := Message{
		Role:             RoleAssistant,
		Content:          "short answer",
		ReasoningContent: string(make([]byte, 400)), // ~100 tokens
	}
	if MessageTokens(withReasoning) <= MessageTokens(base) {
		t.Fatalf("MessageTokens must count ReasoningContent: base=%d with=%d",
			MessageTokens(base), MessageTokens(withReasoning))
	}
	if EstimateMessageTokens(withReasoning) <= EstimateMessageTokens(base) {
		t.Fatalf("EstimateMessageTokens must count ReasoningContent: base=%d with=%d",
			EstimateMessageTokens(base), EstimateMessageTokens(withReasoning))
	}
	baseCost, err := EstimateRequestCost([]Message{base}, nil, 0, ContextAccountingProfile{})
	if err != nil {
		t.Fatal(err)
	}
	withCost, err := EstimateRequestCost([]Message{withReasoning}, nil, 0, ContextAccountingProfile{})
	if err != nil {
		t.Fatal(err)
	}
	if withCost <= baseCost {
		t.Fatalf("EstimateRequestCost must count ReasoningContent: base=%d with=%d", baseCost, withCost)
	}
	// Long reasoning should push PruneMessagesKeepTurns to drop older turns.
	msgs := []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleUser, Content: "old"},
		{Role: RoleAssistant, Content: "old-ans", ReasoningContent: string(make([]byte, 800))},
		{Role: RoleUser, Content: "new"},
		{Role: RoleAssistant, Content: "new-ans"},
	}
	// Budget that fits system + newest turn but not the heavy reasoning turn.
	budget := MessageTokens(msgs[0]) + MessageTokens(msgs[3]) + MessageTokens(msgs[4]) + 10
	pruned := PruneMessagesKeepTurns(msgs, budget, ContextAccountingProfile{})
	for _, m := range pruned {
		if m.Content == "old" || (m.Content == "old-ans" && m.ReasoningContent != "") {
			t.Fatalf("expected heavy reasoning turn pruned under budget=%d, got %+v", budget, pruned)
		}
	}
	if len(pruned) < 2 {
		t.Fatalf("expected system+newest kept, got %d", len(pruned))
	}
}

func TestPruneKeepsReasoningWithExchange(t *testing.T) {
	// Whole-exchange prune retains/removes reasoning with its pair; never
	// splits an assistant tool-call from its reasoning_content.
	call := toolCallFor("c1", "read_file", `{"path":"a"}`)
	msgs := []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleUser, Content: "read"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{call}, ReasoningContent: "must stay with the call"},
		{Role: RoleTool, ToolCallID: "c1", Name: "read_file", Content: "data"},
		{Role: RoleUser, Content: "thanks"},
		{Role: RoleAssistant, Content: "welcome"},
	}
	// Generous budget: full history retained, reasoning still on the tool turn.
	kept := PruneMessagesKeepTurns(msgs, 999999, ContextAccountingProfile{})
	if len(kept) != len(msgs) {
		t.Fatalf("under budget pruned unexpectedly: %d", len(kept))
	}
	var sawReasoning bool
	for _, m := range kept {
		if len(m.ToolCalls) > 0 {
			if m.ReasoningContent != "must stay with the call" {
				t.Fatalf("reasoning split from tool-call exchange: %+v", m)
			}
			sawReasoning = true
		}
	}
	if !sawReasoning {
		t.Fatal("tool-call exchange missing after under-budget prune")
	}
	// Tight budget that keeps only the newest user/assistant: the whole prior
	// exchange (including reasoning) must be gone, not half-present.
	tight := MessageTokens(msgs[0]) + MessageTokens(msgs[4]) + MessageTokens(msgs[5]) + 5
	pruned := PruneMessagesKeepTurns(msgs, tight, ContextAccountingProfile{})
	for _, m := range pruned {
		if m.ReasoningContent == "must stay with the call" {
			t.Fatalf("pruned exchange left orphan reasoning: %+v", pruned)
		}
		if len(m.ToolCalls) > 0 || m.ToolCallID == "c1" {
			t.Fatalf("pruned exchange left orphan tool pair: %+v", pruned)
		}
	}
}
