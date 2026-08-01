package chat

import (
	"context"
	"io"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

func TestContextEnabledTurnCommitsSQLite(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	session := NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, &fakeCompleter{out: "answer"})
	principal, err := contextstate.NewPrincipal("workspace", session.SessionID, "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := contextstate.NewBindingRevision("fake", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSession(context.Background(), contextstate.EnsureSessionRequest{Principal: principal, Binding: binding}); err != nil {
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
	session.SetContextStore(store)
	if _, err := session.SendUser(context.Background(), "question", io.Discard); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Load(context.Background(), principal, principal.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Revision.Session != 1 || snapshot.Revision.Durable != 1 || snapshot.Revision.Source != 2 {
		t.Fatalf("unexpected revision: %+v", snapshot.Revision)
	}
	if len(snapshot.Source) != 2 || !snapshot.Active.Complete {
		t.Fatalf("source=%d active=%+v", len(snapshot.Source), snapshot.Active)
	}
	if snapshot.Active.SourceRange.Start.Sequence != 1 || snapshot.Active.SourceRange.End.Sequence != 2 {
		t.Fatalf("active source range = %+v", snapshot.Active.SourceRange)
	}
	if session.Store() != nil {
		t.Fatal("context-enabled session exposed a legacy store")
	}
}

func TestContextClearAdvancesDurableHeadWithoutResurrectingCheckpoint(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "clear.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	session := NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, &fakeCompleter{out: "answer"})
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
	if _, err := session.SendUser(context.Background(), "before clear", io.Discard); err != nil {
		t.Fatal(err)
	}
	session.Clear()
	snapshot, err := store.Load(context.Background(), principal, principal.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Active.ID.SessionID != "" {
		t.Fatalf("clear left active checkpoint: %+v", snapshot.Active)
	}
	if snapshot.Revision != (contextstate.Revision{Session: 2, Durable: 2, Source: 2}) {
		t.Fatalf("clear revision = %+v", snapshot.Revision)
	}
}

func TestContextModelSelectionAdvancesBindingFence(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "switch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	session := NewSession(&config.Resolved{ProviderName: "fake", Model: "model", Models: []string{"model", "next-model"}}, &fakeCompleter{out: "answer"})
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
	if _, err := session.SendUser(context.Background(), "before switch", io.Discard); err != nil {
		t.Fatal(err)
	}
	if !session.SelectModel("next-model") {
		t.Fatal("model selection rejected")
	}
	snapshot, err := store.Load(context.Background(), principal, principal.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Binding.Model != "next-model" || snapshot.Binding.Generation != 2 {
		t.Fatalf("binding after selection = %+v", snapshot.Binding)
	}
	if snapshot.Revision != (contextstate.Revision{Session: 2, Durable: 2, Source: 2}) {
		t.Fatalf("selection revision = %+v", snapshot.Revision)
	}
}
