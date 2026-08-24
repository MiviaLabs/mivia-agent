package clichat

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

func wireTestContextSession(t *testing.T, store *storage.SQLite, res *config.Resolved) *chat.Session {
	t.Helper()
	sess := chat.NewSession(res, nullCompleter{})
	principal, err := contextstate.NewPrincipal("workspace", sess.SessionID, "subject")
	if err != nil {
		t.Fatal(err)
	}
	manager := &contextmgr.ContextManager{
		PreparationManager:  contextmgr.StructuralPreparationManager{},
		CheckpointPublisher: contextmgr.PreparationCommitter{Store: store},
		Enabled:             true,
	}
	if err := sess.SetContextManager(manager, principal); err != nil {
		t.Fatal(err)
	}
	if err := sess.SetContextStore(store); err != nil {
		t.Fatal(err)
	}
	return sess
}

// TestClearSlashSurvivesResume verifies that executing /clear in the REPL
// properly clears conversation history durably so that resuming the session
// by ID afterwards does not resurrect the pre-clear transcript.
func TestClearSlashSurvivesResume(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "clear_resume.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	res := &config.Resolved{ProviderName: "fake", Model: "model", Models: []string{"model"}}
	sess1 := wireTestContextSession(t, store, res)

	// Turn 1: user sends a message
	if _, err := sess1.SendUser(context.Background(), "first message before clear", io.Discard); err != nil {
		t.Fatal(err)
	}
	sess1.SaveAfterTurn()

	if len(sess1.MessagesCopy()) == 0 {
		t.Fatal("sess1 has no messages after send")
	}

	// User types /clear
	var termBuf bytes.Buffer
	term := NewTestTerminal(&termBuf)
	handled, exit, err := handleSlash("/clear", sess1, res, false, term)
	if err != nil {
		t.Fatalf("handleSlash(/clear) returned error: %v", err)
	}
	if !handled || exit {
		t.Fatalf("handleSlash(/clear) = (%v, %v), want (true, false)", handled, exit)
	}

	// In-memory messages should now be cleared
	if len(sess1.MessagesCopy()) != 0 {
		t.Fatalf("sess1 has %d messages immediately after /clear, want 0", len(sess1.MessagesCopy()))
	}

	// Process 2: mirror a fresh "mivia chat --session <id>" resume invocation
	sess2 := wireTestContextSession(t, store, res)

	// Resume the cleared session by ID
	if err := sess2.Load(sess1.SessionID); err != nil {
		t.Fatalf("sess2.Load(%s) failed: %v", sess1.SessionID, err)
	}

	// Messages after resume must remain empty (no resurrection of pre-clear turn)
	if len(sess2.MessagesCopy()) != 0 {
		t.Fatalf("sess2 resurrected %d messages after loading cleared session %s: %+v", len(sess2.MessagesCopy()), sess1.SessionID, sess2.MessagesCopy())
	}
	if sess2.SessionID != sess1.SessionID {
		t.Fatalf("sess2.SessionID = %q, want resumed ID %q", sess2.SessionID, sess1.SessionID)
	}
}
