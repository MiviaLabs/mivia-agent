package contextmgr

import (
	"context"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func TestStructuralPreparationCarriesElisionCounters(t *testing.T) {
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := contextstate.NewBindingRevision("provider", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	oldCall := plannerToolCall("call-old", "read_file", `{}`)
	newCall := plannerToolCall("call-new", "read_file", `{}`)
	big := strings.Repeat("x", 3000)
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "old"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{oldCall}},
		{Role: provider.RoleTool, ToolCallID: oldCall.ID, Name: "read_file", Content: big},
		{Role: provider.RoleAssistant, Content: "done"},
		{Role: provider.RoleUser, Content: "current"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{newCall}},
		{Role: provider.RoleTool, ToolCallID: newCall.ID, Name: "read_file", Content: "small"},
	}
	cost, err := provider.EstimatePromptCost(messages, nil)
	if err != nil {
		t.Fatal(err)
	}
	prep, err := (StructuralPreparationManager{}).Prepare(context.Background(), PrepareInput{
		Messages: messages, Budget: cost + 10_000, Force: true,
		Principal: principal, Binding: binding, Revision: contextstate.Revision{Session: 1, Durable: 1, Source: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !prep.Compacted {
		t.Fatal("expected compacted preparation")
	}
	if prep.ElidedMessages != 1 || prep.ElidedBytes != len(big) {
		t.Fatalf("elision counters = msgs=%d bytes=%d", prep.ElidedMessages, prep.ElidedBytes)
	}
}

func TestStructuralPreparationZeroElisionBelowTrigger(t *testing.T) {
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := contextstate.NewBindingRevision("provider", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "hi"},
	}
	cost, err := provider.EstimatePromptCost(messages, nil)
	if err != nil {
		t.Fatal(err)
	}
	prep, err := (StructuralPreparationManager{}).Prepare(context.Background(), PrepareInput{
		Messages: messages, Budget: cost*5 + 100,
		Principal: principal, Binding: binding, Revision: contextstate.Revision{Session: 1, Durable: 1, Source: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if prep.Compacted || prep.ElidedMessages != 0 || prep.ElidedBytes != 0 {
		t.Fatalf("unexpected compaction/elision: %+v", prep)
	}
}
