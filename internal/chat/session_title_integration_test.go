package chat

import (
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// TestIntegrationListSessionsShowsSessionBeforeTurnCommits is the
// resume-picker regression: a brand-new session must appear in
// ListSessions/`/resume` as soon as its first user message is sent, not only
// after the whole turn (round trip to the provider) commits. Before
// markFirstUserTurn (internal/chat/session_title.go), a session with
// source_sequence=0 and no chat_sessions snapshot was invisible to
// ListSessions for as long as its first reply was in flight.
func TestIntegrationListSessionsShowsSessionBeforeTurnCommits(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	res := &config.Resolved{ProviderName: "fake", Model: "model", Models: []string{"model"}}
	comp := &blockingCompleter{name: "fake", start: make(chan struct{}, 1), allow: make(chan struct{})}
	run := NewSession(res, comp)
	setupTitleSessionContext(t, run, store)

	const opener = "please help me plan the launch"
	done := make(chan error, 1)
	go func() {
		_, sendErr := run.SendUser(t.Context(), opener, io.Discard)
		done <- sendErr
	}()

	select {
	case <-comp.start:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the in-flight turn to reach the provider")
	}

	infos, err := run.ListSessions()
	if err != nil {
		close(comp.allow)
		t.Fatal(err)
	}
	assertSessionTitle(t, infos, run.SessionID, opener)

	close(comp.allow)
	if sendErr := <-done; sendErr != nil {
		t.Fatalf("SendUser: %v", sendErr)
	}

	infos, err = run.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	assertSessionTitle(t, infos, run.SessionID, opener)
}

// TestIntegrationSetContextSessionTitle pins that an explicit rename
// (chat.Session.SetContextSessionTitle, what "mivia sessions rename" and the
// TUI's /title command both call) overrides the opener-derived title
// fillSessionTitles would otherwise use, and that it survives a resumed
// continuation under a different principal - the same identity as the
// session itself, not something a fresh resuming process would reset.
func TestIntegrationSetContextSessionTitle(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	res := &config.Resolved{ProviderName: "fake", Model: "model", Models: []string{"model"}}
	run1 := NewSession(res, &fakeCompleter{out: "answer"})
	setupTitleSessionContext(t, run1, store)
	if _, err := run1.SendUser(t.Context(), "opener message", io.Discard); err != nil {
		t.Fatal(err)
	}

	const renamed = "Project kickoff"
	if err := run1.SetContextSessionTitle(run1.SessionID, renamed); err != nil {
		t.Fatalf("SetContextSessionTitle: %v", err)
	}

	infos, err := run1.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	assertSessionTitle(t, infos, run1.SessionID, renamed)

	// The rename must stick even after a fresh process resumes the session
	// under its own, different principal.
	run2 := NewSession(res, &fakeCompleter{out: "answer"})
	setupTitleSessionContext(t, run2, store)
	if err := run2.Load(run1.SessionID); err != nil {
		t.Fatalf("resume load: %v", err)
	}
	if _, err := run2.SendUser(t.Context(), "continuation", io.Discard); err != nil {
		t.Fatal(err)
	}
	infos, err = run2.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	assertSessionTitle(t, infos, run1.SessionID, renamed)

	// Renaming an unknown session id must fail clearly, not silently no-op.
	if err := run1.SetContextSessionTitle("does-not-exist", "New title"); err == nil {
		t.Fatal("SetContextSessionTitle(does-not-exist): want an error, got nil")
	}
}

func assertSessionTitle(t *testing.T, infos []SessionInfo, sessionID, want string) {
	t.Helper()
	for _, info := range infos {
		if info.SessionID == sessionID {
			if info.Title != want {
				t.Fatalf("session %q title = %q, want %q", sessionID, info.Title, want)
			}
			return
		}
	}
	t.Fatalf("session %q missing from list: %#v", sessionID, infos)
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
