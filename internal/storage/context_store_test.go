package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

func TestEnsureSessionCreatesZeroRevision(t *testing.T) {
	ctx := context.Background()
	s, principal := openContextTestStore(t)
	defer s.Close()
	binding := contextTestBinding(t)
	if err := s.EnsureSession(ctx, contextstate.EnsureSessionRequest{Principal: principal, Binding: binding}); err != nil {
		t.Fatalf("ensure session: %v", err)
	}
	snapshot, err := s.Load(ctx, principal, principal.SessionID)
	if err != nil {
		t.Fatalf("load initial session: %v", err)
	}
	if snapshot.Revision != (contextstate.Revision{}) || snapshot.Binding != binding || snapshot.Active.ID.SessionID != "" {
		t.Fatalf("initial snapshot = %+v", snapshot)
	}
}

func TestCommitRejectsStaleRevision(t *testing.T) {
	ctx := context.Background()
	s, principal := openContextTestStore(t)
	defer s.Close()
	binding := contextTestBinding(t)
	ensureContextSession(t, s, principal, binding)
	first := contextCommitRequest(t, principal, contextstate.Revision{}, binding, "commit-1", "first")
	if err := s.Commit(ctx, first); err != nil {
		t.Fatalf("first commit: %v", err)
	}
	stale := contextCommitRequest(t, principal, contextstate.Revision{}, binding, "commit-2", "stale")
	if err := s.Commit(ctx, stale); !errors.Is(err, contextstate.ErrStaleRevision) {
		t.Fatalf("stale commit error = %v, want ErrStaleRevision", err)
	}
}

func TestCommitIdempotencyAndConflict(t *testing.T) {
	ctx := context.Background()
	s, principal := openContextTestStore(t)
	defer s.Close()
	binding := contextTestBinding(t)
	ensureContextSession(t, s, principal, binding)
	request := contextCommitRequest(t, principal, contextstate.Revision{}, binding, "commit-1", "first")
	if err := s.Commit(ctx, request); err != nil {
		t.Fatalf("first commit: %v", err)
	}
	if err := s.Commit(ctx, request); err != nil {
		t.Fatalf("idempotent commit: %v", err)
	}
	var sourceCount, checkpointCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM context_source_events`).Scan(&sourceCount); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM context_checkpoints`).Scan(&checkpointCount); err != nil {
		t.Fatal(err)
	}
	if sourceCount != 1 || checkpointCount != 1 {
		t.Fatalf("idempotent counts source=%d checkpoint=%d", sourceCount, checkpointCount)
	}
	conflict := contextCommitRequest(t, principal, contextstate.Revision{}, binding, "commit-1", "different")
	if err := s.Commit(ctx, conflict); !errors.Is(err, contextstate.ErrCheckpointConflict) {
		t.Fatalf("same-key conflict error = %v, want ErrCheckpointConflict", err)
	}
}

func TestCommitCASAcrossSQLiteHandles(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "context.db")
	s, principal := openContextStoreAt(t, path)
	defer s.Close()
	other, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	binding := contextTestBinding(t)
	ensureContextSession(t, s, principal, binding)
	first := contextCommitRequest(t, principal, contextstate.Revision{}, binding, "commit-1", "first")
	if err := s.Commit(ctx, first); err != nil {
		t.Fatalf("first handle commit: %v", err)
	}
	stale := contextCommitRequest(t, principal, contextstate.Revision{}, binding, "commit-2", "second")
	if err := other.Commit(ctx, stale); !errors.Is(err, contextstate.ErrStaleRevision) {
		t.Fatalf("second handle stale error = %v, want ErrStaleRevision", err)
	}
}

func TestAdvanceUpdatesHeadWithCAS(t *testing.T) {
	ctx := context.Background()
	s, principal := openContextTestStore(t)
	defer s.Close()
	binding := contextTestBinding(t)
	ensureContextSession(t, s, principal, binding)
	commit := contextCommitRequest(t, principal, contextstate.Revision{}, binding, "commit-1", "first")
	if err := s.Commit(ctx, commit); err != nil {
		t.Fatalf("commit: %v", err)
	}
	advance := contextstate.AdvanceRequest{
		OperationID: "advance-1", Principal: principal, SessionID: principal.SessionID,
		Expected: contextstate.Revision{Session: 1, Durable: 1, Source: 1}, ExpectedBinding: binding,
		NewSession: 2, NewDurable: 2, NewSourceSequence: 1, NewBinding: binding,
		ClearActive: true, Reason: "clear active context",
	}
	if err := s.Advance(ctx, advance); err != nil {
		t.Fatalf("advance: %v", err)
	}
	if err := s.Advance(ctx, advance); err != nil {
		t.Fatalf("idempotent advance: %v", err)
	}
	snapshot, err := s.Load(ctx, principal, principal.SessionID)
	if err != nil {
		t.Fatalf("load advanced session: %v", err)
	}
	if snapshot.Revision != (contextstate.Revision{Session: 2, Durable: 2, Source: 1}) || snapshot.Active.ID.SessionID != "" {
		t.Fatalf("advanced snapshot = %+v", snapshot)
	}
	stale := advance
	stale.OperationID = "advance-2"
	if err := s.Advance(ctx, stale); !errors.Is(err, contextstate.ErrStaleRevision) {
		t.Fatalf("stale advance error = %v, want ErrStaleRevision", err)
	}
}

// TestAdvancePropagatesRestampProjectionFailure covers Advance's own
// restampProjectionForBindingAdvance error check: a binding advance
// (ClearActive: false, so the restamp UPDATE actually runs) whose chat_sessions
// UPDATE is blocked by a trigger must fail the whole Advance, not commit a
// context head that has moved out of step with the projection that mirrors
// it.
func TestAdvancePropagatesRestampProjectionFailure(t *testing.T) {
	ctx := context.Background()
	s, principal := openContextTestStore(t)
	defer s.Close()
	binding := contextTestBinding(t)
	ensureContextSession(t, s, principal, binding)
	commit := contextCommitRequest(t, principal, contextstate.Revision{}, binding, "commit-1", "first")
	if err := s.Commit(ctx, commit); err != nil {
		t.Fatalf("commit: %v", err)
	}
	seedRevision := uint64(1)
	if err := s.SaveSession(ctx, principal, principal.SessionID, []byte(`[{}]`), binding.Provider, binding.Model, 1, 1, 1, contextstate.SessionSaveOptions{SessionID: principal.SessionID, SessionRevision: &seedRevision}); err != nil {
		t.Fatalf("seed chat_sessions snapshot: %v", err)
	}
	mustCoverageTrigger(t, s, `CREATE TRIGGER block_chat_sessions_restamp BEFORE UPDATE ON chat_sessions BEGIN SELECT RAISE(ABORT,'injected restamp failure'); END`)

	newBinding := binding
	newBinding.Generation++
	advance := contextstate.AdvanceRequest{
		OperationID: "advance-restamp-1", Principal: principal, SessionID: principal.SessionID,
		Expected: contextstate.Revision{Session: 1, Durable: 1, Source: 1}, ExpectedBinding: binding,
		NewSession: 2, NewDurable: 2, NewSourceSequence: 1, NewBinding: newBinding,
		ClearActive: false, Reason: "model switch",
	}
	if err := s.Advance(ctx, advance); err == nil {
		t.Fatal("Advance accepted a binding switch despite the projection restamp failing")
	}
	snapshot, err := s.Load(ctx, principal, principal.SessionID)
	if err != nil {
		t.Fatalf("load after failed advance: %v", err)
	}
	if snapshot.Revision.Session != 1 {
		t.Fatalf("session_revision = %d after a rolled-back advance, want 1 (unchanged)", snapshot.Revision.Session)
	}
}

func TestRecoverySelectsCommittedPointer(t *testing.T) {
	ctx := context.Background()
	s, principal := openContextTestStore(t)
	defer s.Close()
	binding := contextTestBinding(t)
	ensureContextSession(t, s, principal, binding)
	commit := contextCommitRequest(t, principal, contextstate.Revision{}, binding, "commit-1", "committed")
	if err := s.Commit(ctx, commit); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE context_sessions SET active_checkpoint_id=NULL WHERE session_id=?`, principal.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO context_checkpoints(checkpoint_id,workspace_id,session_id,subject_id,source_start,source_end,algorithm,schema_version,summary_model,operation_id,idempotency_key,session_revision,durable_revision,binding_generation,turn_id,summary_metadata,active_context,content_fingerprint,complete) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,0)`, "ctxc_incomplete", principal.WorkspaceID, principal.SessionID, principal.SubjectID, 1, 1, "context-compact-v1", 1, binding.Model, "incomplete", "incomplete", 2, 2, binding.Generation, 2, []byte(`{}`), []byte(`{"state":"incomplete"}`), "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.Load(ctx, principal, principal.SessionID)
	if err != nil {
		t.Fatalf("load after incomplete row: %v", err)
	}
	if snapshot.Active.TurnID != commit.Checkpoint.TurnID || !snapshot.Active.Complete {
		t.Fatalf("recovered active checkpoint = %+v", snapshot.Active)
	}
}

func ensureContextSession(t *testing.T, s *SQLite, principal contextstate.Principal, binding contextstate.BindingRevision) {
	t.Helper()
	if err := s.EnsureSession(context.Background(), contextstate.EnsureSessionRequest{Principal: principal, Binding: binding}); err != nil {
		t.Fatal(err)
	}
}

func contextTestBinding(t *testing.T) contextstate.BindingRevision {
	t.Helper()
	binding, err := contextstate.NewBindingRevision("provider", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func contextCommitRequest(t *testing.T, principal contextstate.Principal, expected contextstate.Revision, binding contextstate.BindingRevision, key, state string) contextstate.CommitRequest {
	t.Helper()
	sequence := expected.Source + 1
	sourceID, err := contextstate.NewSourceID(principal.SessionID, sequence)
	if err != nil {
		t.Fatal(err)
	}
	rng, err := contextstate.NewSourceRange(sourceID, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	checkpointID, err := contextstate.NewCheckpointID(principal.SessionID, rng, "context-compact-v1", 1, binding.Model, key)
	if err != nil {
		t.Fatal(err)
	}
	active := []byte(`{"state":"` + state + `"}`)
	checkpoint := contextstate.CheckpointRecord{
		ID: checkpointID, Revision: contextstate.Revision{Session: expected.Session + 1, Durable: expected.Durable + 1, Source: sequence},
		Binding: binding, SourceRange: rng, ActiveContext: active, SummaryMetadata: []byte(`{"version":1}`), TurnID: expected.Session + 1,
	}
	event := contextstate.SourceEvent{ID: sourceID, Kind: "message", Role: "user", Provenance: "test", RedactionStatus: "metadata", Size: len(state)}
	request, err := contextstate.NewCommitRequest(principal, principal.SessionID, expected, binding, []contextstate.SourceEvent{event}, checkpoint, active, binding, expected.Session+1)
	if err != nil {
		t.Fatal(err)
	}
	return request
}
