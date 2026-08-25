package chat

import (
	"io"
	"reflect"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// newIdentityTestSession builds a context-enabled session wired to a real
// SQLite store, mirroring the fork_on_load_test.go setup pattern.
func newIdentityTestSession(t *testing.T, store *storage.SQLite) *Session {
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

// TestCrossProcessNamedSnapshotLoadStaysNamedCopy pins "id is id, name is
// name" for the named-snapshot direction: process A saves a named snapshot
// 'foo' while its live session id X exists, and a fresh session/principal
// (simulating process B) loads 'foo'. The stored chat_sessions row carries no
// session_id (the named save must never project X), so the load serves the
// saved copy with loadedContextSession false and never reclaims/takes over X.
func TestCrossProcessNamedSnapshotLoadStaysNamedCopy(t *testing.T) {
	store, err := storage.OpenSQLite(t.TempDir() + "/context.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Process A: a live context session X exists, and a named snapshot is
	// saved while X is live.
	processA := newIdentityTestSession(t, store)
	if _, err := processA.SendUser(t.Context(), "hello", io.Discard); err != nil {
		t.Fatal(err)
	}
	liveID := processA.SessionID
	processA.mu.Lock()
	saved := cloneContextMessages(processA.Messages)
	processA.mu.Unlock()
	if err := processA.Save("foo"); err != nil {
		t.Fatal(err)
	}

	// Process B: a fresh session with its own SessionID loads the named
	// snapshot.
	processB := newIdentityTestSession(t, store)
	processBID := processB.SessionID
	if processBID == liveID {
		t.Fatal("test setup: fresh session must have its own SessionID")
	}
	if err := processB.Load("foo"); err != nil {
		t.Fatal(err)
	}
	if processB.LoadedContextSession() {
		t.Fatal("named snapshot load must not report a live context session")
	}
	if processB.SessionID != processBID {
		t.Fatalf("named snapshot load must not reclaim the live session: SessionID %q -> %q", processBID, processB.SessionID)
	}
	// The saved copy is the canonical payload Save wrote; the loaded copy is
	// the same payload decoded, so compare against that normalized form (the
	// JSON round-trip normalizes time.Time's unexported clock state).
	payload, err := catalogMessages(saved)
	if err != nil {
		t.Fatal(err)
	}
	want, err := decodeCatalogMessages(payload)
	if err != nil {
		t.Fatal(err)
	}
	if got := processB.MessagesCopy(); !reflect.DeepEqual(got, want) {
		t.Fatalf("loaded messages are not the saved copy:\n got: %#v\nwant: %#v", got, want)
	}
}

// TestCrossProcessProjectionLoadReportsLiveSession pins the projection
// direction: a SaveAfterTurn-style save under s.SessionID (autoSaveContextSession
// calls Save(s.SessionID)) stamps the catalog row with the live id, so a fresh
// process loading by that id adopts the live session - loadedContextSession
// true and SessionID reclaimed to X ("id is id").
func TestCrossProcessProjectionLoadReportsLiveSession(t *testing.T) {
	store, err := storage.OpenSQLite(t.TempDir() + "/context.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Process A: a live context session X with a projection save under its own
	// id, exactly as autoSaveContextSession issues it.
	processA := newIdentityTestSession(t, store)
	if _, err := processA.SendUser(t.Context(), "hello", io.Discard); err != nil {
		t.Fatal(err)
	}
	liveID := processA.SessionID
	if err := processA.Save(liveID); err != nil {
		t.Fatal(err)
	}

	// Process B: a fresh session with its own SessionID loads by X's id.
	processB := newIdentityTestSession(t, store)
	if processB.SessionID == liveID {
		t.Fatal("test setup: fresh session must have its own SessionID")
	}
	if err := processB.Load(liveID); err != nil {
		t.Fatal(err)
	}
	if !processB.LoadedContextSession() {
		t.Fatal("projection load under the live id must report a live context session")
	}
	if processB.SessionID != liveID {
		t.Fatalf("projection load must adopt the live id: got %q, want %q", processB.SessionID, liveID)
	}
}

// TestDecodeCatalogMessagesToleratesNamedSummaryFromOlderCheckpoints pins a
// real resume failure: a session whose auto-compaction summary was durably
// committed WITH its wire Name still attached (a historical bug in an older
// build, before the commit path anonymized it - see commitContextTurn's
// summaryMessage.Name = "" in turn_finish.go) becomes permanently
// unresumable, since every load runs provider.ValidateToolPairing, which
// rejects a named user message. A fresh commit never produces this shape
// anymore, but a session that already carries one from before that fix must
// still be loadable - the summary's Name is host bookkeeping, never
// model-authored identity, so masking it on load (exactly like the
// core-memory frame's Name is already masked) costs nothing and self-heals
// old data instead of bricking the session forever.
func TestDecodeCatalogMessagesToleratesNamedSummaryFromOlderCheckpoints(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: "sys"},
		{Role: provider.RoleUser, Content: "question"},
		{Role: provider.RoleAssistant, Content: "answer"},
		{Role: provider.RoleUser, Name: agent.SummaryMessageName, Content: "[host-injected context summary of the omitted earlier conversation]"},
		{Role: provider.RoleUser, Content: "next question"},
	}
	data, err := contextstate.MarshalCanonical(msgs)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeCatalogMessages(data)
	if err != nil {
		t.Fatalf("decodeCatalogMessages: %v", err)
	}
	if len(decoded) != len(msgs) {
		t.Fatalf("decoded %d messages, want %d", len(decoded), len(msgs))
	}
	if decoded[3].Content != msgs[3].Content {
		t.Fatalf("summary content = %q, want preserved content %q", decoded[3].Content, msgs[3].Content)
	}
}
