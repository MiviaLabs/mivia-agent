package cli

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// makeTestSessionWithStore builds a session wired to a real FileSessionStore in
// a temp dir, mirroring how runChat wires persistence at startup.
func makeTestSessionWithStore(t *testing.T, model string) *chat.Session {
	t.Helper()
	dir := t.TempDir()
	store, err := chat.NewFileSessionStore(dir)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	comp := welcomeStubCompleter{}
	sess := chat.NewSession(&config.Resolved{Model: model, SystemPrompt: "SYS"}, comp)
	sess.UseTools = false
	sess.SessionDir = dir
	mgr := chat.NewSaveManager(store, model, comp.Name())
	sess.SetSessionStore(store, mgr)
	return sess
}

// addTurn simulates one completed user→assistant turn so the session has
// meaningful history that /new must persist.
func addTurn(t *testing.T, sess *chat.Session, user, assistant string) {
	t.Helper()
	sess.Messages = append(sess.Messages,
		provider.Message{Role: provider.RoleUser, Content: user},
		provider.Message{Role: provider.RoleAssistant, Content: assistant},
	)
}

// TestSlashNewPersistsOldSessionAndClears verifies /new saves the current
// conversation as a distinct recoverable session, then clears in-memory history.
func TestSlashNewPersistsOldSessionAndClears(t *testing.T) {
	sess := makeTestSessionWithStore(t, "m")
	addTurn(t, sess, "secret-question", "secret-answer")
	beforeID := sess.SessionID

	m := newReadyChatModel(30, 90)
	m.session = sess
	m.waiting = false

	if !m.handleSlash("/new") {
		t.Fatal("/new must be handled by the TUI")
	}

	// In-memory history is cleared (only the system prompt remains).
	if n := sess.MessagesCount(); n != 1 {
		t.Fatalf("expected 1 message (system prompt) after /new, got %d", n)
	}
	if got := sess.Messages[0].Role; got != provider.RoleSystem {
		t.Fatalf("expected system prompt to survive /new, got role %v", got)
	}

	// The old conversation must survive on disk as a distinct session.
	sessions, err := sess.ListSessions()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(sessions) == 0 {
		t.Fatal("expected at least one saved session after /new; old chat was lost")
	}
	// Re-read via a fresh store bound to the same dir to confirm the content
	// landed on disk (sess.Load would mutate in-memory state).
	store2, err := chat.NewFileSessionStore(sess.SessionDir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, si := range sessions {
		tmp := &chat.Session{}
		tmp.SetSessionStore(store2, nil)
		if err := tmp.Load(si.Name); err != nil {
			continue
		}
		for _, msg := range tmp.Messages {
			if strings.Contains(msg.Content, "secret-question") || strings.Contains(msg.Content, "secret-answer") {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("old conversation content not found in any saved session; /new lost it")
	}

	// Fresh identity: SessionID must change.
	if sess.SessionID == beforeID {
		t.Fatal("expected a fresh SessionID after /new")
	}
}

// TestSlashNewBlocksWhileBusy verifies /new is a no-op when an agent turn is
// in flight (m.waiting), because the SaveManager swap would race the turn's
// writeback.
func TestSlashNewBlocksWhileBusy(t *testing.T) {
	sess := makeTestSessionWithStore(t, "m")
	addTurn(t, sess, "q", "a")
	beforeID := sess.SessionID
	beforeCount := sess.MessagesCount()

	m := newReadyChatModel(30, 90)
	m.session = sess
	m.waiting = true // a turn is running

	if !m.handleSlash("/new") {
		t.Fatal("/new must still be handled (with a notice) while busy")
	}

	// Nothing changed: no save, no clear, no new ID.
	if sess.SessionID != beforeID {
		t.Fatalf("SessionID changed during busy /new: %q -> %q", beforeID, sess.SessionID)
	}
	if sess.MessagesCount() != beforeCount {
		t.Fatalf("message count changed during busy /new: %d -> %d", beforeCount, sess.MessagesCount())
	}
	// No exit snapshot should have been written for the old chat.
	if sessions, _ := sess.ListSessions(); len(sessions) != 0 {
		t.Fatalf("busy /new should not persist, but found %d sessions", len(sessions))
	}
}

// TestSlashNewRebuildsSaveManager verifies the new session's SaveManager is a
// fresh instance: after /new, a SaveAfterTurn on the cleared
// (system-prompt-only) session is a no-op (hasContent == false), so it must
// NOT create a new session entry. This proves the rebuilt manager did not
// carry the old turnSaveName and write to the old rolling snapshot.
func TestSlashNewRebuildsSaveManager(t *testing.T) {
	sess := makeTestSessionWithStore(t, "m")
	addTurn(t, sess, "q", "a")
	// Force the old manager to mint its rolling turn snapshot name.
	sess.SaveAfterTurn()

	m := newReadyChatModel(30, 90)
	m.session = sess
	m.waiting = false

	if !m.handleSlash("/new") {
		t.Fatal("/new must be handled")
	}

	countBefore, _ := sess.ListSessions()
	sess.SaveAfterTurn()
	countAfter, _ := sess.ListSessions()
	if len(countAfter) != len(countBefore) {
		t.Fatalf("rebuilt SaveManager wrote unexpectedly: %d -> %d sessions", len(countBefore), len(countAfter))
	}
}

// TestHandleSlashNewClassic verifies the classic REPL /new path persists and
// clears, returning (handled=true, exit=false).
func TestHandleSlashNewClassic(t *testing.T) {
	sess := makeTestSessionWithStore(t, "m")
	addTurn(t, sess, "classic-q", "classic-a")
	beforeID := sess.SessionID

	handled, exit, err := handleSlash("/new", sess, nil, false, nil)
	if err != nil {
		t.Fatalf("handleSlash /new error: %v", err)
	}
	if !handled {
		t.Fatal("/new must be handled in the classic REPL")
	}
	if exit {
		t.Fatal("/new must not exit the REPL")
	}
	if sess.SessionID == beforeID {
		t.Fatal("expected fresh SessionID after classic /new")
	}
	if sess.MessagesCount() != 1 {
		t.Fatalf("expected 1 message after classic /new, got %d", sess.MessagesCount())
	}
	if sessions, _ := sess.ListSessions(); len(sessions) == 0 {
		t.Fatal("classic /new did not persist the old session")
	}
}

// TestStoreGetter exposes the wired persistence backend.
func TestStoreGetter(t *testing.T) {
	t.Run("wired", func(t *testing.T) {
		sess := makeTestSessionWithStore(t, "m")
		if sess.Store() == nil {
			t.Fatal("Store() returned nil for a wired session")
		}
	})
	t.Run("nil", func(t *testing.T) {
		sess := &chat.Session{}
		if sess.Store() != nil {
			t.Fatalf("Store() should be nil for an unwired session, got %v", sess.Store())
		}
	})
}

// TestNewInHelpSurfaces verifies /new appears in all advertised help locations.
func TestNewInHelpSurfaces(t *testing.T) {
	// TUI help content.
	for _, sec := range tuiHelpCommands() {
		for _, it := range sec.items {
			if strings.Contains(it.key, "/new") {
				goto classic
			}
		}
	}
	t.Fatal("/new not found in tuiHelpCommands")
classic:
	// Classic REPL help content (catalog-driven sections).
	found := false
	for _, sec := range replHelpContent() {
		for _, it := range sec.items {
			if strings.Contains(it.key, "/new") {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("/new not found in replHelpContent")
	}
	// Inline/stderr path uses the same content structure.
	if !strings.Contains(renderReplHelpInline(), "/new") {
		t.Fatal("/new not found in renderReplHelpInline")
	}
}
