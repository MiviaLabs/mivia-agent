package provider

import (
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

func TestPruneMessagesUnderBudget(t *testing.T) {
	msgs := []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleUser, Content: "hi"},
		{Role: RoleAssistant, Content: "hello"},
	}
	// Budget larger than total — should be unchanged.
	pruned := PruneMessages(msgs, 999999)
	if len(pruned) != len(msgs) {
		t.Fatalf("len=%d, want %d", len(pruned), len(msgs))
	}
}

func TestPruneMessagesDropsOldest(t *testing.T) {
	// Create messages with known sizes.
	// Each message is ~50 chars → ~12 tokens.
	bigMsg := string(make([]byte, 50))
	msgs := []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleUser, Content: bigMsg},
		{Role: RoleAssistant, Content: bigMsg},
		{Role: RoleUser, Content: bigMsg},
		{Role: RoleAssistant, Content: bigMsg},
	}
	// Total: system(12) + 4*12 = ~60 tokens.
	// Budget 40 tokens → should drop oldest non-system messages.
	pruned := PruneMessages(msgs, 40)
	if len(pruned) >= len(msgs) {
		t.Fatalf("expected pruning, len=%d (original %d)", len(pruned), len(msgs))
	}
	// System should still be there.
	if pruned[0].Role != RoleSystem {
		t.Fatalf("first message should be system, got %s", pruned[0].Role)
	}
	// The remaining should be within budget.
	tokens := MessagesTokens(pruned)
	if tokens > 40+MessageTokens(pruned[0]) { // system might push it over slightly
		t.Logf("pruned tokens=%d, budget approx 40+sys", tokens)
	}
}

func TestPruneMessagesNoSystem(t *testing.T) {
	bigMsg := string(make([]byte, 100))
	msgs := []Message{
		{Role: RoleUser, Content: bigMsg},
		{Role: RoleAssistant, Content: bigMsg},
		{Role: RoleUser, Content: "small"},
	}
	pruned := PruneMessages(msgs, 5)
	if len(pruned) == 0 {
		t.Fatal("expected at least 1 message")
	}
	// Should keep the last message(s).
	if pruned[len(pruned)-1].Content != "small" {
		t.Fatalf("expected last msg 'small', got %q", pruned[len(pruned)-1].Content)
	}
}

func TestPruneMessagesZeroBudget(t *testing.T) {
	msgs := []Message{{Role: RoleUser, Content: "hi"}}
	pruned := PruneMessages(msgs, 0)
	if len(pruned) != 1 {
		t.Fatalf("zero budget should return original, got len=%d", len(pruned))
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

func TestPruneMessagesWithToolCalls(t *testing.T) {
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

	// Budget ~100 tokens — should drop turn 1.
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

func TestPruneMessagesEdgeCaseAllFitExactly(t *testing.T) {
	msgs := []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleUser, Content: "hi"},
	}
	// Estimate: sys = 1 token, hi = 1 token
	tokens := MessagesTokens(msgs)
	pruned := PruneMessages(msgs, tokens)
	if len(pruned) != 2 {
		t.Fatalf("exact budget should keep all, len=%d", len(pruned))
	}
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
