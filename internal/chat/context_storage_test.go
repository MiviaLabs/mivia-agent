package chat

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
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

func TestManagedEnsureAndRotationUseRetainedDirectory(t *testing.T) {
	root := t.TempDir()
	worktreeRoot := filepath.Join(root, "wt-a")
	sessionDir := filepath.Join(worktreeRoot, "subdir")
	otherDir := filepath.Join(root, "other")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(otherDir, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := storage.OpenSQLite(filepath.Join(root, "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	session := NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, &fakeCompleter{out: "answer"})
	principal, err := contextstate.NewPrincipal("workspace", session.SessionID, "subject")
	if err != nil {
		t.Fatal(err)
	}
	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	if err := store.BeginWorktreeCreation(context.Background(), principal, instance, worktreeRoot); err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterWorktreeInstance(context.Background(), principal, instance, worktreeRoot); err != nil {
		t.Fatal(err)
	}
	if err := session.SetContextWorktreeBindingAt(instance, worktreeRoot, sessionDir); err != nil {
		t.Fatal(err)
	}
	manager := &contextmgr.ContextManager{PreparationManager: contextmgr.StructuralPreparationManager{}, CheckpointPublisher: contextmgr.PreparationCommitter{Store: store}, Enabled: true}
	if err := session.SetContextManager(manager, principal); err != nil {
		t.Fatal(err)
	}
	oldDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldDir) })
	if err := os.Chdir(otherDir); err != nil {
		t.Fatal(err)
	}
	if err := session.SetContextStore(store); err != nil {
		t.Fatal(err)
	}
	if _, err := session.SendUser(context.Background(), "first", io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := session.RotateSessionID(); err != nil {
		t.Fatal(err)
	}
	if _, err := session.SendUser(context.Background(), "second", io.Discard); err != nil {
		t.Fatal(err)
	}
	infos, err := store.ListWorktreeSessions(context.Background(), principal, instance)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 2 {
		t.Fatalf("managed live sessions = %d, want 2", len(infos))
	}
	for _, info := range infos {
		if info.Dir != sessionDir {
			t.Errorf("session %q directory = %q, want %q", info.Name, info.Dir, sessionDir)
		}
	}
}

func TestContextEnabledAgentTurnsCommitEveryTurn(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "agent-context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	session := NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, &fakeCompleter{out: "answer"})
	session.UseTools = true
	session.Tools = tools.NewRegistry()
	principal, err := contextstate.NewPrincipal("workspace", session.SessionID, "subject")
	if err != nil {
		t.Fatal(err)
	}
	manager := &contextmgr.ContextManager{PreparationManager: contextmgr.StructuralPreparationManager{}, CheckpointPublisher: contextmgr.PreparationCommitter{Store: store}, Enabled: true}
	if err := session.SetContextManager(manager, principal); err != nil {
		t.Fatal(err)
	}
	if err := session.SetContextStore(store); err != nil {
		t.Fatal(err)
	}
	for _, question := range []string{"first question", "second question"} {
		if _, err := session.SendUser(context.Background(), question, io.Discard); err != nil {
			t.Fatalf("send %q: %v", question, err)
		}
	}
	snapshot, err := store.Load(context.Background(), principal, session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Revision.Source != 4 {
		t.Fatalf("durable source revision=%d, want 4 events for two agent turns", snapshot.Revision.Source)
	}
	if got := session.MessagesCopy(); len(got) != 4 {
		t.Fatalf("in-memory history length=%d, want 4", len(got))
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
	_ = session.Clear()
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

func TestManagedWorktreeAdvanceAPIsCarryInstanceWhileActive(t *testing.T) {
	tests := []struct {
		name string
		act  func(*Session) error
	}{
		{name: "clear", act: func(session *Session) error { return session.Clear() }},
		{name: "switch binding", act: func(session *Session) error {
			binding := session.PublishedBinding()
			binding.Model = "next-model"
			return session.SwitchBinding(binding)
		}},
		{name: "select model", act: func(session *Session) error {
			if !session.SelectModel("next-model") {
				return errors.New("SelectModel rejected an active worktree instance")
			}
			return nil
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session, store, principal, instance := managedWorktreeContextSession(t)
			if err := test.act(session); err != nil {
				t.Fatalf("active managed worktree action: %v", err)
			}
			if _, err := store.LoadWorktree(context.Background(), principal, principal.SessionID, instance); err != nil {
				t.Fatalf("load active managed worktree context: %v", err)
			}
		})
	}
}

func TestManagedWorktreeAdvanceAPIsRejectDeletingInstance(t *testing.T) {
	tests := []struct {
		name string
		act  func(*Session) error
	}{
		{name: "clear", act: func(session *Session) error { return session.Clear() }},
		{name: "switch binding", act: func(session *Session) error {
			binding := session.PublishedBinding()
			binding.Model = "next-model"
			return session.SwitchBinding(binding)
		}},
		{name: "select model", act: func(session *Session) error {
			if session.SelectModel("next-model") {
				return errors.New("SelectModel accepted a deleting worktree instance")
			}
			return nil
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session, store, principal, instance := managedWorktreeContextSession(t)
			if err := store.BeginWorktreeDeletion(context.Background(), principal, instance); err != nil {
				t.Fatalf("begin worktree deletion: %v", err)
			}
			err := test.act(session)
			if test.name == "select model" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, contextstate.ErrWorktreeDeleted) {
				t.Fatalf("deleting managed worktree action error = %v, want ErrWorktreeDeleted", err)
			}
		})
	}
}

func managedWorktreeContextSession(t *testing.T) (*Session, *storage.SQLite, contextstate.Principal, contextstate.WorktreeInstance) {
	t.Helper()
	root := t.TempDir()
	store, err := storage.OpenSQLite(filepath.Join(root, "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	session := NewSession(&config.Resolved{ProviderName: "fake", Model: "model", Models: []string{"model", "next-model"}}, &fakeCompleter{out: "answer"})
	principal, err := contextstate.NewPrincipal("workspace", session.SessionID, "subject")
	if err != nil {
		t.Fatal(err)
	}
	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	worktreePath := filepath.Join(root, "wt-a")
	if err := store.BeginWorktreeCreation(context.Background(), principal, instance, worktreePath); err != nil {
		t.Fatalf("begin worktree creation: %v", err)
	}
	if err := store.RegisterWorktreeInstance(context.Background(), principal, instance, worktreePath); err != nil {
		t.Fatalf("register worktree instance: %v", err)
	}
	if err := session.SetContextWorktreeBinding(instance); err != nil {
		t.Fatalf("set worktree binding: %v", err)
	}
	manager := &contextmgr.ContextManager{
		PreparationManager:  contextmgr.StructuralPreparationManager{},
		CheckpointPublisher: contextmgr.PreparationCommitter{Store: store},
		Enabled:             true,
	}
	if err := session.SetContextManager(manager, principal); err != nil {
		t.Fatalf("set context manager: %v", err)
	}
	if err := session.SetContextStore(store); err != nil {
		t.Fatalf("set context store: %v", err)
	}
	return session, store, principal, instance
}
