package contextmgr

import (
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func TestPlanThresholdAndTarget(t *testing.T) {
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: "sys"},
		{Role: provider.RoleUser, Content: "objective"},
	}
	before, err := provider.EstimateRequestCost(messages, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	budget := before * 5 / 4
	if budget <= 0 || percentFloor(budget, 4, 5) != before {
		t.Fatalf("fixture cost=%d cannot exercise exact trigger with budget=%d", before, budget)
	}

	below, err := Plan(PlanInput{Messages: messages, Budget: budget + 2})
	if err != nil {
		t.Fatal(err)
	}
	if below.Compacted {
		t.Fatal("request below trigger was compacted")
	}

	exact, err := Plan(PlanInput{Messages: messages, Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	if !exact.Compacted || exact.TriggerTokens != before {
		t.Fatalf("exact trigger result = %+v", exact)
	}
	if exact.TargetTokens != budget/2 || exact.AfterTokens != before {
		t.Fatalf("target accounting = %+v, want target=%d cost=%d", exact, budget/2, before)
	}
	if exact.IdempotencyKey == "" || exact.IdempotencyKey != mustPlanKey(t, messages, budget) {
		t.Fatalf("unstable or missing idempotency key: %q", exact.IdempotencyKey)
	}

	if _, err := Plan(PlanInput{Messages: messages, Budget: before - 1}); !errors.Is(err, contextstate.ErrPromptBudgetExceeded) {
		t.Fatalf("one-token-over hard budget error = %v", err)
	}
}

func TestPlanForceCompactsBelowThreshold(t *testing.T) {
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "current objective"},
		{Role: provider.RoleAssistant, Content: "older answer"},
	}
	budget, err := provider.EstimateRequestCost(messages, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Plan(PlanInput{Messages: messages, Budget: budget + 100, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Compacted {
		t.Fatalf("forced plan was not compacted: %+v", plan)
	}
}

func TestPlanRetainsObjectiveToolExchangeAndSourceRange(t *testing.T) {
	call := plannerToolCall("call-new", "read_file", `{"path":"x"}`)
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: strings.Repeat("old ", 80)},
		{Role: provider.RoleAssistant, Content: strings.Repeat("old answer ", 80)},
		{Role: provider.RoleUser, Content: "current objective"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call}},
		{Role: provider.RoleTool, ToolCallID: call.ID, Name: call.Function.Name, Content: "safe result"},
	}
	eventOne := contextstate.SourceEvent{ID: contextstate.SourceID{SessionID: "session", Sequence: 4}, Kind: "message", Role: "user", Provenance: "host", RedactionStatus: "metadata", Size: 1}
	eventTwo := eventOne
	eventTwo.ID.Sequence = 5
	plan, err := Plan(PlanInput{
		Messages: messages, Budget: 120, SourceEvents: []contextstate.SourceEvent{eventOne, eventTwo}, RecentTail: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Compacted {
		t.Fatalf("expected compaction, got %+v", plan)
	}
	if plan.SourceRange.Start.Sequence != 4 || plan.SourceRange.End.Sequence != 5 {
		t.Fatalf("source range = %+v", plan.SourceRange)
	}
	if !containsPlannerMessage(plan.Messages, provider.RoleUser, "current objective") {
		t.Fatal("current objective was not retained")
	}
	if !containsPlannerToolCall(plan.Messages, call.ID) || !containsPlannerToolResult(plan.Messages, call.ID) {
		t.Fatal("latest tool exchange was split or dropped")
	}
	if containsPlannerMessage(plan.Messages, provider.RoleUser, strings.Repeat("old ", 80)) {
		t.Fatal("old turn survived bounded retention")
	}
	if err := provider.ValidateToolPairing(plan.Messages); err != nil {
		t.Fatalf("retained shape invalid: %v", err)
	}
}

func TestPlanRejectsInvalidToolShapes(t *testing.T) {
	validCall := plannerToolCall("call-1", "read_file", `{}`)
	cases := []struct {
		name string
		msgs []provider.Message
	}{
		{"duplicate call IDs", []provider.Message{{Role: provider.RoleUser, Content: "q"}, {Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{validCall, validCall}}}},
		{"multiple results", []provider.Message{{Role: provider.RoleUser, Content: "q"}, {Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{validCall}}, {Role: provider.RoleTool, ToolCallID: "call-1"}, {Role: provider.RoleTool, ToolCallID: "call-1"}}},
		{"orphan result", []provider.Message{{Role: provider.RoleUser, Content: "q"}, {Role: provider.RoleTool, ToolCallID: "missing"}}},
		{"unterminated call", []provider.Message{{Role: provider.RoleUser, Content: "q"}, {Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{validCall}}}},
		{"id-less result", []provider.Message{{Role: provider.RoleUser, Content: "q"}, {Role: provider.RoleTool, Content: "result"}}},
		{"bare assistant", []provider.Message{{Role: provider.RoleUser, Content: "q"}, {Role: provider.RoleAssistant}}},
		{"unsupported role", []provider.Message{{Role: "developer", Content: "no"}}},
		{"malformed call", []provider.Message{{Role: provider.RoleUser, Content: "q"}, {Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{plannerToolCall("call-2", "read_file", "not-json")}}, {Role: provider.RoleTool, ToolCallID: "call-2"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Plan(PlanInput{Messages: tc.msgs, Budget: 100}); !errors.Is(err, contextstate.ErrInvalidDTO) {
				t.Fatalf("error = %v, want ErrInvalidDTO", err)
			}
		})
	}
}

func TestPlanRejectsOversizedCurrentObjectiveLocally(t *testing.T) {
	objective := strings.Repeat("objective ", 200)
	_, err := Plan(PlanInput{
		Messages: []provider.Message{{Role: provider.RoleSystem, Content: "sys"}, {Role: provider.RoleUser, Content: objective}},
		Budget:   20,
	})
	if !errors.Is(err, contextstate.ErrPromptBudgetExceeded) {
		t.Fatalf("error = %v, want prompt budget overflow", err)
	}
}

func plannerToolCall(id, name, args string) provider.ToolCall {
	call := provider.ToolCall{ID: id, Type: "function"}
	call.Function.Name = name
	call.Function.Arguments = args
	return call
}

func containsPlannerMessage(messages []provider.Message, role, content string) bool {
	for _, message := range messages {
		if message.Role == role && message.Content == content {
			return true
		}
	}
	return false
}

func containsPlannerToolCall(messages []provider.Message, id string) bool {
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			if call.ID == id {
				return true
			}
		}
	}
	return false
}

func containsPlannerToolResult(messages []provider.Message, id string) bool {
	for _, message := range messages {
		if message.Role == provider.RoleTool && message.ToolCallID == id {
			return true
		}
	}
	return false
}

func mustPlanKey(t *testing.T, messages []provider.Message, budget int) string {
	t.Helper()
	first, err := Plan(PlanInput{Messages: messages, Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	return first.IdempotencyKey
}
