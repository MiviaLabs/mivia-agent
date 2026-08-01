package chat

import (
	"context"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

func TestContextUsageReportsRequestPercentage(t *testing.T) {
	session := NewSession(&config.Resolved{Model: "model", SystemPrompt: "system"}, &fakeCompleter{out: "answer"})
	session.MaxContextTokens = 100
	session.binding.PromptBudgetTokens = 100
	session.Messages = []provider.Message{{Role: provider.RoleSystem, Content: strings.Repeat("s", 100)}}
	usage := session.ContextUsage()
	if usage.UsedTokens == 0 || usage.BudgetTokens != 100 || usage.Percent <= 0 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestCompactPublishesStructuralCheckpointImmediately(t *testing.T) {
	session := NewSession(&config.Resolved{ProviderName: "fake", Model: "model", SystemPrompt: "system"}, &fakeCompleter{out: "answer"})
	store, err := storage.OpenSQLite(t.TempDir() + "/context.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := contextstate.NewPrincipal("workspace", session.SessionID, "subject")
	if err != nil {
		t.Fatal(err)
	}
	manager := &contextmgr.ContextManager{
		PreparationManager:  contextmgr.StructuralPreparationManager{},
		CheckpointPublisher: contextmgr.PreparationCommitter{Store: store},
		Enabled:             true,
	}
	if err := session.SetContextManager(manager, principal); err != nil {
		t.Fatal(err)
	}
	if err := session.SetContextStore(store); err != nil {
		t.Fatal(err)
	}
	if _, err := session.SendUser(context.Background(), "first", nil); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		session.Messages = append(session.Messages,
			provider.Message{Role: provider.RoleUser, Content: strings.Repeat("old question ", 20)},
			provider.Message{Role: provider.RoleAssistant, Content: strings.Repeat("old answer ", 20)},
		)
	}
	before := len(session.Messages)
	if err := session.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(session.Messages) >= before {
		t.Fatalf("compact retained %d messages, before=%d", len(session.Messages), before)
	}
	snapshot, err := store.Load(context.Background(), principal, session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Active.ActiveContext) == 0 {
		t.Fatal("compact did not publish active context")
	}
}
