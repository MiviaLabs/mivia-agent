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

// TestOutputReserveFallsBackToReasoningFloorWhenUnset pins the fix for a
// planner/wire mismatch: an unset MaxTokens used to reserve 0 context-window
// room for the completion, while the wire request separately asks for up to
// provider.ReasoningOutputReserve(level) tokens (effectiveMaxTokens in
// openai_compat_request.go) - the planner could pack history right up to the
// full budget, then the wire request's declared max_tokens pushes
// prompt_tokens+max_tokens past the model's real context window.
func TestOutputReserveFallsBackToReasoningFloorWhenUnset(t *testing.T) {
	got := outputReserve(nil, reasoning.Max)
	want := provider.ReasoningOutputReserve(reasoning.Max)
	if got != want || got == 0 {
		t.Fatalf("output reserve = %d, want %d (provider.ReasoningOutputReserve(Max), non-zero)", got, want)
	}
}
