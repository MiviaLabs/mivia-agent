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
// tailProbeCall is the newest exchange's tool call, shared by the fixture and
// its assertions.
var tailProbeCall = plannerToolCall("call-new", "read_file", `{"path":"new.txt"}`)

// tailFixture builds: system; "old objective"; the "old note" hole-punch
// probe; an old assistant + results exchange of `results` tool calls (a
// `results+1`-message unit); an optional "done"; "current objective"; the
// latest call-new exchange. All contents sit below the 2048-byte elision
// floor, so the exchange unit reaches retention unmodified.
//
// "old note" is deliberately an ASSISTANT message. DC-6 is about never
// presenting an answer without the tool results that produced it - about
// DERIVED content. The probe used to be a user message, which made this test
// accidentally assert that user turns are droppable; salvageUserMessages now
// re-admits those precisely because a user turn is the premise rather than
// derived content. The contiguity contract is unchanged for everything the
// rule was actually written to protect.
func tailFixture(results int, done bool) []provider.Message {
	oldCalls := make([]provider.ToolCall, results)
	for i := range oldCalls {
		oldCalls[i] = plannerToolCall(fmt.Sprintf("call-old-%d", i), "read_file", `{"path":"old.txt"}`)
	}
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "old objective"},
		{Role: provider.RoleAssistant, Content: "old note"},
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
		provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{tailProbeCall}},
		provider.Message{Role: provider.RoleTool, ToolCallID: tailProbeCall.ID, Name: tailProbeCall.Function.Name, Content: "small"},
	)
}

func TestRetainTailDropsOlderMessagesPastOversizedUnit(t *testing.T) {
	callNew := tailProbeCall
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
		if containsPlannerMessage(plan.Messages, provider.RoleAssistant, "old note") {
			t.Fatal("older derived message retained past an oversized recent unit: the retained optional tail must be a contiguous suffix of the newest messages")
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
	free, _, err := retainMessages(input, messages[1], 1, 1_000_000, 0, nil)
	if err != nil {
		t.Fatalf("retainMessages(schemaCost=0): %v", err)
	}
	// A schema charge larger than the whole budget must make the mandatory
	// selection overflow, which is the only way the charge can be observed
	// from outside.
	if _, _, err := retainMessages(input, messages[1], 1, 1_000_000, 2_000_000, nil); !errors.Is(err, contextstate.ErrPromptBudgetExceeded) {
		t.Fatalf("error = %v, want ErrPromptBudgetExceeded once the schema charge exceeds the budget", err)
	}
	if len(free) == 0 {
		t.Fatal("retention kept nothing with an unconstrained budget")
	}
}

// salvageProductionShape rebuilds the message shape from a real session that
// lost its task: a system prompt, an early user turn stating the actual work,
// a long run of bulky tool exchanges, and a bare "continue" as the newest
// user message after a resume. The objective anchor resolves to "continue",
// so before the salvage pass every earlier user turn was optional and fell
// off the RecentTail=8 message suffix while tool noise survived.
func salvageProductionShape() []provider.Message {
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "REAL TASK: fix the transcript viewport follow logic"},
		{Role: provider.RoleUser, Content: "SECOND TASK: and check the missed-count badge"},
	}
	for i := 0; i < 4; i++ {
		call := plannerToolCall(fmt.Sprintf("call-%d", i), "read_file", `{"path":"x.go"}`)
		messages = append(messages,
			provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call}},
			provider.Message{Role: provider.RoleTool, Name: "read_file", ToolCallID: call.ID, Content: fmt.Sprintf("result %d", i)},
		)
	}
	final := plannerToolCall("call-final", "grep", `{"q":"follow"}`)
	return append(messages,
		provider.Message{Role: provider.RoleUser, Content: "continue"},
		provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{final}},
		provider.Message{Role: provider.RoleTool, Name: "grep", ToolCallID: final.ID, Content: "no matches"},
	)
}

// TestRetainSalvagesOlderUserTurnsWithinBudget pins the fix for a confirmed
// production failure: after compaction the retained context held zero original
// user messages, so the agent told the user it had no task. The objective
// anchor is the NEWEST user message - after a resume that is a bare
// "continue" - and the recent-tail walk is a contiguous suffix capped by a
// MESSAGE COUNT, so it spent its slots on tool noise ("no matches", spent
// elision placeholders) while the task statement fell off the end. Crucially
// the token target was nowhere near exhausted: the message cap alone
// destroyed the task. Older user turns are now salvaged within the same token
// target the tail walk already respects.
func TestRetainSalvagesOlderUserTurnsWithinBudget(t *testing.T) {
	messages := salvageProductionShape()
	cost, err := provider.EstimateRequestCost(messages, nil, 0, provider.ContextAccountingProfile{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Plan(PlanInput{Messages: messages, Budget: cost * 4, Force: true})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !plan.Compacted {
		t.Fatal("fixture did not compact")
	}
	if !containsPlannerMessage(plan.Messages, provider.RoleUser, "REAL TASK: fix the transcript viewport follow logic") {
		t.Fatalf("the task statement was dropped; retained %d messages:\n%s", len(plan.Messages), formatSalvagePlan(plan.Messages))
	}
	if err := provider.ValidateToolPairing(plan.Messages); err != nil {
		t.Fatalf("salvage broke tool pairing: %v", err)
	}
	if plan.AfterTokens > plan.TargetTokens {
		t.Fatalf("AfterTokens %d exceeds TargetTokens %d", plan.AfterTokens, plan.TargetTokens)
	}
}

// TestSalvageIsBoundedAndNeverOverflows: salvage must respect the same token
// target the tail walk does and must never turn a valid plan into a budget
// overflow, however many old user turns exist.
func TestSalvageIsBoundedAndNeverOverflows(t *testing.T) {
	messages := []provider.Message{{Role: provider.RoleSystem, Content: "system"}}
	for i := 0; i < 40; i++ {
		messages = append(messages, provider.Message{Role: provider.RoleUser, Content: fmt.Sprintf("old user turn %d %s", i, string(make([]byte, 200)))})
	}
	messages = append(messages, provider.Message{Role: provider.RoleUser, Content: "continue"})
	cost, err := provider.EstimateRequestCost(messages, nil, 0, provider.ContextAccountingProfile{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Plan(PlanInput{Messages: messages, Budget: cost / 3, Force: true})
	if err != nil {
		t.Fatalf("Plan must not overflow on a user-heavy history: %v", err)
	}
	if plan.AfterTokens > plan.TargetTokens {
		t.Fatalf("AfterTokens %d exceeds TargetTokens %d", plan.AfterTokens, plan.TargetTokens)
	}
	// The retained user turns come from three bounded sources: the objective
	// anchor ("continue"), the recent-tail walk, and the salvage pass
	// (salvageUserTurns newest plus one reserved slot for the oldest). The
	// token target usually binds first; this is the structural ceiling that
	// must hold even when it does not.
	retainedUsers := 0
	for _, m := range plan.Messages {
		if m.Role == provider.RoleUser {
			retainedUsers++
		}
	}
	ceiling := 1 + defaultRecentTailMessages + salvageUserTurns + 1
	if retainedUsers > ceiling {
		t.Fatalf("retained %d user turns, want at most %d", retainedUsers, ceiling)
	}
}

func formatSalvagePlan(messages []provider.Message) string {
	out := ""
	for i, m := range messages {
		content := m.Content
		if len(content) > 50 {
			content = content[:50]
		}
		out += fmt.Sprintf("  [%d] %s %q\n", i, m.Role, content)
	}
	return out
}

// TestRetainReflessPlaceholdersDoNotConsumeTailSlots pins the fix for
// spent elision placeholders consuming retention slots: two 62-byte ref-less
// placeholders held 2 of 8 slots in production failures, dropping substantive
// earlier exchanges while carrying zero recoverable information. Ref-less
// placeholders must not be charged against the RecentTail budget.
func TestRetainReflessPlaceholdersDoNotConsumeTailSlots(t *testing.T) {
	// 1 system + 1 task user turn (salvaged)
	// Substantive exchange 0 (assistant call-0 + tool result-0) -> 2 slots
	// Placeholder exchange 1 (assistant call-1 + ref-less tool placeholder) -> 1 slot
	// Placeholder exchange 2 (assistant call-2 + ref-less tool placeholder) -> 1 slot
	// Substantive exchange 3 (assistant call-3 + tool result-3) -> 2 slots
	// Substantive exchange 4 (assistant call-4 + tool result-4) -> 2 slots
	// User: "continue" (current objective) -> mandatory
	// Substantive exchange 5 (assistant call-5 + tool result-5) -> mandatory latest tool unit
	//
	// Total optional slots with fix: 2 + 1 + 1 + 2 + 2 = 8 slots (fits exactly in RecentTail=8).
	// Without fix: 2 + 2 + 2 + 2 + 2 = 10 slots (exchange 0 would be dropped).
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "ORIGINAL TASK"},
	}

	call0 := plannerToolCall("call-0", "read_file", `{"path":"0.txt"}`)
	messages = append(messages,
		provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call0}},
		provider.Message{Role: provider.RoleTool, ToolCallID: call0.ID, Name: "read_file", Content: "substantive result 0"},
	)

	call1 := plannerToolCall("call-1", "read_file", `{"path":"1.txt"}`)
	messages = append(messages,
		provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call1}},
		provider.Message{Role: provider.RoleTool, ToolCallID: call1.ID, Name: "read_file", Content: elisionNotice(4096)},
	)

	call2 := plannerToolCall("call-2", "read_file", `{"path":"2.txt"}`)
	messages = append(messages,
		provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call2}},
		provider.Message{Role: provider.RoleTool, ToolCallID: call2.ID, Name: "read_file", Content: elisionNotice(4096)},
	)

	call3 := plannerToolCall("call-3", "read_file", `{"path":"3.txt"}`)
	messages = append(messages,
		provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call3}},
		provider.Message{Role: provider.RoleTool, ToolCallID: call3.ID, Name: "read_file", Content: "substantive result 3"},
	)

	call4 := plannerToolCall("call-4", "read_file", `{"path":"4.txt"}`)
	messages = append(messages,
		provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call4}},
		provider.Message{Role: provider.RoleTool, ToolCallID: call4.ID, Name: "read_file", Content: "substantive result 4"},
	)

	call5 := plannerToolCall("call-5", "read_file", `{"path":"5.txt"}`)
	messages = append(messages,
		provider.Message{Role: provider.RoleUser, Content: "continue"},
		provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call5}},
		provider.Message{Role: provider.RoleTool, ToolCallID: call5.ID, Name: "read_file", Content: "substantive result 5"},
	)

	cost, err := provider.EstimateRequestCost(messages, nil, 0, provider.ContextAccountingProfile{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Plan(PlanInput{Messages: messages, Budget: cost * 4, Force: true, RecentTail: 8})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !plan.Compacted {
		t.Fatal("plan did not compact")
	}
	if !containsPlannerMessage(plan.Messages, provider.RoleTool, "substantive result 0") {
		t.Fatalf("substantive exchange 0 was dropped because ref-less placeholders consumed tail slots; retained:\n%s", formatSalvagePlan(plan.Messages))
	}
	if err := provider.ValidateToolPairing(plan.Messages); err != nil {
		t.Fatalf("tool pairing violated: %v", err)
	}
}

// TestRetainRefBearingPlaceholdersConsumeTailSlots pins that elision notices
// carrying recoverable remainder references DO charge against the RecentTail
// budget, because the model can recover their full content via read_output.
func TestRetainRefBearingPlaceholdersConsumeTailSlots(t *testing.T) {
	ref1 := "ref:output:0000000000000000000000000000000000000000000000000000000000000001"
	ref2 := "ref:output:0000000000000000000000000000000000000000000000000000000000000002"

	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "ORIGINAL TASK"},
	}

	call0 := plannerToolCall("call-0", "read_file", `{"path":"0.txt"}`)
	messages = append(messages,
		provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call0}},
		provider.Message{Role: provider.RoleTool, ToolCallID: call0.ID, Name: "read_file", Content: "substantive result 0"},
	)

	call1 := plannerToolCall("call-1", "read_file", `{"path":"1.txt"}`)
	messages = append(messages,
		provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call1}},
		provider.Message{Role: provider.RoleTool, ToolCallID: call1.ID, Name: "read_file", Content: elisionNoticeWithRef(4096, ref1)},
	)

	call2 := plannerToolCall("call-2", "read_file", `{"path":"2.txt"}`)
	messages = append(messages,
		provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call2}},
		provider.Message{Role: provider.RoleTool, ToolCallID: call2.ID, Name: "read_file", Content: elisionNoticeWithRef(4096, ref2)},
	)

	call3 := plannerToolCall("call-3", "read_file", `{"path":"3.txt"}`)
	messages = append(messages,
		provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call3}},
		provider.Message{Role: provider.RoleTool, ToolCallID: call3.ID, Name: "read_file", Content: "substantive result 3"},
	)

	call4 := plannerToolCall("call-4", "read_file", `{"path":"4.txt"}`)
	messages = append(messages,
		provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call4}},
		provider.Message{Role: provider.RoleTool, ToolCallID: call4.ID, Name: "read_file", Content: "substantive result 4"},
	)

	call5 := plannerToolCall("call-5", "read_file", `{"path":"5.txt"}`)
	messages = append(messages,
		provider.Message{Role: provider.RoleUser, Content: "continue"},
		provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call5}},
		provider.Message{Role: provider.RoleTool, ToolCallID: call5.ID, Name: "read_file", Content: "substantive result 5"},
	)

	cost, err := provider.EstimateRequestCost(messages, nil, 0, provider.ContextAccountingProfile{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Plan(PlanInput{Messages: messages, Budget: cost * 4, Force: true, RecentTail: 8})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !plan.Compacted {
		t.Fatal("plan did not compact")
	}
	// With ref-bearing placeholders charging 1 slot each, slots for exchanges 1-4 = 2 + 2 + 2 + 2 = 8 slots.
	// Exchange 0 (which needs 2 more slots) must be dropped at RecentTail=8.
	if containsPlannerMessage(plan.Messages, provider.RoleTool, "substantive result 0") {
		t.Fatalf("substantive exchange 0 survived even though ref-bearing placeholders consumed tail slots; retained:\n%s", formatSalvagePlan(plan.Messages))
	}
	if err := provider.ValidateToolPairing(plan.Messages); err != nil {
		t.Fatalf("tool pairing violated: %v", err)
	}
}
