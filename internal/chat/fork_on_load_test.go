package chat

import (
	"io"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

func TestLoadedContextSessionFalseForFreshSession(t *testing.T) {
	session := NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, &fakeCompleter{out: "answer"})
	if session.LoadedContextSession() {
		t.Fatal("fresh session must not report as a loaded context session")
	}
}

// TestLoadedContextSessionFalseAfterNamedSaveAndLoad pins that a named
// save+load round-trip through the catalog (chat_sessions table) does NOT set
// loadedContextSession, because chat_sessions rows have no session_id — only
// loads that fall through to the context_sessions durable path report true.
func TestLoadedContextSessionFalseAfterNamedSaveAndLoad(t *testing.T) {
	store, err := storage.OpenSQLite(t.TempDir() + "/context.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	session := NewSession(&config.Resolved{ProviderName: "fake", Model: "model", Models: []string{"model"}}, &fakeCompleter{out: "answer"})
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

	if _, err := session.SendUser(t.Context(), "hello", io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := session.Save("named-snapshot"); err != nil {
		t.Fatal(err)
	}
	if err := session.Clear(); err != nil {
		t.Fatal(err)
	}

	// Named save+load through catalog → chat_sessions → SessionID="" → false.
	if err := session.Load("named-snapshot"); err != nil {
		t.Fatal(err)
	}
	if session.LoadedContextSession() {
		t.Fatal("named catalog save+load must not set loadedContextSession (no session_id)")
	}
}

// TestLoadedContextSessionTrueAfterDurableSessionLoad verifies that loading a
// durable context session (context_sessions fallback path, where SessionID is
// populated) sets the flag to true.
func TestLoadedContextSessionTrueAfterDurableSessionLoad(t *testing.T) {
	store, err := storage.OpenSQLite(t.TempDir() + "/context.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	session := NewSession(&config.Resolved{ProviderName: "fake", Model: "model", Models: []string{"model"}}, &fakeCompleter{out: "answer"})
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

	// A committed turn persists through the durable context path (context_sessions).
	if _, err := session.SendUser(t.Context(), "hello", io.Discard); err != nil {
		t.Fatal(err)
	}

	// Load by the session's own ID. This falls through chat_sessions (no match)
	// to the context_sessions durable path, populating SessionID.
	if err := session.Load(session.SessionID); err != nil {
		t.Fatal(err)
	}
	if !session.LoadedContextSession() {
		t.Fatal("loading a durable context session by ID must set loadedContextSession to true")
	}
}

// TestLoadedContextSessionFlagSurvivesClear verifies the flag is only changed
// by Load, not by Clear or other operations.
func TestLoadedContextSessionFlagSurvivesClear(t *testing.T) {
	store, err := storage.OpenSQLite(t.TempDir() + "/context.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	session := NewSession(&config.Resolved{ProviderName: "fake", Model: "model", Models: []string{"model"}}, &fakeCompleter{out: "answer"})
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

	// Turn + load by session ID → flag becomes true.
	if _, err := session.SendUser(t.Context(), "question", io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := session.Load(session.SessionID); err != nil {
		t.Fatal(err)
	}
	if !session.LoadedContextSession() {
		t.Fatal("expected true after durable session load")
	}

	// Clear does not reset the flag.
	if err := session.Clear(); err != nil {
		t.Fatal(err)
	}
	if !session.LoadedContextSession() {
		t.Fatal("flag must survive Clear; only Load resets it")
	}

	// Another turn does not reset the flag.
	if _, err := session.SendUser(t.Context(), "follow-up", io.Discard); err != nil {
		t.Fatal(err)
	}
	if !session.LoadedContextSession() {
		t.Fatal("flag must survive turns; only Load resets it")
	}
}

// TestLoadedContextSessionAccessorUnderLock verifies the accessor method
// returns the in-memory flag correctly (directly testing the exported method).
func TestLoadedContextSessionAccessorUnderLock(t *testing.T) {
	session := NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, &fakeCompleter{out: "answer"})
	if session.LoadedContextSession() {
		t.Fatal("initial value must be false")
	}

	session.mu.Lock()
	session.loadedContextSession = true
	session.mu.Unlock()
	if !session.LoadedContextSession() {
		t.Fatal("accessor must return true when flag is set")
	}

	session.mu.Lock()
	session.loadedContextSession = false
	session.mu.Unlock()
	if session.LoadedContextSession() {
		t.Fatal("accessor must return false when flag is cleared")
	}
}
