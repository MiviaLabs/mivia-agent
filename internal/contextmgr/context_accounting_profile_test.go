package contextmgr

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// reasoningTerminalHistory is a valid, Plan()-acceptable history: one user
// objective followed by two resolved assistant tool-call rounds, an OLD one
// (fully superseded) and the TERMINAL one (the newest tool exchange). Only
// the terminal round's ReasoningContent is "the current round" a
// ReasoningBillingTerminalExchange provider would actually bill.
func reasoningTerminalHistory(oldReasoning, terminalReasoning string) []provider.Message {
	return []provider.Message{
		{Role: provider.RoleSystem, Content: "system prompt"},
		{Role: provider.RoleUser, Content: "do the task"},
		{
			Role:             provider.RoleAssistant,
			ReasoningContent: oldReasoning,
			ToolCalls: []provider.ToolCall{{ID: "call-1", Type: "function", Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "read_file", Arguments: "{}"}}},
		},
		{Role: provider.RoleTool, ToolCallID: "call-1", Content: "old tool result"},
		{
			Role:             provider.RoleAssistant,
			ReasoningContent: terminalReasoning,
			ToolCalls: []provider.ToolCall{{ID: "call-2", Type: "function", Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "read_file", Arguments: "{}"}}},
		},
		{Role: provider.RoleTool, ToolCallID: "call-2", Content: "terminal tool result"},
	}
}

// TestPlanTriggerRespectsReasoningBillingProfile is the compaction-trigger
// regression test for the bug this profile fixes: a reasoning-replay
// provider's session keeps ReasoningContent across turns
// (internal/chat/binding.go stripReasoningForProviderSwitch only strips it on
// a provider change), so a planner that always charges ReasoningContent
// inflates `before` by the whole session's accumulated chain-of-thought and
// fires the 80% trigger far below real usage. With the SAME history and
// budget, ReasoningBillingTerminalExchange must not cross the trigger while
// ReasoningBillingAllTurns does.
func TestPlanTriggerRespectsReasoningBillingProfile(t *testing.T) {
	oldReasoning := stringOfLen(4000) // an old, already-resolved round's CoT
	terminalReasoning := stringOfLen(50)
	messages := reasoningTerminalHistory(oldReasoning, terminalReasoning)

	terminalProfile := provider.ContextAccountingProfile{ReasoningBilling: provider.ReasoningBillingTerminalExchange}
	allTurnsProfile := provider.ContextAccountingProfile{ReasoningBilling: provider.ReasoningBillingAllTurns}

	costTerminal, err := provider.EstimateRequestCost(messages, nil, 0, terminalProfile)
	if err != nil {
		t.Fatal(err)
	}
	costAllTurns, err := provider.EstimateRequestCost(messages, nil, 0, allTurnsProfile)
	if err != nil {
		t.Fatal(err)
	}
	if costTerminal >= costAllTurns {
		t.Fatalf("fixture must make all-turns billing strictly more expensive: terminal=%d all-turns=%d", costTerminal, costAllTurns)
	}
	// A budget whose 80% trigger sits strictly between the two costs: under
	// it, the terminal-only estimate stays below trigger and the all-turns
	// estimate crosses it.
	mid := (costTerminal + costAllTurns) / 2
	budget := mid * 5 / 4
	trigger := percentFloor(budget, 4, 5)
	if !(costTerminal < trigger && trigger <= costAllTurns) {
		t.Fatalf("fixture did not place trigger (%d) strictly between terminal (%d) and all-turns (%d) costs", trigger, costTerminal, costAllTurns)
	}

	terminalResult, err := Plan(PlanInput{Messages: messages, Budget: budget, ContextAccounting: terminalProfile})
	if err != nil {
		t.Fatal(err)
	}
	if terminalResult.Compacted {
		t.Fatalf("ReasoningBillingTerminalExchange must not trigger compaction: before=%d trigger=%d", terminalResult.BeforeTokens, terminalResult.TriggerTokens)
	}

	allTurnsResult, err := Plan(PlanInput{Messages: messages, Budget: budget, ContextAccounting: allTurnsProfile})
	if err != nil {
		t.Fatal(err)
	}
	if !allTurnsResult.Compacted {
		t.Fatalf("ReasoningBillingAllTurns must trigger compaction: before=%d trigger=%d", allTurnsResult.BeforeTokens, allTurnsResult.TriggerTokens)
	}
}

// TestPlanUnsetProfileDefaultsToConservativeAllTurns proves a PlanInput that
// leaves ContextAccounting unset (an unrecognized/generic provider) prices
// identically to an explicit ReasoningBillingAllTurns profile.
func TestPlanUnsetProfileDefaultsToConservativeAllTurns(t *testing.T) {
	messages := reasoningTerminalHistory(stringOfLen(3000), stringOfLen(50))
	budget, err := provider.EstimateRequestCost(messages, nil, 0, provider.ContextAccountingProfile{})
	if err != nil {
		t.Fatal(err)
	}
	unset, err := Plan(PlanInput{Messages: messages, Budget: budget * 2})
	if err != nil {
		t.Fatal(err)
	}
	explicit, err := Plan(PlanInput{Messages: messages, Budget: budget * 2, ContextAccounting: provider.ContextAccountingProfile{ReasoningBilling: provider.ReasoningBillingAllTurns}})
	if err != nil {
		t.Fatal(err)
	}
	if unset.BeforeTokens != explicit.BeforeTokens {
		t.Fatalf("unset ContextAccounting BeforeTokens=%d, want explicit all-turns BeforeTokens=%d", unset.BeforeTokens, explicit.BeforeTokens)
	}
}

// TestPlannerBeforeMatchesRequestEstimate is the shared-accounting pin: the
// planner's `before` (provider.EstimateMessagesPromptCost, uncalibrated) and
// the agent loop's calibration estimate (loop_request.go's
// provider.EstimatePromptCost) must price the exact same history/profile to
// the exact same number - a mismatch would mean calibration corrects the
// planner's trigger against a different notion of cost than the one that
// fired it.
func TestPlannerBeforeMatchesRequestEstimate(t *testing.T) {
	for _, profile := range []provider.ContextAccountingProfile{
		{ReasoningBilling: provider.ReasoningBillingAllTurns},
		{ReasoningBilling: provider.ReasoningBillingTerminalExchange},
		{ReasoningBilling: provider.ReasoningBillingNever},
	} {
		messages := reasoningTerminalHistory(stringOfLen(1200), stringOfLen(1200))
		plan, err := Plan(PlanInput{Messages: messages, Budget: 1 << 30, ContextAccounting: profile})
		if err != nil {
			t.Fatal(err)
		}
		requestEstimate, err := provider.EstimatePromptCost(messages, nil, profile)
		if err != nil {
			t.Fatal(err)
		}
		if plan.BeforeTokens != requestEstimate {
			t.Fatalf("profile %+v: planner before=%d, request-style estimate=%d", profile, plan.BeforeTokens, requestEstimate)
		}
	}
}

func stringOfLen(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'x'
	}
	return string(b)
}
