package chat

import (
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// TestIntegrationListSessionsTitlesForkedContinuation pins the fix for the
// session list showing raw session IDs: ListSessions fills the Title of every
// live context session with its first user message, including a forked
// continuation whose newest checkpoint re-states the original opener.
//
// Scenario: run1 commits "opener ..." under session S1. run2 (a fresh
// principal) loads S1 and continues with a different message. The catalog then
// holds two rows for one conversation; both rows must surface the same
// readable title derived from the conversation opener.
func TestIntegrationListSessionsTitlesForkedContinuation(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	const opener = "opener: build the session list fix"
	res := &config.Resolved{ProviderName: "fake", Model: "model", Models: []string{"model"}}

	run1 := NewSession(res, &fakeCompleter{out: "answer"})
	setupTitleSessionContext(t, run1, store)
	if _, err := run1.SendUser(t.Context(), opener, io.Discard); err != nil {
		t.Fatal(err)
	}

	run2 := NewSession(res, &fakeCompleter{out: "answer"})
	setupTitleSessionContext(t, run2, store)
	if err := run2.Load(run1.SessionID); err != nil {
		t.Fatalf("resume load: %v", err)
	}
	if !run2.LoadedContextSession() {
		t.Fatal("resume load must report a loaded context session")
	}
	if _, err := run2.SendUser(t.Context(), "continuation with different text", io.Discard); err != nil {
		t.Fatal(err)
	}

	infos, err := run2.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]SessionInfo, len(infos))
	for _, info := range infos {
		if info.SessionID != "" {
			byID[info.SessionID] = info
		}
	}
	for _, sid := range []string{run1.SessionID, run2.SessionID} {
		info, ok := byID[sid]
		if !ok {
			t.Fatalf("session %q missing from list: %#v", sid, infos)
		}
		if info.Title != opener {
			t.Fatalf("session %q title = %q, want the conversation opener %q", sid, info.Title, opener)
		}
	}
}

func setupTitleSessionContext(t *testing.T, session *Session, store *storage.SQLite) {
	t.Helper()
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
}

// TestIntegrationListSessionsTitlesLargeFork pins the fix for a forked
// continuation whose oldest checkpoint exceeds 64 KiB of canonical JSON: the
// whole checkpoint must be read, not a byte-sliced prefix, or the title is
// lost and the sidebar falls back to the raw session ID.
func TestIntegrationListSessionsTitlesLargeFork(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	const opener = "opener for the large fork"
	res := &config.Resolved{ProviderName: "fake", Model: "model", Models: []string{"model"}}

	run1 := NewSession(res, &fakeCompleter{out: strings.Repeat("x", 12*1024)})
	setupTitleSessionContext(t, run1, store)
	if _, err := run1.SendUser(t.Context(), opener, io.Discard); err != nil {
		t.Fatal(err)
	}
	// Grow the history past 64 KiB of canonical JSON.
	for i := 0; i < 7; i++ {
		if _, err := run1.SendUser(t.Context(), "more history", io.Discard); err != nil {
			t.Fatal(err)
		}
	}

	run2 := NewSession(res, &fakeCompleter{out: "answer"})
	setupTitleSessionContext(t, run2, store)
	if err := run2.Load(run1.SessionID); err != nil {
		t.Fatalf("resume load: %v", err)
	}
	if _, err := run2.SendUser(t.Context(), "continuation", io.Discard); err != nil {
		t.Fatal(err)
	}

	infos, err := run2.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range infos {
		if info.SessionID == run2.SessionID && info.Title != opener {
			t.Fatalf("forked session title = %q, want the opener %q", info.Title, opener)
		}
	}
}
