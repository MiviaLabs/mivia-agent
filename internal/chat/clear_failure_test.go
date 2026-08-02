package chat

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// clearFailureSession wires a real SQLite context store onto a session,
// mirroring the construction pattern used across the context integration
// tests (context_storage_test.go, resync_test.go).
func clearFailureSession(t *testing.T, name string) (*Session, *storage.SQLite, contextstate.Principal) {
	t.Helper()
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
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
	return session, store, principal
}

// INV-AG-35: Clear must advance the durable head BEFORE mutating in-memory
// Messages. When the durable CAS rejects a stale expected revision, Clear
// returns an error and must leave the conversation and the in-memory head
// exactly as they were.
func TestClearPreservesConversationOnStaleRevision(t *testing.T) {
	session, store, principal := clearFailureSession(t, "clear-stale.db")
	if _, err := session.SendUser(context.Background(), "first question", io.Discard); err != nil {
		t.Fatal(err)
	}
	durableHead, err := store.Load(context.Background(), principal, principal.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if durableHead.Revision != (contextstate.Revision{Session: 1, Durable: 1, Source: 2}) {
		t.Fatalf("durable head after one turn = %+v, want {1 1 2}", durableHead.Revision)
	}
	preClear := session.MessagesCopy()
	if len(preClear) != 2 {
		t.Fatalf("pre-clear messages = %d, want 2 (user + assistant)", len(preClear))
	}

	// Drift the in-memory fence away from the durable head so the CAS inside
	// resetSystem rejects the clear. The bump happens under the lock, same as
	// the corruption pattern in resync_test.go.
	session.mu.Lock()
	session.contextHead.Session++
	session.contextHead.Durable++
	session.mu.Unlock()

	if err := session.Clear(); err == nil {
		t.Fatal("Clear with a stale context head succeeded, want CAS failure")
	} else if !errors.Is(err, contextstate.ErrStaleRevision) {
		t.Fatalf("Clear error = %v, want wrapped %v", err, contextstate.ErrStaleRevision)
	}

	// A refused clear must not touch the in-memory conversation...
	if got := session.MessagesCopy(); !reflect.DeepEqual(got, preClear) {
		t.Fatalf("messages after failed Clear = %#v, want %#v", got, preClear)
	}
	// ...nor the durable head.
	after, err := store.Load(context.Background(), principal, principal.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != durableHead.Revision {
		t.Fatalf("durable head after failed Clear = %+v, want %+v", after.Revision, durableHead.Revision)
	}

	// Undo the test-injected drift and verify the session keeps working: the
	// failed Clear must not have wedged the session or poisoned its token.
	session.mu.Lock()
	session.contextHead = durableHead.Revision
	session.mu.Unlock()

	if _, err := session.SendUser(context.Background(), "second question", io.Discard); err != nil {
		t.Fatalf("turn after failed Clear: %v", err)
	}
	if got := session.MessagesCount(); got != 4 {
		t.Fatalf("messages after second turn = %d, want 4", got)
	}
}

// advanceFailStore wraps a real SQLite context store and injects a failure
// into Advance (the durable CAS used by Clear). All other operations delegate
// to the underlying store.
type advanceFailStore struct {
	contextstate.Store
	fail bool
}

func (s *advanceFailStore) Advance(ctx context.Context, request contextstate.AdvanceRequest) error {
	if s.fail {
		return errors.New("injected advance failure")
	}
	return s.Store.Advance(ctx, request)
}

var _ contextstate.Store = (*advanceFailStore)(nil)

// INV-AG-35: a refused Advance must never destroy in-memory state. Clear
// returns the store error and the conversation stays intact; once the store
// is healthy again, a real Clear succeeds and drops the history.
func TestClearPreservesMessagesWhenAdvanceFails(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "clear-advance.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	wrapped := &advanceFailStore{Store: store}
	session := NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, &fakeCompleter{out: "answer"})
	principal, err := contextstate.NewPrincipal("workspace", session.SessionID, "subject")
	if err != nil {
		t.Fatal(err)
	}
	manager := &contextmgr.ContextManager{
		PreparationManager:  contextmgr.StructuralPreparationManager{},
		CheckpointPublisher: contextmgr.PreparationCommitter{Store: wrapped},
		Enabled:             true,
	}
	if err := session.SetContextManager(manager, principal); err != nil {
		t.Fatal(err)
	}
	if err := session.SetContextStore(wrapped); err != nil {
		t.Fatal(err)
	}
	if _, err := session.SendUser(context.Background(), "first question", io.Discard); err != nil {
		t.Fatal(err)
	}
	preClear := session.MessagesCopy()
	if len(preClear) != 2 {
		t.Fatalf("pre-clear messages = %d, want 2 (user + assistant)", len(preClear))
	}
	durableHead, err := store.Load(context.Background(), principal, principal.SessionID)
	if err != nil {
		t.Fatal(err)
	}

	// Make the durable CAS refuse the clear's advance.
	wrapped.fail = true
	if err := session.Clear(); err == nil {
		t.Fatal("Clear with failing Advance succeeded, want error")
	}
	if got := session.MessagesCopy(); !reflect.DeepEqual(got, preClear) {
		t.Fatalf("messages after failed Clear = %#v, want %#v", got, preClear)
	}
	after, err := store.Load(context.Background(), principal, principal.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != durableHead.Revision {
		t.Fatalf("durable head after failed Clear = %+v, want %+v", after.Revision, durableHead.Revision)
	}

	// Remove the wrapper and verify a real Clear advances the head and drops
	// the in-memory history.
	if err := session.SetContextStore(store); err != nil {
		t.Fatal(err)
	}
	if err := session.Clear(); err != nil {
		t.Fatalf("Clear with healthy store: %v", err)
	}
	if got := session.MessagesCount(); got != 0 {
		t.Fatalf("messages after real Clear = %d, want 0", got)
	}
	final, err := store.Load(context.Background(), principal, principal.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Revision != (contextstate.Revision{Session: 2, Durable: 2, Source: 2}) {
		t.Fatalf("revision after real Clear = %+v, want {2 2 2}", final.Revision)
	}
	if final.Active.ID.SessionID != "" {
		t.Fatalf("real Clear left an active checkpoint: %+v", final.Active)
	}
}
