package contextmgr

import (
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
)

// elisionHistory builds a multi-turn transcript with an oversized prior tool
// result (call-old) before the latest user objective, plus a small current
// tool exchange that is mandatory.
func elisionHistory(priorToolContent, currentToolContent string) []provider.Message {
	oldCall := plannerToolCall("call-old", "read_file", `{"path":"old.txt"}`)
	newCall := plannerToolCall("call-new", "read_file", `{"path":"new.txt"}`)
	return []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "old objective"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{oldCall}},
		{Role: provider.RoleTool, ToolCallID: oldCall.ID, Name: oldCall.Function.Name, Content: priorToolContent},
		{Role: provider.RoleAssistant, Content: "finished prior turn"},
		{Role: provider.RoleUser, Content: "current objective"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{newCall}},
		{Role: provider.RoleTool, ToolCallID: newCall.ID, Name: newCall.Function.Name, Content: currentToolContent},
	}
}

func forceCompactBudget(t *testing.T, messages []provider.Message) int {
	t.Helper()
	cost, err := provider.EstimatePromptCost(messages, nil, provider.ContextAccountingProfile{})
	if err != nil {
		t.Fatal(err)
	}
	// Large enough that mandatory retention always succeeds; Force drives compaction.
	return cost + 10_000
}

func TestPlanElidesOversizedPriorToolResultWhenTriggered(t *testing.T) {
	big := strings.Repeat("x", 3000)
	messages := elisionHistory(big, "small current")
	budget := forceCompactBudget(t, messages)

	plan, err := Plan(PlanInput{Messages: messages, Budget: budget, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Compacted {
		t.Fatal("expected compaction")
	}
	if plan.ElidedMessages != 1 {
		t.Fatalf("ElidedMessages=%d, want 1", plan.ElidedMessages)
	}
	if plan.ElidedBytes != len(big) {
		t.Fatalf("ElidedBytes=%d, want %d", plan.ElidedBytes, len(big))
	}
	var found bool
	for _, msg := range plan.Messages {
		if msg.Role == provider.RoleTool && msg.ToolCallID == "call-old" {
			found = true
			if msg.Content == big {
				t.Fatal("prior oversized tool result was not elided")
			}
			if !strings.HasPrefix(msg.Content, "[context elided prior tool result;") {
				t.Fatalf("unexpected elision notice: %q", msg.Content)
			}
			if msg.Name != "read_file" {
				t.Fatalf("tool name changed: %q", msg.Name)
			}
		}
		if msg.Role == provider.RoleTool && msg.ToolCallID == "call-new" {
			if msg.Content != "small current" {
				t.Fatalf("current tool result changed: %q", msg.Content)
			}
		}
	}
	if !found {
		// Prior unit may be dropped by tail retention; still require counters.
		// Re-run with a large recent tail so the elided body is retained.
		t.Fatal("call-old missing from retained set; widen tail in fixture")
	}
	if err := provider.ValidateToolPairing(plan.Messages); err != nil {
		t.Fatalf("pairing: %v", err)
	}
}

func TestPlanDoesNotElideAtOrAfterObjective(t *testing.T) {
	// Oversized tool result belongs to the current objective turn.
	call := plannerToolCall("call-now", "read_file", `{}`)
	big := strings.Repeat("y", 3000)
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "current objective"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call}},
		{Role: provider.RoleTool, ToolCallID: call.ID, Name: call.Function.Name, Content: big},
	}
	budget := forceCompactBudget(t, messages)
	plan, err := Plan(PlanInput{Messages: messages, Budget: budget, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ElidedMessages != 0 || plan.ElidedBytes != 0 {
		t.Fatalf("elided same-turn tool result: %+v", plan)
	}
	for _, msg := range plan.Messages {
		if msg.Role == provider.RoleTool && msg.Content != big {
			t.Fatalf("same-turn body changed: %q", msg.Content)
		}
	}
}

func TestPlanBelowTriggerReturnsCloneWithZeroElision(t *testing.T) {
	big := strings.Repeat("x", 3000)
	messages := elisionHistory(big, "small")
	cost, err := provider.EstimatePromptCost(messages, nil, provider.ContextAccountingProfile{})
	if err != nil {
		t.Fatal(err)
	}
	// Budget high enough that trigger > before.
	budget := cost*5/4 + 1000
	if PercentFloor(budget, 4, 5) <= cost {
		budget = cost*2 + 1000
	}
	plan, err := Plan(PlanInput{Messages: messages, Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Compacted {
		t.Fatal("below-trigger plan compacted")
	}
	if plan.ElidedMessages != 0 || plan.ElidedBytes != 0 {
		t.Fatalf("elision below trigger: msgs=%d bytes=%d", plan.ElidedMessages, plan.ElidedBytes)
	}
	if len(plan.Messages) != len(messages) {
		t.Fatalf("message count %d != %d", len(plan.Messages), len(messages))
	}
	for i := range messages {
		if plan.Messages[i].Content != messages[i].Content {
			t.Fatalf("message %d content diverged under below-trigger plan", i)
		}
	}
}

func TestPlanDoesNotElideMandatorySystemObjectiveOrLatestToolUnit(t *testing.T) {
	// History ends at the prior tool exchange so the oversized result is the
	// latest tool unit (mandatory) even though it sits before a new objective
	// that is also the last user message after we append nothing else...
	// Structure: system, user(obj), assistant+huge tool — latest tool unit is mandatory.
	call := plannerToolCall("call-latest", "read_file", `{}`)
	big := strings.Repeat("z", 4000)
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "current objective"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call}},
		{Role: provider.RoleTool, ToolCallID: call.ID, Name: call.Function.Name, Content: big},
	}
	budget := forceCompactBudget(t, messages)
	plan, err := Plan(PlanInput{Messages: messages, Budget: budget, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ElidedMessages != 0 {
		t.Fatalf("mandatory latest tool unit was elided: %d", plan.ElidedMessages)
	}
	for _, msg := range plan.Messages {
		if msg.Role == provider.RoleTool && msg.Content != big {
			t.Fatalf("latest tool body changed: %q", msg.Content)
		}
		if msg.Role == provider.RoleSystem && msg.Content != "system" {
			t.Fatalf("system changed: %q", msg.Content)
		}
		if msg.Role == provider.RoleUser && msg.Content != "current objective" {
			t.Fatalf("objective changed: %q", msg.Content)
		}
	}
}

func TestElisionThreshold2048(t *testing.T) {
	// Direct helper: 2048 kept, 2049 elided when eligible.
	call := plannerToolCall("c1", "read_file", `{}`)
	at := strings.Repeat("a", 2048)
	over := strings.Repeat("b", 2049)
	// objectiveIndex = 3 so tool at index 2 is before objective.
	messagesAt := []provider.Message{
		{Role: provider.RoleUser, Content: "old"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call}},
		{Role: provider.RoleTool, ToolCallID: call.ID, Name: "read_file", Content: at},
		{Role: provider.RoleUser, Content: "new"},
	}
	// No mandatory tool unit after objective: latest tool unit is index 1-2.
	mandatory := mandatoryIndexes(messagesAt, 3, nil)
	out, stats := elideToolResults(cloneMessages(messagesAt), 3, mandatory)
	if stats.Messages != 0 || out[2].Content != at {
		t.Fatalf("2048-byte body elided: stats=%+v content_len=%d", stats, len(out[2].Content))
	}

	messagesOver := cloneMessages(messagesAt)
	messagesOver[2].Content = over
	// Insert a later tool unit after objective so the prior one is not mandatory.
	call2 := plannerToolCall("c2", "read_file", `{}`)
	messagesOver = append(messagesOver,
		provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call2}},
		provider.Message{Role: provider.RoleTool, ToolCallID: call2.ID, Name: "read_file", Content: "now"},
	)
	mandatory = mandatoryIndexes(messagesOver, 3, nil)
	out, stats = elideToolResults(cloneMessages(messagesOver), 3, mandatory)
	if stats.Messages != 1 || out[2].Content == over {
		t.Fatalf("2049-byte body not elided: stats=%+v content_len=%d", stats, len(out[2].Content))
	}
	if stats.Bytes != 2049 {
		t.Fatalf("ElidedBytes=%d, want 2049", stats.Bytes)
	}
}

func TestElisionBucketBoundariesAndEmptyContent(t *testing.T) {
	if got := sizeBucketLabel(0); got != "0 KiB" {
		t.Fatalf("sizeBucketLabel(0)=%q", got)
	}
	if got := sizeBucketLabel(3000); got != "4 KiB" {
		t.Fatalf("sizeBucketLabel(3000)=%q, want 4 KiB", got)
	}
	if got := sizeBucketLabel(1024*1024 + 1); got != "2 MiB" {
		t.Fatalf("sizeBucketLabel(1MiB+1)=%q, want 2 MiB", got)
	}
	if got := elisionNotice(3000); !strings.Contains(got, "4 KiB") {
		t.Fatalf("notice=%q", got)
	}
	// Empty tool content is never eligible.
	call := plannerToolCall("c", "t", `{}`)
	messages := []provider.Message{
		{Role: provider.RoleUser, Content: "old"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call}},
		{Role: provider.RoleTool, ToolCallID: "c", Name: "t", Content: ""},
		{Role: provider.RoleUser, Content: "new"},
	}
	mandatory := map[int]struct{}{3: {}}
	_, stats := elideToolResults(cloneMessages(messages), 3, mandatory)
	if stats.Messages != 0 {
		t.Fatalf("empty content elided: %+v", stats)
	}
}

func TestElisionSkipsWhenNoticeNotCheaper(t *testing.T) {
	big := strings.Repeat("x", 2049)
	call := plannerToolCall("c", "t", `{}`)
	call2 := plannerToolCall("c2", "t", `{}`)
	messages := []provider.Message{
		{Role: provider.RoleUser, Content: "old"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call}},
		{Role: provider.RoleTool, ToolCallID: "c", Name: "t", Content: big},
		{Role: provider.RoleUser, Content: "new"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call2}},
		{Role: provider.RoleTool, ToolCallID: "c2", Name: "t", Content: "ok"},
	}
	mandatory := mandatoryIndexes(messages, 3, nil)

	// Positive control: real estimator finds the notice cheaper.
	out, stats := elideToolResults(cloneMessages(messages), 3, mandatory)
	if stats.Messages != 1 {
		t.Fatalf("expected cheaper notice to win: %+v", stats)
	}
	if out[2].Content == big {
		t.Fatal("body not replaced under real estimator")
	}

	// Defensive path: notice must not win when its estimate is not lower.
	prev := messageTokenCost
	messageTokenCost = func(m provider.Message) int {
		if strings.Contains(m.Content, "context elided") {
			return 1_000_000
		}
		return 1
	}
	t.Cleanup(func() { messageTokenCost = prev })
	out, stats = elideToolResults(cloneMessages(messages), 3, mandatory)
	if stats.Messages != 0 || out[2].Content != big {
		t.Fatalf("notice replaced body when not cheaper: stats=%+v", stats)
	}
}

func TestPlanCompactMissingObjective(t *testing.T) {
	// Compaction path with no user message fails objective resolution.
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: "system only"},
		{Role: provider.RoleAssistant, Content: "no user"},
	}
	_, err := Plan(PlanInput{Messages: messages, Budget: 10_000, Force: true})
	if err == nil {
		t.Fatal("expected missing-objective error")
	}
}

func TestPlanCompactInvalidIdempotencyKey(t *testing.T) {
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "objective"},
	}
	_, err := Plan(PlanInput{
		Messages: messages, Budget: 10_000, Force: true,
		IdempotencyKey: "bad\nkey",
	})
	if !errors.Is(err, contextstate.ErrInvalidDTO) {
		t.Fatalf("error=%v, want ErrInvalidDTO", err)
	}
}

// intSize is 32 on 32-bit platforms and 64 on 64-bit platforms, evaluated at
// compile time with no import. It guards the 1<<31 boundary assertions, which
// are unrepresentable as a 32-bit int.
const intSize = 32 << (^uint(0) >> 63)

func TestSizeBucketAndCeilPowerEdges(t *testing.T) {
	if got := sizeBucketLabel(1); got != "1 KiB" {
		t.Fatalf("sizeBucketLabel(1)=%q", got)
	}
	if got := ceilPowerOfTwo(0); got != 1 {
		t.Fatalf("ceilPowerOfTwo(0)=%d", got)
	}
	if got := ceilPowerOfTwo(1); got != 1 {
		t.Fatalf("ceilPowerOfTwo(1)=%d", got)
	}
	// Saturation guard: the ceiling saturates at the largest representable
	// power of two, 1<<(intSize-2). 1<<31 is a representable power of two
	// only on 64-bit ints, so the >1<<30 cases are guarded by intSize.
	if intSize == 64 {
		if got := ceilPowerOfTwo(1<<30 + 1); int64(got) != int64(1)<<31 {
			t.Fatalf("ceilPowerOfTwo(1<<30+1)=%d, want %d", got, int64(1)<<31)
		}
		// Pin the observable notice: a body just over 1 GiB must read as
		// "about 2048 MiB", never the understated 1024 MiB.
		if got := sizeBucketLabel(1<<30 + 1); got != "2048 MiB" {
			t.Fatalf("sizeBucketLabel(1<<30+1)=%q, want 2048 MiB", got)
		}
	}
	// No-hang saturation at the type boundary: ceilPowerOfTwo(maxInt) must
	// equal the largest representable power of two and must terminate. The
	// unguarded doubling loop would wrap p negative (then to 0) and spin
	// forever.
	if got := ceilPowerOfTwo(int(^uint(0) >> 1)); got != 1<<(intSize-2) {
		t.Fatalf("ceilPowerOfTwo(maxInt)=%d, want %d", got, 1<<(intSize-2))
	}
}

func TestPlanCompactPropagatesEstimateAndMarshalErrors(t *testing.T) {
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "objective"},
	}
	prevEst, prevMar := estimateToolSchemaCost, marshalCanonical
	t.Cleanup(func() {
		estimateToolSchemaCost = prevEst
		marshalCanonical = prevMar
	})

	estimateToolSchemaCost = func([]provider.ToolSpec) (int, error) {
		return 0, errors.New("estimate boom")
	}
	_, err := Plan(PlanInput{Messages: messages, Budget: 10_000, Force: true})
	if err == nil || !strings.Contains(err.Error(), "estimate boom") {
		t.Fatalf("estimate error = %v", err)
	}

	estimateToolSchemaCost = prevEst
	marshalCanonical = func(any) ([]byte, error) {
		return nil, errors.New("marshal boom")
	}
	_, err = Plan(PlanInput{Messages: messages, Budget: 10_000, Force: true})
	if err == nil || !strings.Contains(err.Error(), "marshal boom") {
		t.Fatalf("marshal error = %v", err)
	}
}

func TestPlanElisionPreservesInputImmutability(t *testing.T) {
	big := strings.Repeat("x", 3000)
	messages := elisionHistory(big, "small")
	// Snapshot original tool body.
	original := messages[3].Content
	budget := forceCompactBudget(t, messages)
	if _, err := Plan(PlanInput{Messages: messages, Budget: budget, Force: true}); err != nil {
		t.Fatal(err)
	}
	if messages[3].Content != original {
		t.Fatal("Plan mutated PlanInput.Messages")
	}
}

func TestPlanElisionIdempotencyKeyChangesWithContent(t *testing.T) {
	big := strings.Repeat("x", 3000)
	messages := elisionHistory(big, "small")
	budget := forceCompactBudget(t, messages)
	// Force compact with elision.
	withElide, err := Plan(PlanInput{Messages: messages, Budget: budget, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	// Same structure but prior body already the notice — second plan should be
	// stable and differ from a non-elided retention of the big body.
	// Compare two forced plans of identical input: keys must match (deterministic).
	again, err := Plan(PlanInput{Messages: messages, Budget: budget, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if withElide.IdempotencyKey == "" || withElide.IdempotencyKey != again.IdempotencyKey {
		t.Fatalf("nondeterministic key: %q vs %q", withElide.IdempotencyKey, again.IdempotencyKey)
	}
	// History with small prior body (no elision) yields a different retained set key
	// when the prior unit is kept.
	small := elisionHistory("tiny prior", "small")
	noElide, err := Plan(PlanInput{Messages: small, Budget: forceCompactBudget(t, small), Force: true, RecentTail: 64})
	if err != nil {
		t.Fatal(err)
	}
	withElideWide, err := Plan(PlanInput{Messages: messages, Budget: budget, Force: true, RecentTail: 64})
	if err != nil {
		t.Fatal(err)
	}
	if noElide.IdempotencyKey == withElideWide.IdempotencyKey {
		t.Fatal("idempotency key ignored elided content change")
	}
}

func TestPlanElisionKeepsToolPairingAndAfterTokensMatchEstimate(t *testing.T) {
	big := strings.Repeat("x", 3000)
	messages := elisionHistory(big, "small")
	budget := forceCompactBudget(t, messages)
	plan, err := Plan(PlanInput{Messages: messages, Budget: budget, Force: true, RecentTail: 64})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.ValidateToolPairing(plan.Messages); err != nil {
		t.Fatal(err)
	}
	fresh, err := provider.EstimatePromptCost(plan.Messages, nil, provider.ContextAccountingProfile{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.AfterTokens != fresh {
		t.Fatalf("AfterTokens=%d, fresh estimate=%d", plan.AfterTokens, fresh)
	}
	if plan.AfterTokens > plan.BeforeTokens {
		t.Fatalf("AfterTokens %d > BeforeTokens %d", plan.AfterTokens, plan.BeforeTokens)
	}
}

func TestPlanMandatoryOverflowStillErrPromptBudgetExceeded(t *testing.T) {
	call := plannerToolCall("call-huge", "read_file", `{}`)
	// Mandatory latest tool unit with huge body — cannot elide (at/after objective
	// and is latest unit). Tiny budget must still fail hard.
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "current objective"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call}},
		{Role: provider.RoleTool, ToolCallID: call.ID, Name: call.Function.Name, Content: strings.Repeat("m", 50_000)},
	}
	_, err := Plan(PlanInput{Messages: messages, Budget: 50, Force: true})
	if !errors.Is(err, contextstate.ErrPromptBudgetExceeded) {
		t.Fatalf("error=%v, want ErrPromptBudgetExceeded", err)
	}
}

func TestPlanNoElisionKeepsEightMessageTailBehavior(t *testing.T) {
	// No oversized tools: retention shape matches default tail of 8.
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
	}
	for i := 0; i < 12; i++ {
		messages = append(messages,
			provider.Message{Role: provider.RoleUser, Content: "u" + strings.Repeat("x", i+1)},
			provider.Message{Role: provider.RoleAssistant, Content: "a" + strings.Repeat("y", i+1)},
		)
	}
	// Final user is objective.
	messages = append(messages, provider.Message{Role: provider.RoleUser, Content: "current objective"})
	budget := forceCompactBudget(t, messages)
	plan, err := Plan(PlanInput{Messages: messages, Budget: budget, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ElidedMessages != 0 {
		t.Fatalf("unexpected elision: %d", plan.ElidedMessages)
	}
	// System + objective are mandatory; optional tail adds at most 8 messages.
	// Total retained should be well under the full history.
	if len(plan.Messages) >= len(messages) {
		t.Fatalf("retention did not drop history: kept %d of %d", len(plan.Messages), len(messages))
	}
	if !containsPlannerMessage(plan.Messages, provider.RoleUser, "current objective") {
		t.Fatal("objective missing")
	}
}

func TestPlanElidedActiveContextOmitsOriginalBody(t *testing.T) {
	big := strings.Repeat("x", 3000)
	messages := elisionHistory(big, "small")
	budget := forceCompactBudget(t, messages)
	plan, err := Plan(PlanInput{Messages: messages, Budget: budget, Force: true, RecentTail: 64})
	if err != nil {
		t.Fatal(err)
	}
	active := string(plan.Candidate.ActiveContext)
	if strings.Contains(active, big) {
		t.Fatal("active context still contains original oversized tool body")
	}
	if !strings.Contains(active, "context elided prior tool result") {
		t.Fatalf("active context missing elision notice: %s", active)
	}
}

// TestPlanCompactRedactsCandidateReasoning pins the compacted-path candidate
// redaction (planCompact): candidate ActiveContext bytes are durable,
// operator-visible state, so reasoning must be scrubbed before they are
// marshaled, while plan.Messages stay raw for replay. Mirrors
// TestStructuralPrepareRedactsCandidateReasoning, which pins the non-compacted
// structural path.
func TestPlanCompactRedactsCandidateReasoning(t *testing.T) {
	policy, err := redact.Compile([]string{`(?i)secret-[0-9]+`}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	old := redact.Current()
	defer redact.SetPolicy(old)
	redact.SetPolicy(policy)

	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "question"},
		{Role: provider.RoleAssistant, Content: "answer", ReasoningContent: "secret-1234"},
	}
	plan, err := Plan(PlanInput{
		Messages:   messages,
		Budget:     forceCompactBudget(t, messages),
		Force:      true,
		RecentTail: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Compacted {
		t.Fatal("expected compaction")
	}
	// The reasoning-carrying assistant message must be in the retained set,
	// otherwise the leak assertions below are vacuous.
	var retainedReasoning bool
	for _, msg := range plan.Messages {
		if msg.Role == provider.RoleAssistant && msg.ReasoningContent == "secret-1234" {
			retainedReasoning = true
		}
	}
	if !retainedReasoning {
		t.Fatal("reasoning-carrying assistant message was not retained")
	}

	active := string(plan.Candidate.ActiveContext)
	if !strings.Contains(active, "[redacted]") {
		t.Fatalf("compacted candidate ActiveContext was not redacted: %s", active)
	}
	if strings.Contains(active, "secret-1234") {
		t.Fatalf("compacted candidate ActiveContext leaked the reasoning secret: %s", active)
	}
	// plan.Messages keeps raw reasoning for replay, so a raw marshal of the
	// retained set must differ from the redacted candidate bytes.
	raw, err := contextstate.MarshalCanonical(plan.Messages)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == string(plan.Candidate.ActiveContext) {
		t.Fatal("compacted candidate ActiveContext equals a raw marshal; reasoning was not redacted")
	}
}

func TestPlanExactTriggerCanElide(t *testing.T) {
	big := strings.Repeat("x", 3000)
	messages := elisionHistory(big, "small")
	before, err := provider.EstimatePromptCost(messages, nil, provider.ContextAccountingProfile{})
	if err != nil {
		t.Fatal(err)
	}
	// budget such that trigger == before: trigger = floor(4/5 * budget) == before
	// => budget = ceil(before * 5/4)
	budget := before * 5 / 4
	for PercentFloor(budget, 4, 5) < before {
		budget++
	}
	for PercentFloor(budget, 4, 5) > before {
		budget--
	}
	if PercentFloor(budget, 4, 5) != before {
		t.Skipf("cannot construct exact trigger for before=%d", before)
	}
	// Mandatory set must fit in budget.
	plan, err := Plan(PlanInput{Messages: messages, Budget: budget, RecentTail: 64})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Compacted {
		t.Fatal("exact trigger did not compact")
	}
	if plan.ElidedMessages != 1 {
		t.Fatalf("ElidedMessages=%d, want 1", plan.ElidedMessages)
	}
}

func TestPlanElidesPriorTurnIterationsWithinSameObjective(t *testing.T) {
	callOld := plannerToolCall("call-old", "read_file", `{}`)
	callNew := plannerToolCall("call-new", "read_file", `{}`)
	big := strings.Repeat("a", 5000)
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "active user objective"},
		// Iteration 1
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{callOld}},
		{Role: provider.RoleTool, ToolCallID: callOld.ID, Name: callOld.Function.Name, Content: big},
		{Role: provider.RoleAssistant, Content: "intermediate thinking done"},
		// Iteration 2 (latest tool unit)
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{callNew}},
		{Role: provider.RoleTool, ToolCallID: callNew.ID, Name: callNew.Function.Name, Content: "small output"},
	}
	budget := forceCompactBudget(t, messages)
	plan, err := Plan(PlanInput{Messages: messages, Budget: budget, Force: true})
	if err != nil {
		t.Fatalf("plan failed: %v", err)
	}
	if !plan.Compacted {
		t.Fatal("expected compaction")
	}
	if plan.ElidedMessages != 1 {
		t.Fatalf("ElidedMessages=%d, want 1", plan.ElidedMessages)
	}
	for _, msg := range plan.Messages {
		if msg.Role == provider.RoleTool && msg.ToolCallID == "call-old" {
			if !strings.HasPrefix(msg.Content, "[context elided prior tool result;") {
				t.Fatalf("prior iteration tool result was not elided: %q", msg.Content)
			}
		}
		if msg.Role == provider.RoleTool && msg.ToolCallID == "call-new" {
			if msg.Content != "small output" {
				t.Fatalf("latest tool result was mutated unexpectedly: %q", msg.Content)
			}
		}
	}
}

func TestPlanEmergencyElisionWhenMandatoryLatestToolResultExceedsBudget(t *testing.T) {
	call := plannerToolCall("call-huge", "read_file", `{}`)
	huge := strings.Repeat("x", 50_000)
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "active objective"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call}},
		{Role: provider.RoleTool, ToolCallID: call.ID, Name: call.Function.Name, Content: huge},
	}
	// Budget is 2,000 tokens — big enough for system + user + tool notice (~100 tokens), but far too small for 50k chars.
	budget := 2000
	plan, err := Plan(PlanInput{Messages: messages, Budget: budget, Force: true})
	if err != nil {
		t.Fatalf("expected emergency elision to succeed within budget, got error: %v", err)
	}
	if !plan.Compacted {
		t.Fatal("expected compaction")
	}
	if plan.ElidedMessages != 1 {
		t.Fatalf("ElidedMessages=%d, want 1", plan.ElidedMessages)
	}
	var foundElided bool
	for _, msg := range plan.Messages {
		if msg.Role == provider.RoleTool && msg.ToolCallID == "call-huge" {
			foundElided = true
			if !strings.HasPrefix(msg.Content, "[context elided prior tool result;") {
				t.Fatalf("huge mandatory tool result was not emergency elided: %q", msg.Content)
			}
		}
	}
	if !foundElided {
		t.Fatal("call-huge message missing from retained set")
	}
}

// TestPlanEmergencyElisionFiresBelowBudgetButAboveTarget pins the fix for a
// real, reachable "compaction runs but barely helps" defect: a mandatory tool
// result whose cost sits BETWEEN the 50% target and the 100% budget used to
// survive every compaction pass whole, because emergency elision only fired
// once the mandatory set exceeded the FULL budget - the gate this test's
// sibling above (...ExceedsBudget) exercises. mandatoryIndexes always keeps
// the latest tool unit whole and ordinary elision explicitly skips anything
// mandatory, so nothing else could ever shrink it. A single verbose
// run_command or read_file in the latest step routinely lands a mandatory set
// in exactly this window (tool-result byte caps default to effectively
// uncapped), so AfterTokens settled at the mandatory floor - often much
// closer to the 80% trigger than the 50% target - and every subsequent turn
// re-ran compaction for almost no reduction.
func TestPlanEmergencyElisionFiresBelowBudgetButAboveTarget(t *testing.T) {
	call := plannerToolCall("call-mid", "run_command", `{}`)
	// 24,000 chars of tool output prices the mandatory set at ~60% of a
	// 10,000-token budget: comfortably under the OLD gate (> Budget, 100%)
	// so it would never have been touched, but well over the target (50%)
	// the planner is actually trying to reach.
	body := strings.Repeat("x", 24_000)
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "active objective"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call}},
		{Role: provider.RoleTool, ToolCallID: call.ID, Name: call.Function.Name, Content: body},
	}
	budget := 10_000
	target := PercentFloor(budget, 1, 2)
	plan, err := Plan(PlanInput{Messages: messages, Budget: budget, Force: true})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.BeforeTokens <= target {
		t.Fatalf("fixture BeforeTokens=%d, want it above target=%d for this test to mean anything", plan.BeforeTokens, target)
	}
	if plan.BeforeTokens >= budget {
		t.Fatalf("fixture BeforeTokens=%d, want it below budget=%d - this is the OTHER (already-covered) gate", plan.BeforeTokens, budget)
	}
	if !plan.Compacted {
		t.Fatal("expected compaction")
	}
	if plan.ElidedMessages != 1 {
		t.Fatalf("ElidedMessages=%d, want 1 - a mandatory result between target and budget must still be emergency elided", plan.ElidedMessages)
	}
	if plan.AfterTokens >= target {
		t.Fatalf("AfterTokens=%d still at or above target=%d after emergency elision ran", plan.AfterTokens, target)
	}
	var foundElided bool
	for _, msg := range plan.Messages {
		if msg.Role == provider.RoleTool && msg.ToolCallID == "call-mid" {
			foundElided = true
			if !strings.HasPrefix(msg.Content, "[context elided prior tool result;") {
				t.Fatalf("mandatory tool result was not emergency elided: %q", msg.Content)
			}
		}
	}
	if !foundElided {
		t.Fatal("call-mid message missing from retained set")
	}
}
