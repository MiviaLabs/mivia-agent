package contextmgr

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// fuzzPlanHistories are the seeded histories for FuzzPlanInvariants. All of
// them pass provider.ValidateToolPairing so the fuzzer exercises retention,
// elision, and compaction logic rather than shape rejection.
func fuzzPlanHistories() [][]provider.Message {
	callOld := plannerToolCall("call-old", "read_file", `{"path":"old.txt"}`)
	callNew := plannerToolCall("call-new", "read_file", `{"path":"new.txt"}`)
	return [][]provider.Message{
		{
			{Role: provider.RoleSystem, Content: "system"},
			{Role: provider.RoleUser, Content: "current objective"},
			{Role: provider.RoleAssistant, Content: "older answer"},
		},
		{
			{Role: provider.RoleSystem, Content: "system"},
			{Role: provider.RoleUser, Content: "old objective"},
			{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{callOld}},
			{Role: provider.RoleTool, ToolCallID: callOld.ID, Name: callOld.Function.Name, Content: "result"},
			{Role: provider.RoleAssistant, Content: "done"},
			{Role: provider.RoleUser, Content: "current objective"},
			{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{callNew}},
			{Role: provider.RoleTool, ToolCallID: callNew.ID, Name: callNew.Function.Name, Content: "small"},
		},
		{
			{Role: provider.RoleSystem, Content: "system"},
			{Role: provider.RoleUser, Content: "old objective"},
			{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{callOld}},
			{Role: provider.RoleTool, ToolCallID: callOld.ID, Name: callOld.Function.Name, Content: strings.Repeat("x", 3000)},
			{Role: provider.RoleAssistant, Content: "done"},
			{Role: provider.RoleUser, Content: "current objective"},
			{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{callNew}},
			{Role: provider.RoleTool, ToolCallID: callNew.ID, Name: callNew.Function.Name, Content: "small"},
		},
	}
}

// FuzzPlanInvariants asserts the planner's documented postconditions on every
// input: no panic, and on success the retained set stays tool-paired, stays
// within the budget, never grows the prompt, and keeps the latest user
// objective. Errors are allowed; they are how the planner refuses.
func FuzzPlanInvariants(f *testing.F) {
	histories := fuzzPlanHistories()
	f.Add(int64(0), int64(65536), int64(0), true)
	f.Add(int64(1), int64(65536), int64(64), false)
	f.Add(int64(2), int64(65536), int64(64), true)
	f.Fuzz(func(t *testing.T, historyBytes, budgetBytes, tailBytes int64, force bool) {
		mod := func(value, base int64) int { return int(((value % base) + base) % base) }
		index := mod(historyBytes, int64(len(histories)))
		budget := mod(budgetBytes, 65536) + 1
		// Map 0..66 onto the RecentTail domain: 0 is the default-8 marker,
		// 1..64 are in-range, 65 is over the cap, 66 is the negative case.
		tail := mod(tailBytes, 67)
		if tail == 66 {
			tail = -1
		}
		plan, err := Plan(PlanInput{
			Messages:   histories[index],
			Budget:     budget,
			RecentTail: tail,
			Force:      force,
		})
		if err != nil {
			return
		}
		if err := provider.ValidateToolPairing(plan.Messages); err != nil {
			t.Fatalf("retained messages broke tool pairing: %v", err)
		}
		if plan.AfterTokens > plan.BeforeTokens {
			t.Fatalf("AfterTokens %d > BeforeTokens %d", plan.AfterTokens, plan.BeforeTokens)
		}
		if plan.AfterTokens > budget {
			t.Fatalf("AfterTokens %d exceeds budget %d", plan.AfterTokens, budget)
		}
		if !containsPlannerMessage(plan.Messages, provider.RoleUser, "current objective") {
			t.Fatal("current objective was not retained")
		}
	})
}
