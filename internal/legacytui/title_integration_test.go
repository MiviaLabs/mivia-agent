package legacytui

import (
	"context"
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

func TestIntegrationTUITitleLoadedDurableSession(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	res := &config.Resolved{ProviderName: "test", Model: "test"}
	current := chat.NewSession(res, welcomeStubCompleter{})
	loaded := chat.NewSession(res, welcomeStubCompleter{})
	root := t.TempDir()
	setupTitleTestContext(t, current, store, root)
	setupTitleTestContext(t, loaded, store, root)
	if _, err := loaded.SendUser(context.Background(), "saved request", io.Discard); err != nil {
		t.Fatal(err)
	}

	m := newTUIModel(current, res, true)
	m.mode = modeChat
	m.handleSlash("/load " + loaded.SessionID)
	if m.activeSession == nil || m.activeSession.SessionID != loaded.SessionID {
		t.Fatalf("loaded active session = %#v", m.activeSession)
	}
	m.handleSlash("/title Loaded durable session")

	infos, err := current.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range infos {
		if info.SessionID == loaded.SessionID {
			if info.Title != "Loaded durable session" {
				t.Fatalf("title = %q", info.Title)
			}
			return
		}
	}
	t.Fatalf("loaded session %q missing from %#v", loaded.SessionID, infos)
}

func TestIntegrationTUITitleRejectsSavedSnapshot(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	res := &config.Resolved{ProviderName: "test", Model: "test"}
	session := chat.NewSession(res, welcomeStubCompleter{})
	setupTitleTestContext(t, session, store, t.TempDir())
	m := newTUIModel(session, res, true)
	m.activeSession = &chat.SessionInfo{Name: session.SessionID}
	m.handleSlash("/title must not update")
	if len(m.blocks) == 0 || !strings.Contains(cli.StripANSI(m.blocks[len(m.blocks)-1].Text), "titles are not available for saved snapshots") {
		t.Fatalf("title response = %#v", m.blocks)
	}
}

func TestIntegrationTUITitleUsesLoadedWorktreeInstance(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	res := &config.Resolved{ProviderName: "test", Model: "test"}
	root := t.TempDir()
	current := chat.NewSession(res, welcomeStubCompleter{})
	loaded := chat.NewSession(res, welcomeStubCompleter{})
	setupTitleTestContext(t, current, store, root)
	instance := contextstate.WorktreeInstance{Worktree: "loaded", ID: "wt_1234567890abcdef"}
	principal, err := contextstate.NewPrincipal(cli.ContextWorkspaceID(root), loaded.SessionID, "local-user")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BeginWorktreeCreation(context.Background(), principal, instance, root); err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterWorktreeInstance(context.Background(), principal, instance, root); err != nil {
		t.Fatal(err)
	}
	if err := loaded.SetContextWorktreeBindingAt(instance, root, root); err != nil {
		t.Fatal(err)
	}
	setupTitleTestContextPrincipal(t, loaded, store, root)
	if err := loaded.SetContextStore(store); err != nil {
		t.Fatal(err)
	}
	if _, err := loaded.SendUser(context.Background(), "saved request", io.Discard); err != nil {
		t.Fatal(err)
	}

	m := newTUIModel(current, res, true)
	m.mode = modeChat
	m.activeSession = &chat.SessionInfo{SessionID: loaded.SessionID, Name: loaded.SessionID, WorktreeInstance: instance}
	m.handleSlash("/title Loaded worktree session")

	infos, err := store.ListWorktreeSessions(context.Background(), principal, instance)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].Title != "Loaded worktree session" {
		t.Fatalf("worktree sessions = %#v", infos)
	}
}

func setupTitleTestContext(t *testing.T, session *chat.Session, store *storage.SQLite, root string) {
	t.Helper()
	setupTitleTestContextPrincipal(t, session, store, root)
	if err := session.SetContextStore(store); err != nil {
		t.Fatal(err)
	}
}

func setupTitleTestContextPrincipal(t *testing.T, session *chat.Session, store *storage.SQLite, root string) contextstate.Principal {
	t.Helper()
	principal, err := contextstate.NewPrincipal(cli.ContextWorkspaceID(root), session.SessionID, "local-user")
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
	return principal
}
