package contextmgr

import (
	"errors"
	"fmt"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func retentionMessages() []provider.Message {
	return []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "current objective"},
		{Role: provider.RoleAssistant, Content: "older answer"},
	}
}

func TestPlanRejectsAnOutOfRangeRecentTail(t *testing.T) {
	messages := retentionMessages()
	budget, err := provider.EstimateRequestCost(messages, nil, 0, provider.ContextAccountingProfile{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tail := range []int{-1, maxRecentTailMessages + 1} {
		_, err := Plan(PlanInput{Messages: messages, Budget: budget + 100, Force: true, RecentTail: tail})
		if !errors.Is(err, contextstate.ErrInvalidDTO) {
			t.Fatalf("recent_tail=%d error = %v, want ErrInvalidDTO", tail, err)
		}
	}
}

// TestRetainTailDropsOlderMessagesPastOversizedUnit pins the DC-6 defect in
// the recent-tail walk: retainMessages walked the tail newest-to-oldest and
// used `continue` when a unit exceeded the RecentTail cap, so a recent
// oversized tool-exchange unit was skipped and OLDER messages were then filled
// in. The retained optional tail therefore had a hole - an older message
// ("old objective") survived while the newer exchange between it and the
// current objective was dropped, so the compacted context could present an
// answer without the tool results that produced it. The cap must stop the walk
// (break), making the retained optional tail a contiguous suffix of the newest
// messages.
func TestRetainTailDropsOlderMessagesPastOversizedUnit(t *testing.T) {
	callNew := plannerToolCall("call-new", "read_file", `{"path":"new.txt"}`)
	// tailFixture builds: system; "old objective"; an old assistant + results
	// exchange of `results` tool calls (a `results+1`-message unit); an
	// optional "done"; "current objective"; the latest call-new exchange. All
	// contents sit below the 2048-byte elision floor, so the exchange unit
	// reaches retention unmodified.
	tailFixture := func(results int, done bool) []provider.Message {
		oldCalls := make([]provider.ToolCall, results)
		for i := range oldCalls {
			oldCalls[i] = plannerToolCall(fmt.Sprintf("call-old-%d", i), "read_file", `{"path":"old.txt"}`)
		}
		messages := []provider.Message{
			{Role: provider.RoleSystem, Content: "system"},
			{Role: provider.RoleUser, Content: "old objective"},
			{Role: provider.RoleAssistant, ToolCalls: oldCalls},
		}
		for _, call := range oldCalls {
			messages = append(messages, provider.Message{Role: provider.RoleTool, ToolCallID: call.ID, Name: call.Function.Name, Content: "result"})
		}
		if done {
			messages = append(messages, provider.Message{Role: provider.RoleAssistant, Content: "done"})
		}
		return append(messages,
			provider.Message{Role: provider.RoleUser, Content: "current objective"},
			provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{callNew}},
			provider.Message{Role: provider.RoleTool, ToolCallID: callNew.ID, Name: callNew.Function.Name, Content: "small"},
		)
	}
	assertRetained := func(t *testing.T, messages []provider.Message) *PlanResult {
		t.Helper()
		plan, err := Plan(PlanInput{Messages: messages, Budget: 10000, Force: true, RecentTail: 0})
		if err != nil {
			t.Fatal(err)
		}
		if !plan.Compacted {
			t.Fatal("forced plan was not compacted")
		}
		if err := provider.ValidateToolPairing(plan.Messages); err != nil {
			t.Fatalf("retained shape invalid: %v", err)
		}
		if !containsPlannerMessage(plan.Messages, provider.RoleUser, "current objective") {
			t.Fatal("current objective was not retained")
		}
		if !containsPlannerToolCall(plan.Messages, callNew.ID) || !containsPlannerToolResult(plan.Messages, callNew.ID) {
			t.Fatal("latest tool exchange was split or dropped")
		}
		if containsPlannerMessage(plan.Messages, provider.RoleUser, "old objective") {
			t.Fatal("older message retained past an oversized recent unit: the retained optional tail must be a contiguous suffix of the newest messages")
		}
		return &plan
	}
	t.Run("exchange larger than the cap", func(t *testing.T) {
		// A 10-message unit (assistant + 9 results) plus a one-message "done"
		// ahead of it: the walk fills "done" (tailCount=1), then the unit
		// overflows (1+10 > 8). The cap must stop the walk, not skip past the
		// boundary and then fill "old objective".
		messages := tailFixture(9, true)
		if !containsPlannerMessage(messages, provider.RoleAssistant, "done") {
			t.Fatal("fixture must contain the 'done' message")
		}
		plan := assertRetained(t, messages)
		if !containsPlannerMessage(plan.Messages, provider.RoleAssistant, "done") {
			t.Fatal("'done' was not retained")
		}
	})
	t.Run("exchange exactly at the cap", func(t *testing.T) {
		// An 8-message unit (assistant + 7 results) exactly fills the default
		// cap, so "old objective" cannot fit after it (8+1 > 8) and is dropped
		// before and after the fix.
		assertRetained(t, tailFixture(7, false))
	})
	t.Run("exchange one over the cap", func(t *testing.T) {
		// A 9-message unit (assistant + 8 results) is one over the cap and is
		// dropped with NO older fill: "old objective" must not be retained.
		assertRetained(t, tailFixture(8, false))
	})
}

func TestRetainMessagesChargesTheHoistedSchemaCost(t *testing.T) {
	// The tool schemas are priced once by Plan and handed down, so retention
	// must charge that number rather than silently costing messages only.
	messages := retentionMessages()
	input := PlanInput{Messages: messages, Budget: 1_000_000}
	free, err := retainMessages(input, messages[1], 1, 1_000_000, 0)
	if err != nil {
		t.Fatalf("retainMessages(schemaCost=0): %v", err)
	}
	// A schema charge larger than the whole budget must make the mandatory
	// selection overflow, which is the only way the charge can be observed
	// from outside.
	if _, err := retainMessages(input, messages[1], 1, 1_000_000, 2_000_000); !errors.Is(err, contextstate.ErrPromptBudgetExceeded) {
		t.Fatalf("error = %v, want ErrPromptBudgetExceeded once the schema charge exceeds the budget", err)
	}
	if len(free) == 0 {
		t.Fatal("retention kept nothing with an unconstrained budget")
	}
}
