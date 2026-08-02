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
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// resyncSessionContext wires a real SQLite context store onto the session,
// mirroring the setup used across the context integration tests.
func resyncSessionContext(t *testing.T, name string) (*Session, *storage.SQLite, contextstate.Principal) {
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

func TestResyncContextHeadRestoresFromStore(t *testing.T) {
	session, store, principal := resyncSessionContext(t, "resync.db")
	for _, question := range []string{"first question", "second question"} {
		if _, err := session.SendUser(context.Background(), question, io.Discard); err != nil {
			t.Fatalf("send %q: %v", question, err)
		}
	}
	snapshot, err := store.Load(context.Background(), principal, principal.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if session.contextHead != snapshot.Revision {
		t.Fatalf("pre-corruption context head = %+v, want %+v", session.contextHead, snapshot.Revision)
	}
	if len(session.MessagesCopy()) == 0 {
		t.Fatal("expected in-memory messages after two turns")
	}

	// Corrupt the in-memory fence so only the durable store can restore it.
	session.contextPublishMu.Lock()
	defer session.contextPublishMu.Unlock()
	session.mu.Lock()
	session.contextHead = contextstate.Revision{}
	session.mu.Unlock()

	if err := session.resyncContextHead(); err != nil {
		t.Fatalf("resyncContextHead: %v", err)
	}
	if session.contextHead != snapshot.Revision {
		t.Fatalf("resynced context head = %+v, want %+v", session.contextHead, snapshot.Revision)
	}
	var want []provider.Message
	if err := contextstate.UnmarshalCanonical(snapshot.Active.ActiveContext, &want); err != nil {
		t.Fatalf("decode durable active context: %v", err)
	}
	if got := session.MessagesCopy(); !reflect.DeepEqual(got, want) {
		t.Fatalf("resynced messages = %#v, want %#v", got, want)
	}
}

func TestResyncContextHeadErrorsWhenNotConfigured(t *testing.T) {
	session := NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, &fakeCompleter{out: "answer"})
	if err := session.resyncContextHead(); err == nil {
		t.Fatal("resyncContextHead without a context store succeeded, want error")
	}
}

func TestResyncContextHeadErrorsOnBindingMismatch(t *testing.T) {
	session, _, _ := resyncSessionContext(t, "resync-binding.db")
	if _, err := session.SendUser(context.Background(), "question", io.Discard); err != nil {
		t.Fatal(err)
	}

	session.contextPublishMu.Lock()
	defer session.contextPublishMu.Unlock()
	// Drift the session binding away from the durable store's binding without
	// advancing the durable head, so the store snapshot still carries the old
	// generation.
	session.mu.Lock()
	session.binding.ModelGeneration++
	session.mu.Unlock()

	err := session.resyncContextHead()
	if !errors.Is(err, contextstate.ErrStaleBinding) {
		t.Fatalf("resyncContextHead error = %v, want wrapped %v", err, contextstate.ErrStaleBinding)
	}
}
