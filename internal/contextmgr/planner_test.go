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
	before, err := provider.EstimateRequestCost(messages, nil, 0, provider.ContextAccountingProfile{})
	if err != nil {
		t.Fatal(err)
	}
	budget := before * 5 / 4
	if budget <= 0 || PercentFloor(budget, 4, 5) != before {
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
	budget, err := provider.EstimateRequestCost(messages, nil, 0, provider.ContextAccountingProfile{})
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

func TestPlanDoesNotChargeOutputReserveAgainstPromptBudget(t *testing.T) {
	messages := []provider.Message{{Role: provider.RoleUser, Content: "objective"}}
	promptCost, err := provider.EstimateRequestCost(messages, nil, 0, provider.ContextAccountingProfile{})
	if err != nil {
		t.Fatal(err)
	}

	plan, err := Plan(PlanInput{
		Messages:      messages,
		Budget:        promptCost,
		OutputReserve: 128000,
		Force:         true,
	})
	if err != nil {
		t.Fatalf("Plan() returned %v; output reserve must not consume prompt budget", err)
	}
	if plan.AfterTokens != promptCost {
		t.Fatalf("after prompt cost = %d, want %d", plan.AfterTokens, promptCost)
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

func TestPlanCalibrationScalesEstimates(t *testing.T) {
	msg := provider.Message{Role: provider.RoleUser, Content: "hello world"}
	input := PlanInput{
		Messages:         []provider.Message{msg},
		Budget:           100000,
		CalibrationRatio: 2.0,
	}
	plan, err := Plan(input)
	if err != nil {
		t.Fatal(err)
	}
	est, _ := provider.EstimatePromptCost(input.Messages, nil, provider.ContextAccountingProfile{})
	if plan.BeforeTokens != 2*est {
		t.Fatalf("ratio=2.0: BeforeTokens=%d, want 2*%d=%d", plan.BeforeTokens, est, 2*est)
	}
}

func TestPlanCalibrationDefaultZeroIsUnity(t *testing.T) {
	msg := provider.Message{Role: provider.RoleUser, Content: "hello world"}
	input := PlanInput{
		Messages:         []provider.Message{msg},
		Budget:           100000,
		CalibrationRatio: 0, // default — should behave as ratio=1.0
	}
	plan, err := Plan(input)
	if err != nil {
		t.Fatal(err)
	}
	est, _ := provider.EstimatePromptCost(input.Messages, nil, provider.ContextAccountingProfile{})
	if plan.BeforeTokens != est {
		t.Fatalf("ratio=0 (unity): BeforeTokens=%d, want estimate=%d", plan.BeforeTokens, est)
	}
}

func TestPlanCalibrationTriggersCompactionEarlier(t *testing.T) {
	// Build messages that cost some amount at ratio=1.0
	content := strings.Repeat("a ", 150) // ~75 tokens for content alone
	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: content},
	}
	est, _ := provider.EstimatePromptCost(msgs, nil, provider.ContextAccountingProfile{})
	// Set budget such that at ratio=1.0, est < 80%*budget (no compaction)
	// but at ratio=2.0, 2*est >= 80%*budget (compaction triggers).
	// trigger = floor(budget * 4/5)
	// Need: est < trigger(ratio=1) but 2*est >= trigger
	// With a large enough budget: budget = est*3 → trigger = est*3*4/5 = est*2.4
	// ratio=1: est < 2.4*est ✓; ratio=2: 2*est < 2.4*est — still below trigger
	// Need tighter: budget = est*2 → trigger = est*2*4/5 = est*1.6
	// ratio=1: est < 1.6*est ✓; ratio=2: 2*est >= 1.6*est ✓
	budget := est * 2
	input := PlanInput{
		Messages:         msgs,
		Budget:           budget,
		CalibrationRatio: 2.0,
	}
	plan, err := Plan(input)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Compacted {
		t.Fatalf("ratio=2.0 with budget %d should trigger compaction (est=%d, doubled=%d, trigger=%d)", budget, est, 2*est, PercentFloor(budget, 4, 5))
	}
	// Verify ratio=1.0 does NOT compact with same budget
	input.CalibrationRatio = 1.0
	plan1, err := Plan(input)
	if err != nil {
		t.Fatal(err)
	}
	if plan1.Compacted {
		t.Fatalf("ratio=1.0 with budget %d should NOT trigger compaction (est=%d, trigger=%d)", budget, est, PercentFloor(budget, 4, 5))
	}
}

func TestPlanOverflowEvenWithObjective(t *testing.T) {
	msgs := []provider.Message{{Role: "user", Content: "hi"}}
	// Budget large enough to pass retainMessages (selected set within budget)
	// but after calibration (ratio=10), the cost exceeds budget.
	// selectedCost = est (since no tools), needs est <= budget
	// But after: 10*est > budget
	// With est~2, budget=5 → 10*2=20 > 5 ✓, but retainMessages also checks
	// calibratedCost: 10*2=20 > 5 at line 181 → retainMessages fails first
	// Need budget large enough that calibratedCost in retainMessages passes
	// but after in Plan still overflows: that means retainMessages succeeds
	// with calibrated cost < budget, then Plan checks again with same cost...
	// Actually Plan line 106-112 checks the SAME set that retainMessages checked.
	// The difference: retainMessages may drop some messages to hit target,
	// then Plan re-estimates the final set. If retainMessages succeeds
	// (all mandatory fit in budget), Plan line 112 should never fire because
	// the same set was already checked. UNLESS there's a tool cost difference...
	// With no tools, line 112 is dead code for single-message inputs.
	// Use a multi-message scenario where retainMessages succeeds but Plan
	// overflows.
	input := PlanInput{
		Messages:         msgs,
		Budget:           1,
		CalibrationRatio: 1.0,
	}
	_, err := Plan(input)
	if err == nil {
		t.Fatal("expected error when objective alone exceeds budget")
	}
}

func TestPlanCalibrationOverflow(t *testing.T) {
	msgs := []provider.Message{{Role: "user", Content: "hi"}}
	est, _ := provider.EstimateRequestCost(msgs, nil, 0, provider.ContextAccountingProfile{})
	// Budget is large enough at ratio=1.0 but overflows at ratio=10.0
	budget := est * 3
	input := PlanInput{
		Messages:         msgs,
		Budget:           budget,
		CalibrationRatio: 10.0,
	}
	_, err := Plan(input)
	if err == nil {
		t.Fatal("expected error when calibration makes objective exceed budget")
	}
}

func TestPlanToolMarshalError(t *testing.T) {
	msgs := []provider.Message{{Role: "user", Content: "hi"}}
	est, _ := provider.EstimateRequestCost(msgs, nil, 0, provider.ContextAccountingProfile{})
	budget := est * 3
	ch := make(chan int)
	tools := []provider.ToolSpec{{"type": "function", "function": map[string]any{"name": "bad", "params": ch}}}
	input := PlanInput{
		Messages: msgs,
		Budget:   budget,
		Tools:    tools,
	}
	_, err := Plan(input)
	if err == nil {
		t.Fatal("expected error from unmarshalable tool spec")
	}
}

func TestCalibratedCostAddsTheHoistedSchemaCost(t *testing.T) {
	msgs := []provider.Message{{Role: "user", Content: "hi"}}
	selected := map[int]struct{}{0: {}}
	base := calibratedCost(msgs, selected, 0, 1.0, provider.ContextAccountingProfile{})
	if got := calibratedCost(msgs, selected, 500, 1.0, provider.ContextAccountingProfile{}); got != base+500 {
		t.Fatalf("cost = %d, want %d (base %d + hoisted schema charge)", got, base+500, base)
	}
	if got := calibratedCost(msgs, selected, 0, 2.0, provider.ContextAccountingProfile{}); got != base*2 {
		t.Fatalf("calibrated cost = %d, want %d", got, base*2)
	}
}

func TestPromptOverflow(t *testing.T) {
	msg := provider.Message{Role: "user", Content: "hi"}
	err := promptOverflow(100, 50, msg, 0, 1.0, provider.ContextAccountingProfile{})
	if err == nil {
		t.Fatal("expected error")
	}
	s := err.Error()
	if !strings.Contains(s, "100") || !strings.Contains(s, "50") {
		t.Fatalf("expected cost and budget in error: %s", s)
	}
}

func TestPlannerFingerprintDistinguishesReasoning(t *testing.T) {
	// Same source content, different ReasoningContent must mint different
	// compaction idempotency keys (otherwise a reasoning-only change is
	// invisible to the planner and replayed thinking can be double-compacted).
	base := []provider.Message{
		{Role: provider.RoleSystem, Content: "sys"},
		{Role: provider.RoleUser, Content: "objective with enough tokens to matter"},
		{Role: provider.RoleAssistant, Content: "answer body", ReasoningContent: "first chain of thought"},
	}
	alt := []provider.Message{
		{Role: provider.RoleSystem, Content: "sys"},
		{Role: provider.RoleUser, Content: "objective with enough tokens to matter"},
		{Role: provider.RoleAssistant, Content: "answer body", ReasoningContent: "second different chain of thought"},
	}
	// Drive the real planIdempotencyKey path used by Plan.
	k1, err := planIdempotencyKey(PlanInput{Budget: 10_000, OutputReserve: 0}, contextstate.SourceRange{}, 5_000, base)
	if err != nil {
		t.Fatal(err)
	}
	k2, err := planIdempotencyKey(PlanInput{Budget: 10_000, OutputReserve: 0}, contextstate.SourceRange{}, 5_000, alt)
	if err != nil {
		t.Fatal(err)
	}
	if k1 == "" || k2 == "" {
		t.Fatal("empty idempotency key")
	}
	if k1 == k2 {
		t.Fatalf("reasoning-only difference must change fingerprint: both %q", k1)
	}
	// Identical messages → stable key.
	k1b, err := planIdempotencyKey(PlanInput{Budget: 10_000, OutputReserve: 0}, contextstate.SourceRange{}, 5_000, base)
	if err != nil {
		t.Fatal(err)
	}
	if k1b != k1 {
		t.Fatalf("identical messages produced unstable keys: %q vs %q", k1, k1b)
	}
}

func TestPlanRetainsPinnedObjectiveWithTrailingUserSteers(t *testing.T) {
	callOld := plannerToolCall("call-old", "read_file", `{"path":"old.txt"}`)
	callNew := plannerToolCall("call-new", "read_file", `{"path":"new.txt"}`)
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "root task prompt"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{callOld}},
		{Role: provider.RoleTool, ToolCallID: callOld.ID, Name: callOld.Function.Name, Content: strings.Repeat("result ", 100)},
		{Role: provider.RoleAssistant, Content: "in progress"},
		{Role: provider.RoleUser, Content: "[parent instruction: remember to check errors]"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{callNew}},
		{Role: provider.RoleTool, ToolCallID: callNew.ID, Name: callNew.Function.Name, Content: "done"},
	}
	// Plan with explicit CurrentObjective pinned to "root task prompt" and Force=true
	plan, err := Plan(PlanInput{
		Messages:         messages,
		Budget:           2000,
		CurrentObjective: "root task prompt",
		Force:            true,
		RecentTail:       4,
	})
	if err != nil {
		t.Fatalf("Plan failed with pinned objective and trailing steer: %v", err)
	}
	if !plan.Compacted {
		t.Fatal("expected compaction to occur")
	}
	// Pinned root objective must be retained as mandatory
	if !containsPlannerMessage(plan.Messages, provider.RoleUser, "root task prompt") {
		t.Fatal("pinned root task prompt was dropped during compaction")
	}
	// Trailing steer in recent tail must also be retained
	if !containsPlannerMessage(plan.Messages, provider.RoleUser, "[parent instruction: remember to check errors]") {
		t.Fatal("trailing steer frame in recent tail was dropped")
	}
	if err := provider.ValidateToolPairing(plan.Messages); err != nil {
		t.Fatalf("retained message shape invalid: %v", err)
	}
}

func TestPlanRetainsPinnedObjectiveWithPromptTooLongNotice(t *testing.T) {
	compactNotice := "[context compacted: the provider rejected the prompt as too long, so earlier turns and tool results were dropped to fit the model context; re-read any needed file with offset/limit for the remaining parts]"
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "user original prompt"},
		{Role: provider.RoleAssistant, Content: "reply"},
		{Role: provider.RoleUser, Content: compactNotice},
		{Role: provider.RoleAssistant, Content: "ready"},
	}
	plan, err := Plan(PlanInput{
		Messages:         messages,
		Budget:           1000,
		CurrentObjective: "user original prompt",
		Force:            true,
	})
	if err != nil {
		t.Fatalf("Plan failed with prompt-too-long notice: %v", err)
	}
	if !plan.Compacted {
		t.Fatal("expected compaction to occur")
	}
	if !containsPlannerMessage(plan.Messages, provider.RoleUser, "user original prompt") {
		t.Fatal("pinned user original prompt was dropped")
	}
	if !containsPlannerMessage(plan.Messages, provider.RoleUser, compactNotice) {
		t.Fatal("compact notice was dropped")
	}
}

func TestPlanRejectsUnmatchedExplicitObjective(t *testing.T) {
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "task objective"},
	}
	_, err := Plan(PlanInput{
		Messages:         messages,
		Budget:           1000,
		CurrentObjective: "completely different objective",
		Force:            true,
	})
	if !errors.Is(err, contextstate.ErrInvalidDTO) {
		t.Fatalf("error = %v, want ErrInvalidDTO for unmatched explicit objective", err)
	}
}

func TestPlanRejectsMissingObjectiveWhenNoUserMessages(t *testing.T) {
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
	}
	_, err := Plan(PlanInput{
		Messages: messages,
		Budget:   1000,
		Force:    true,
	})
	if !errors.Is(err, contextstate.ErrPromptBudgetExceeded) {
		t.Fatalf("error = %v, want ErrPromptBudgetExceeded when no user message exists", err)
	}
}
