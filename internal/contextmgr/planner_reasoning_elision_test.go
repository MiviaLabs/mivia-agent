package contextmgr

import (
	"context"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// reasoningElisionHistory builds a two-turn history with reasoning on three
// assistant messages: a stale tool-call turn (index 2), a stale text turn
// (index 4), and the mandatory latest tool-call turn (index 6). Only the two
// stale turns are eligible for reasoning elision.
func reasoningElisionHistory() []provider.Message {
	oldCall := plannerToolCall("call-old", "read_file", `{"path":"old.txt"}`)
	newCall := plannerToolCall("call-new", "read_file", `{"path":"new.txt"}`)
	staleA := strings.Repeat("stale plan for the old objective. ", 20)
	staleB := strings.Repeat("stale wrap-up thoughts. ", 20)
	return []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "old objective"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{oldCall}, ReasoningContent: staleA},
		{Role: provider.RoleTool, ToolCallID: oldCall.ID, Name: oldCall.Function.Name, Content: "result"},
		{Role: provider.RoleAssistant, Content: "finished prior turn", ReasoningContent: staleB},
		{Role: provider.RoleUser, Content: "current objective"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{newCall}, ReasoningContent: "current reasoning"},
		{Role: provider.RoleTool, ToolCallID: newCall.ID, Name: newCall.Function.Name, Content: "small"},
	}
}

// TestPlanElidesStaleReasoningBeforeObjective pins that reasoning on stale
// assistant turns before the current objective is replaced with the marker,
// while the mandatory latest tool unit and at/after-objective turns keep
// their reasoning verbatim.
func TestPlanElidesStaleReasoningBeforeObjective(t *testing.T) {
	messages := reasoningElisionHistory()
	staleA := messages[2].ReasoningContent
	staleB := messages[4].ReasoningContent

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
	var marked, kept int
	for _, msg := range plan.Messages {
		if msg.Role != provider.RoleAssistant {
			continue
		}
		switch msg.ReasoningContent {
		case reasoningElisionMarker:
			marked++
		case "current reasoning":
			kept++
		case staleA, staleB:
			t.Fatalf("stale reasoning survived elision: %q", msg.ReasoningContent)
		}
	}
	if marked != 2 {
		t.Fatalf("marked assistant turns = %d, want 2", marked)
	}
	if kept != 1 {
		t.Fatal("mandatory latest tool-call turn lost its reasoning")
	}
	if plan.ElidedReasoningMessages != 2 {
		t.Fatalf("ElidedReasoningMessages=%d, want 2", plan.ElidedReasoningMessages)
	}
	if plan.ElidedReasoningBytes != len(staleA)+len(staleB) {
		t.Fatalf("ElidedReasoningBytes=%d, want %d", plan.ElidedReasoningBytes, len(staleA)+len(staleB))
	}
	// Input messages must stay untouched: planCompact works on a clone.
	if messages[2].ReasoningContent != staleA || messages[4].ReasoningContent != staleB {
		t.Fatal("input messages were mutated")
	}
}

// TestPlanReasoningElisionDropsAfterTokens pins that the marker replacement
// strictly lowers the retained-cost estimate against the same retained set
// with raw reasoning.
func TestPlanReasoningElisionDropsAfterTokens(t *testing.T) {
	messages := reasoningElisionHistory()
	plan, err := Plan(PlanInput{
		Messages:   messages,
		Budget:     forceCompactBudget(t, messages),
		Force:      true,
		RecentTail: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Compacted || len(plan.Messages) != len(messages) {
		t.Fatalf("expected full retention under RecentTail=64: compacted=%v retained=%d", plan.Compacted, len(plan.Messages))
	}
	// With everything retained, the no-elision cost is the raw history cost.
	rawCost := provider.EstimateMessagesPromptCost(messages, 0, provider.ContextAccountingProfile{})
	if plan.AfterTokens >= rawCost {
		t.Fatalf("AfterTokens=%d did not drop below raw cost %d", plan.AfterTokens, rawCost)
	}
}

// TestPlanReasoningElisionIsIdempotent pins that re-planning already-marked
// messages elides nothing new.
func TestPlanReasoningElisionIsIdempotent(t *testing.T) {
	messages := reasoningElisionHistory()
	first, err := Plan(PlanInput{
		Messages:   messages,
		Budget:     forceCompactBudget(t, messages),
		Force:      true,
		RecentTail: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.ElidedReasoningMessages == 0 {
		t.Fatal("first plan elided no reasoning; second-pass check is vacuous")
	}
	second, err := Plan(PlanInput{
		Messages:   first.Messages,
		Budget:     forceCompactBudget(t, first.Messages),
		Force:      true,
		RecentTail: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.ElidedReasoningMessages != 0 || second.ElidedReasoningBytes != 0 {
		t.Fatalf("second plan elided again: msgs=%d bytes=%d", second.ElidedReasoningMessages, second.ElidedReasoningBytes)
	}
}

// TestStructuralPreparationCarriesReasoningElisionCounters pins that Prepare
// mirrors the reasoning-elision aggregates from PlanResult onto Preparation.
func TestStructuralPreparationCarriesReasoningElisionCounters(t *testing.T) {
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := contextstate.NewBindingRevision("provider", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	messages := reasoningElisionHistory()
	staleBytes := len(messages[2].ReasoningContent) + len(messages[4].ReasoningContent)
	prep, err := (StructuralPreparationManager{}).Prepare(context.Background(), PrepareInput{
		Messages: messages, Budget: forceCompactBudget(t, messages), Force: true, RecentTail: 64,
		Principal: principal, Binding: binding, Revision: contextstate.Revision{Session: 1, Durable: 1, Source: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !prep.Compacted {
		t.Fatal("expected compacted preparation")
	}
	if prep.ElidedReasoningMessages != 2 || prep.ElidedReasoningBytes != staleBytes {
		t.Fatalf("reasoning counters = msgs=%d bytes=%d, want msgs=2 bytes=%d", prep.ElidedReasoningMessages, prep.ElidedReasoningBytes, staleBytes)
	}
}

// TestPlanReasoningElisionSkipsCheaperThanMarker pins the strictly-cheaper
// guard's negative branch: stale reasoning shorter than the marker is left
// verbatim with zero reasoning-elision stats.
func TestPlanReasoningElisionSkipsCheaperThanMarker(t *testing.T) {
	messages := reasoningElisionHistory()
	messages[2].ReasoningContent = "ok"
	messages[4].ReasoningContent = "no"

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
	for _, msg := range plan.Messages {
		if msg.ReasoningContent == reasoningElisionMarker {
			t.Fatal("short reasoning must not be replaced by a longer marker")
		}
	}
	if plan.ElidedReasoningMessages != 0 || plan.ElidedReasoningBytes != 0 {
		t.Fatalf("stats = %d/%d, want 0/0", plan.ElidedReasoningMessages, plan.ElidedReasoningBytes)
	}
}
