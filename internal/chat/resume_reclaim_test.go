package chat

import (
	"io"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// newContextSessionForTest wires a fresh Session to a durable context store
// under its own freshly-minted principal, mirroring what setupSessionContext
// does for a real `mivia chat` invocation.
func newContextSessionForTest(t *testing.T, store *storage.SQLite) *Session {
	t.Helper()
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
	return session
}

// TestResumeAcrossProcessesUpdatesSameSessionInPlace is the regression test
// for the "every resume forks a new session" bug: `mivia chat --session <id>`
// resumes in a brand-new process, which mints its OWN principal/session id
// before Load ever runs (setupSessionContext always does this - see
// internal/cli/chat_command.go and internal/cli/context_setup_session.go).
// Loading an existing session's history from that DIFFERENT principal must
// retarget this Session's identity at the resumed session so its next turn
// commits back onto the SAME catalog row - not a second, unrelated one.
func TestResumeAcrossProcessesUpdatesSameSessionInPlace(t *testing.T) {
	store, err := storage.OpenSQLite(t.TempDir() + "/context.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// "Process 1": create a session and commit its first turn.
	first := newContextSessionForTest(t, store)
	firstPrincipal := first.contextPrincipal
	if _, err := first.SendUser(t.Context(), "remember the number 7", io.Discard); err != nil {
		t.Fatal(err)
	}
	firstID := first.SessionID

	before, err := store.ListSessions(t.Context(), firstPrincipal)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 || before[0].SessionID != firstID {
		t.Fatalf("setup: expected exactly one session %q before resume, got %+v", firstID, before)
	}
	beforeTurnCount := before[0].TurnCount

	// "Process 2": a brand-new Session/principal, as a fresh `mivia chat`
	// invocation always mints, then resumes by --session firstID.
	second := newContextSessionForTest(t, store)
	if second.SessionID == firstID {
		t.Fatal("test setup: second session must mint a different id than first")
	}

	if err := second.Load(firstID); err != nil {
		t.Fatalf("Load(%q) = %v", firstID, err)
	}
	if second.SessionID != firstID {
		t.Fatalf("Load must retarget SessionID at the resumed session: got %q, want %q", second.SessionID, firstID)
	}
	if second.contextPrincipal.SessionID != firstID {
		t.Fatalf("Load must retarget contextPrincipal.SessionID at the resumed session: got %q, want %q", second.contextPrincipal.SessionID, firstID)
	}

	if _, err := second.SendUser(t.Context(), "what number?", io.Discard); err != nil {
		t.Fatal(err)
	}

	after, err := store.ListSessions(t.Context(), firstPrincipal)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 {
		t.Fatalf("resume must update the resumed session in place, not fork a new one; got %d sessions: %+v", len(after), after)
	}
	if after[0].SessionID != firstID {
		t.Fatalf("the single remaining session must still be %q, got %q", firstID, after[0].SessionID)
	}
	if after[0].TurnCount <= beforeTurnCount {
		t.Fatalf("resumed session's turn count must grow after the resumed turn: before=%d after=%d", beforeTurnCount, after[0].TurnCount)
	}
}

// TestResumeUnknownSessionStaysFailClosed pins that resuming a session id the
// catalog does not know about still fails the Load call and mints no session
// as a side effect - the fix for the fork bug must not weaken this.
func TestResumeUnknownSessionStaysFailClosed(t *testing.T) {
	store, err := storage.OpenSQLite(t.TempDir() + "/context.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	session := newContextSessionForTest(t, store)
	originalID := session.SessionID

	if err := session.Load("does-not-exist"); err == nil {
		t.Fatal("Load of an unknown session id must fail")
	}
	if session.SessionID != originalID {
		t.Fatalf("a failed Load must not change SessionID: got %q, want %q", session.SessionID, originalID)
	}

	infos, err := store.ListSessions(t.Context(), session.contextPrincipal)
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range infos {
		if info.SessionID == "does-not-exist" {
			t.Fatalf("a failed Load must not create a session under the requested id: %+v", infos)
		}
	}
}
