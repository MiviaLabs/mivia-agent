package chat

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// TestSaveLastPersistsLoneUserMessage locks the defect where SaveLast gated
// the exit auto-save on len(msgs) > 1: a session whose only content is one
// user message (no system prompt) was never written to the __last__ exit
// snapshot, so the question vanished on exit. The package's own hasContent
// helper (pinned by TestHasContent_UserOnly) declares a lone user message to
// be real content, so the exit gate must use it instead of the raw length.
func TestSaveLastPersistsLoneUserMessage(t *testing.T) {
	store := newTestStore(t)
	mgr := NewSaveManager(store, "test-model", "test-provider")
	sess := NewSession(&config.Resolved{Model: "test-model"}, &fakeCompleter{})
	sess.SetSessionStore(store, mgr)
	sess.Messages = []provider.Message{{Role: provider.RoleUser, Content: "question that must survive exit"}}

	if err := sess.SaveLast(); err != nil {
		t.Fatalf("SaveLast: %v", err)
	}

	infos, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) == 0 {
		t.Fatal("SaveLast dropped a lone user message: no auto-save on disk")
	}
	loaded, err := store.Load(infos[0].Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].Role != provider.RoleUser || !strings.Contains(loaded[0].Content, "must survive exit") {
		t.Fatalf("loaded transcript = %+v, want the lone user question", loaded)
	}
}

// TestSaveAfterTurnPersistsLoneUserMessage locks the same defect on the
// per-turn crash-recovery snapshot: saveAfterTurn dropped a session holding
// only one user message, so the question never reached the rolling _turn_
// snapshot and was lost on crash. SaveManager's own hasContent gate
// (save_manager.go) already persists such transcripts; the session-level gate
// in saveAfterTurn contradicted it.
func TestSaveAfterTurnPersistsLoneUserMessage(t *testing.T) {
	store := newTestStore(t)
	mgr := NewSaveManager(store, "test-model", "test-provider")
	sess := NewSession(&config.Resolved{Model: "test-model"}, &fakeCompleter{})
	sess.SetSessionStore(store, mgr)
	sess.Messages = []provider.Message{{Role: provider.RoleUser, Content: "question that must survive crash"}}

	if err := sess.saveAfterTurn(sess.currentSaveToken()); err != nil {
		t.Fatalf("saveAfterTurn: %v", err)
	}
	if got := mgr.Metrics().SaveAfterTurnCount; got != 1 {
		t.Fatalf("SaveAfterTurnCount = %d, want 1", got)
	}

	infos, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || !IsTurnSaveName(infos[0].Name) {
		t.Fatalf("expected one rolling turn snapshot, got %+v", infos)
	}
	loaded, err := store.Load(infos[0].Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].Role != provider.RoleUser || !strings.Contains(loaded[0].Content, "must survive crash") {
		t.Fatalf("loaded transcript = %+v, want the lone user question", loaded)
	}
}

// Negative paths: sessions with no real content stay no-ops for both gates,
// exactly as they were before the fix.

func TestSaveLastSystemOnlyNoSave(t *testing.T) {
	store := newTestStore(t)
	mgr := NewSaveManager(store, "test-model", "test-provider")
	sess := NewSession(&config.Resolved{Model: "test-model", SystemPrompt: "system"}, &fakeCompleter{})
	sess.SetSessionStore(store, mgr)

	if err := sess.SaveLast(); err != nil {
		t.Fatalf("SaveLast: %v", err)
	}
	infos, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 0 {
		t.Fatalf("system-only session produced %d auto-saves, want 0", len(infos))
	}
}

func TestSaveLastEmptyNoSave(t *testing.T) {
	store := newTestStore(t)
	mgr := NewSaveManager(store, "test-model", "test-provider")
	sess := NewSession(&config.Resolved{Model: "test-model"}, &fakeCompleter{})
	sess.SetSessionStore(store, mgr)

	if err := sess.SaveLast(); err != nil {
		t.Fatalf("SaveLast: %v", err)
	}
	infos, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 0 {
		t.Fatalf("empty session produced %d auto-saves, want 0", len(infos))
	}
}

func TestSaveAfterTurnSystemOnlyNoSave(t *testing.T) {
	store := newTestStore(t)
	mgr := NewSaveManager(store, "test-model", "test-provider")
	sess := NewSession(&config.Resolved{Model: "test-model", SystemPrompt: "system"}, &fakeCompleter{})
	sess.SetSessionStore(store, mgr)

	if err := sess.saveAfterTurn(sess.currentSaveToken()); err != nil {
		t.Fatalf("saveAfterTurn: %v", err)
	}
	if got := mgr.Metrics().SaveAfterTurnCount; got != 0 {
		t.Fatalf("system-only SaveAfterTurnCount = %d, want 0", got)
	}
	infos, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 0 {
		t.Fatalf("system-only session produced %d snapshots, want 0", len(infos))
	}
}

func TestSaveAfterTurnEmptyNoSave(t *testing.T) {
	store := newTestStore(t)
	mgr := NewSaveManager(store, "test-model", "test-provider")
	sess := NewSession(&config.Resolved{Model: "test-model"}, &fakeCompleter{})
	sess.SetSessionStore(store, mgr)

	if err := sess.saveAfterTurn(sess.currentSaveToken()); err != nil {
		t.Fatalf("saveAfterTurn: %v", err)
	}
	if got := mgr.Metrics().SaveAfterTurnCount; got != 0 {
		t.Fatalf("empty SaveAfterTurnCount = %d, want 0", got)
	}
	infos, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 0 {
		t.Fatalf("empty session produced %d snapshots, want 0", len(infos))
	}
}
