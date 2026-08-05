package chat

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
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
	if got := outputReserve(&limit); got != limit {
		t.Fatalf("output reserve = %d, want %d", got, limit)
	}
}
