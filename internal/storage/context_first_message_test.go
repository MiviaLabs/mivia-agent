package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// commitFirstMessageCheckpoint commits one turn whose active context carries a
// canonical user message with the given opener, then returns the store.
func commitFirstMessageCheckpoint(t *testing.T, store *SQLite, principal contextstate.Principal, binding contextstate.BindingRevision, opener string) {
	t.Helper()
	if err := store.EnsureSession(context.Background(), contextstate.EnsureSessionRequest{Principal: principal, Binding: binding}); err != nil {
		t.Fatal(err)
	}
	active, err := contextstate.MarshalCanonical([]map[string]string{
		{"role": "user", "content": opener},
		{"role": "assistant", "content": "ok"},
	})
	if err != nil {
		t.Fatal(err)
	}
	expected := contextstate.Revision{Session: 0, Durable: 0, Source: 0}
	sequence := expected.Source + 1
	sourceID, err := contextstate.NewSourceID(principal.SessionID, sequence)
	if err != nil {
		t.Fatal(err)
	}
	rng, err := contextstate.NewSourceRange(sourceID, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	checkpointID, err := contextstate.NewCheckpointID(principal.SessionID, rng, "context-compact-v1", 1, binding.Model, "first-message-test")
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := contextstate.CheckpointRecord{
		ID: checkpointID, Revision: contextstate.Revision{Session: 1, Durable: 1, Source: sequence},
		Binding: binding, SourceRange: rng, ActiveContext: active,
		SummaryMetadata: []byte(`{"version":1}`), TurnID: 1,
	}
	event := contextstate.SourceEvent{ID: sourceID, Kind: "message", Role: "user", Provenance: "test", RedactionStatus: "metadata", Size: len(opener)}
	req, err := contextstate.NewCommitRequest(principal, principal.SessionID, expected, binding, []contextstate.SourceEvent{event}, checkpoint, active, binding, sequence)
	if err != nil {
		t.Fatal(err)
	}
	req.Fingerprint, err = contextstate.FingerprintCommitRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := req.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(context.Background(), req); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteFirstUserMessageFromOldestCheckpoint(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := contextstate.NewPrincipal("workspace", "sess", "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := contextstate.NewBindingRevision("provider", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	commitFirstMessageCheckpoint(t, store, principal, binding, "opener hello world")

	got, err := store.FirstUserMessage(context.Background(), principal, principal.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got != "opener hello world" {
		t.Fatalf("FirstUserMessage = %q, want %q", got, "opener hello world")
	}
}

func TestSQLiteFirstUserMessageScopedToSubject(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	owner, err := contextstate.NewPrincipal("workspace", "sess", "owner-subject")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := contextstate.NewBindingRevision("provider", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	commitFirstMessageCheckpoint(t, store, owner, binding, "private opener")

	other, err := contextstate.NewPrincipal("workspace", "sess", "other-subject")
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.FirstUserMessage(context.Background(), other, "sess")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("FirstUserMessage across subjects = %q, want empty", got)
	}
}

func TestSQLiteFirstUserMessageEmptyWithoutUserMessages(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := contextstate.NewPrincipal("workspace", "sess", "subject")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSession(context.Background(), contextstate.EnsureSessionRequest{Principal: principal, Binding: mustBinding(t)}); err != nil {
		t.Fatal(err)
	}
	got, err := store.FirstUserMessage(context.Background(), principal, principal.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("FirstUserMessage = %q, want empty", got)
	}
}
