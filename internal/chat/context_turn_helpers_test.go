package chat

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
)

func TestContextTurnMessagesFallsBackToLatestUser(t *testing.T) {
	messages := []provider.Message{
		{Role: provider.RoleUser, Content: "older"},
		{Role: provider.RoleAssistant, Content: "answer"},
		{Role: provider.RoleUser, Content: "latest"},
		{Role: provider.RoleAssistant, Content: "new answer"},
	}
	got := contextTurnMessages(messages, "missing")
	if len(got) != 2 || got[0].Content != "latest" || got[1].Content != "new answer" {
		t.Fatalf("fallback messages = %#v", got)
	}
	got[0].Content = "changed"
	if messages[2].Content != "latest" {
		t.Fatal("fallback messages alias the session history")
	}
}

func TestContextTurnMessagesReturnsNilWithoutUser(t *testing.T) {
	messages := []provider.Message{{Role: provider.RoleAssistant, Content: "answer"}}
	if got := contextTurnMessages(messages, "missing"); got != nil {
		t.Fatalf("messages = %#v, want nil", got)
	}
}

func TestOutputReserveReturnsConfiguredLimit(t *testing.T) {
	limit := 42
	if got := outputReserve(&limit, reasoning.High); got != limit {
		t.Fatalf("output reserve = %d, want %d", got, limit)
	}
}

// TestOutputReserveFallsBackToReasoningFloorWhenUnset pins that an unset
// MaxTokens no longer fingerprints the plan's idempotency key as if it
// reserved 0: outputReserve falls back to reasoning.OutputReserveFloor(level),
// the same fallback the wire request applies (effectiveMaxTokens in
// openai_compat_request.go). This does NOT itself shrink the prompt budget
// the planner packs history against - see config.EffectiveOutputTokens for
// where that reserve is actually applied.
func TestOutputReserveFallsBackToReasoningFloorWhenUnset(t *testing.T) {
	got := outputReserve(nil, reasoning.Max)
	want := reasoning.OutputReserveFloor(reasoning.Max)
	if got != want || got == 0 {
		t.Fatalf("output reserve = %d, want %d (reasoning.OutputReserveFloor(Max), non-zero)", got, want)
	}
}
