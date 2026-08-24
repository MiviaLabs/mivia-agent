package agent

// sdkPromptBudgetPreflight's prune hysteresis (mirrors contextmgr.Plan's
// trigger 80% / target 50% shape, ported from the legacy pruneHistory in
// agentloop_adapter.go's sdkPruneToBudget): pruning must not fire on every
// step once history nears the budget, only once the estimated cost crosses
// the trigger, and then down to the target - so the provider prompt-cache
// prefix survives many steps between drops instead of being invalidated
// from token 0 on every step.

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// pruneTurn returns one user/assistant exchange whose combined content is
// roughly size bytes, so callers can size history precisely against the
// ~4-chars-per-token estimator (provider.context.go's estimateTokens).
func pruneTurn(tag string, size int) []provider.Message {
	half := size / 2
	return []provider.Message{
		{Role: provider.RoleUser, Content: tag + "-u-" + strings.Repeat("a", half)},
		{Role: provider.RoleAssistant, Content: tag + "-a-" + strings.Repeat("b", half)},
	}
}

func countPruneEvents(t *testing.T, opts *Options) *int {
	t.Helper()
	n := 0
	opts.OnEvent = func(e Event) {
		if e.Kind == EventPrune {
			n++
		}
	}
	return &n
}

// TestSDKPromptBudgetPreflightStaysUntouchedUnderTrigger locks that a history
// whose estimated cost never crosses 80% of the budget is never pruned,
// across repeated per-step calls, and the untouched prefix stays
// byte-identical (same messages, same content) as new turns are appended -
// the exact invariant a provider's prompt cache depends on.
func TestSDKPromptBudgetPreflightStaysUntouchedUnderTrigger(t *testing.T) {
	loop := &Loop{Tools: tools.NewRegistry(), Messages: []provider.Message{
		{Role: provider.RoleSystem, Content: "fixed system prompt"},
	}}
	opts := Options{MaxContextTokens: 10_000}
	pruned := countPruneEvents(t, &opts)

	for step := 0; step < 5; step++ {
		loop.Messages = append(loop.Messages, pruneTurn("t", 400)...)
		before := append([]provider.Message(nil), loop.Messages...)
		if _, err := sdkPromptBudgetPreflight(loop, opts, loop.Messages); err != nil {
			t.Fatalf("step %d: %v", step, err)
		}
		if len(loop.Messages) != len(before) {
			t.Fatalf("step %d: history mutated while under trigger: had %d messages, now %d", step, len(before), len(loop.Messages))
		}
		for i := range before {
			if loop.Messages[i].Content != before[i].Content {
				t.Fatalf("step %d: message %d content changed while under trigger:\n got %q\nwant %q", step, i, loop.Messages[i].Content, before[i].Content)
			}
		}
	}
	if *pruned != 0 {
		t.Fatalf("prune fired %d times while history stayed under the 80%% trigger", *pruned)
	}
}

// TestSDKPromptBudgetPreflightPrunesOnceThenStabilizes locks the hysteresis
// shape itself: once history crosses the 80% trigger, the preflight drops
// old turns down to roughly the 50% target in one shot, and does NOT prune
// again on the very next call even though the trimmed history is still below
// the (now much higher relative) trigger - i.e. one front-drop buys several
// stable steps, not a fresh drop every step at the boundary.
func TestSDKPromptBudgetPreflightPrunesOnceThenStabilizes(t *testing.T) {
	const budget = 10_000
	loop := &Loop{Tools: tools.NewRegistry(), Messages: []provider.Message{
		{Role: provider.RoleSystem, Content: "fixed system prompt"},
	}}
	// Build history comfortably past the 80% trigger (8,000 tokens ~= 32,000
	// chars) before the first prune call.
	for i := 0; i < 20; i++ {
		loop.Messages = append(loop.Messages, pruneTurn("seed", 2_000)...)
	}
	profile := loop.contextAccounting()
	beforeTokens := provider.MessagesTokens(loop.Messages, profile)
	trigger := budget * 4 / 5
	if beforeTokens < trigger {
		t.Fatalf("test fixture too small: beforeTokens=%d, want >= trigger %d", beforeTokens, trigger)
	}

	opts := Options{MaxContextTokens: budget}
	pruned := countPruneEvents(t, &opts)

	if _, err := sdkPromptBudgetPreflight(loop, opts, loop.Messages); err != nil {
		t.Fatal(err)
	}
	if *pruned != 1 {
		t.Fatalf("prune fired %d times on the crossing step, want exactly 1", *pruned)
	}
	afterTokens := provider.MessagesTokens(loop.Messages, profile)
	target := budget / 2
	// Generous slack: pruning trims by whole turns, so it can land
	// somewhat under the exact target, never far over it.
	if afterTokens > target+target/2 {
		t.Fatalf("pruned history is %d tokens, want near the %d target (budget=%d)", afterTokens, target, budget)
	}
	if afterTokens >= beforeTokens {
		t.Fatalf("pruned history (%d tokens) is not smaller than before (%d)", afterTokens, beforeTokens)
	}

	// The very next call, with nothing appended, must be a no-op: the pruned
	// history already sits well under ITS OWN 80% trigger.
	stable := append([]provider.Message(nil), loop.Messages...)
	if _, err := sdkPromptBudgetPreflight(loop, opts, loop.Messages); err != nil {
		t.Fatal(err)
	}
	if *pruned != 1 {
		t.Fatalf("prune fired again immediately after pruning down to target (count=%d), want it to stay at 1", *pruned)
	}
	if len(loop.Messages) != len(stable) {
		t.Fatalf("history mutated on the stabilizing call: had %d messages, now %d", len(stable), len(loop.Messages))
	}
	for i := range stable {
		if loop.Messages[i].Content != stable[i].Content {
			t.Fatalf("message %d changed on the stabilizing call:\n got %q\nwant %q", i, loop.Messages[i].Content, stable[i].Content)
		}
	}
}
