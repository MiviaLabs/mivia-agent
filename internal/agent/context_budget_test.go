package agent

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// TestSDKPromptBudgetPreflightPrunesToSchemaAwareBudget pins that the
// fallback prune path (no context preparation manager, i.e. workflow
// agent loops) trims history with the same accounting the rejection
// check uses. The rejection prices content plus message frames plus
// tool schemas; pruning accounted content only, so a history near the
// budget was pruned "successfully" and then rejected with
// ErrPromptBudgetExceeded once schema cost was added — the exact
// failure recorded in workflow runs ("prompt exceeds model budget
// (72xxx > 72000 tokens)").
func TestSDKPromptBudgetPreflightPrunesToSchemaAwareBudget(t *testing.T) {
	toolSpecs := []provider.ToolSpec{
		{"type": "function", "function": map[string]any{"name": "read_file", "description": strings.Repeat("d", 400)}},
		{"type": "function", "function": map[string]any{"name": "grep", "description": strings.Repeat("d", 400)}},
	}
	schemaCost, err := provider.EstimateToolSchemaCost(toolSpecs)
	if err != nil {
		t.Fatal(err)
	}
	if schemaCost <= 0 {
		t.Fatal("test tool schemas must carry a positive cost")
	}

	// A droppable old turn with large content plus a small current objective.
	// Content tokens fit the budget; content plus schema cost does not, which
	// is the boundary where the old accounting failed.
	big := strings.Repeat("x", 8000)
	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: big},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "c1", Type: "function", Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "read_file", Arguments: "{}"}}}},
		{Role: provider.RoleTool, Content: "result", ToolCallID: "c1"},
		{Role: provider.RoleUser, Content: "continue"},
	}
	contentTokens := provider.MessagesTokens(msgs, provider.ContextAccountingProfile{})
	budget := contentTokens + schemaCost/2

	l := &Loop{Messages: append([]provider.Message(nil), msgs...)}
	opts := Options{MaxContextTokens: budget, AdvertisedToolSpecs: toolSpecs}
	pruned, err := sdkPromptBudgetPreflight(l, opts, msgs)
	if err != nil {
		t.Fatalf("sdkPromptBudgetPreflight must prune to a schema-aware budget, got: %v", err)
	}
	got, err := provider.EstimatePromptCost(pruned, toolSpecs, provider.ContextAccountingProfile{})
	if err != nil {
		t.Fatal(err)
	}
	if got > budget {
		t.Fatalf("estimate %d tokens still exceeds budget %d after schema-aware prune", got, budget)
	}
	if len(pruned) >= len(msgs) {
		t.Fatal("schema-aware prune did not drop the oversized old turn")
	}
	if len(l.Messages) != len(pruned) {
		t.Fatal("pruned history must be written back onto l.Messages")
	}
}

// TestSDKPromptBudgetPreflightStillFailsClosedWhenIrreducible pins that the
// fallback path still rejects a prompt whose mandatory set (system + current
// objective) alone exceeds the schema-aware budget. Pruning must never
// silently ship an over-budget request.
func TestSDKPromptBudgetPreflightStillFailsClosedWhenIrreducible(t *testing.T) {
	toolSpecs := []provider.ToolSpec{
		{"type": "function", "function": map[string]any{"name": "read_file", "description": strings.Repeat("d", 400)}},
	}
	big := strings.Repeat("x", 8000)
	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: big}, // prune keeps the system whole
		{Role: provider.RoleUser, Content: "continue"},
	}
	// Budget far below the irreducible system content plus schema cost, so no
	// amount of turn pruning can fit the request.
	budget := 200

	l := &Loop{Messages: append([]provider.Message(nil), msgs...)}
	opts := Options{MaxContextTokens: budget, AdvertisedToolSpecs: toolSpecs}
	if _, err := sdkPromptBudgetPreflight(l, opts, msgs); err == nil {
		t.Fatal("sdkPromptBudgetPreflight must fail closed when the mandatory set cannot fit the budget")
	}
}
