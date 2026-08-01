package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func TestContextCompactionInvariants(t *testing.T) {
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	call := provider.ToolCall{ID: "call-1", Type: "function"}
	call.Function.Name = "read_file"
	call.Function.Arguments = `{"path":"argument-sentinel"}`
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: "system-prompt-sentinel"},
		{Role: provider.RoleUser, Content: "user-sentinel"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call}},
		{Role: provider.RoleTool, ToolCallID: "call-1", Name: "read_file", Content: "tool-secret-sentinel"},
	}
	events, payloads, err := contextmgr.ProjectSource(context.Background(), principal, messages, 1, contextstate.RedactionPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := contextstate.MarshalCanonical(struct {
		Events   []contextstate.SourceEvent
		Payloads []contextstate.PayloadRecord
	}{events, payloads})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "system-prompt-sentinel") || strings.Contains(string(raw), "argument-sentinel") || strings.Contains(string(raw), "tool-secret-sentinel") {
		t.Fatalf("source projection leaked content: %s", raw)
	}
	if len(events) != 3 || len(payloads) != 2 {
		t.Fatalf("events=%d payloads=%d", len(events), len(payloads))
	}
	policy := contextstate.RedactionPolicy{Configured: true, Classifier: func(data []byte) error {
		if strings.Contains(string(data), "tool-secret-sentinel") {
			return contextstate.ErrInvalidDTO
		}
		return nil
	}}
	if _, _, err := contextmgr.ProjectSource(context.Background(), principal, messages, 1, policy); err == nil {
		t.Fatal("configured source classifier accepted tool sentinel")
	}
}
