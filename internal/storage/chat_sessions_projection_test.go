package storage

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

func mustBinding(t *testing.T) contextstate.BindingRevision {
	t.Helper()
	binding, err := contextstate.NewBindingRevision("provider", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

// TestLoadSession_ClearedSessionStaysEmptyDespiteOlderCheckpoint locks in the
// regression a bug audit caught before ship: an earlier version of
// liveContextSessionSQL fell back to the newest complete checkpoint whenever
// active_checkpoint_id was NULL, on the mistaken premise that NULL only ever
// meant "pointer lost track of a still-valid checkpoint." /clear
// (Advance with ClearActive: true, context_store.go's advanceActiveCheckpoint)
// also sets active_checkpoint_id=NULL, deliberately, while bumping
// session_revision/durable_revision past whatever checkpoint used to be
// active - a fallback that ignores that distinction resurrects a
// conversation the user explicitly cleared on the very next resume. This
// pins the correct behavior: after a clear, resume must stay empty even
// though an older complete checkpoint still exists on disk.
func TestLoadSession_ClearedSessionStaysEmptyDespiteOlderCheckpoint(t *testing.T) {
	ctx := context.Background()
	s, principal := openContextTestStore(t)
	defer s.Close()
	binding := contextTestBinding(t)
	ensureContextSession(t, s, principal, binding)
	commit := contextCommitRequest(t, principal, contextstate.Revision{}, binding, "commit-1", "sensitive-pre-clear-content")
	if err := s.Commit(ctx, commit); err != nil {
		t.Fatalf("commit: %v", err)
	}
	clear := contextstate.AdvanceRequest{
		OperationID: "clear-1", Principal: principal, SessionID: principal.SessionID,
		Expected:        contextstate.Revision{Session: commit.NewSession, Durable: commit.NewDurable, Source: commit.NewSourceSequence},
		ExpectedBinding: binding, NewBinding: binding,
		NewSession: commit.NewSession + 1, NewDurable: commit.NewDurable + 1, NewSourceSequence: commit.NewSourceSequence,
		ClearActive: true, Reason: "clear",
	}
	if err := s.Advance(ctx, clear); err != nil {
		t.Fatalf("Advance (clear): %v", err)
	}
	payload, _, err := s.LoadSession(ctx, principal, principal.SessionID)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if strings.Contains(string(payload), "sensitive-pre-clear-content") {
		t.Fatalf("payload = %s, want the cleared conversation to stay gone, not resurrected from the older checkpoint", payload)
	}
}

// TestLoadSession_NeverTurnedSnapshotSurvivesResume pins the fix for a
// regression a follow-up audit caught: making the live checkpoint
// unconditionally authoritative (closing the /clear-resurrection bug) also
// silently destroyed the ONLY copy of a session whose first-ever turn died
// before any preparation existed - adoptFailedTurnSnapshot
// (internal/chat/turn_finish.go) saves exactly this shape: a live session,
// zero completed checkpoints, and a real snapshot. Stamping the snapshot with
// the head revision at save time lets LoadSession tell "nothing has happened
// since this was saved" (safe to serve) apart from a genuinely stale
// pre-clear snapshot (TestLoadSession_ClearedSessionStaysEmptyDespiteOlderCheckpoint,
// TestLoadSession_StaleSnapshotDoesNotSurviveAClear below).
func TestLoadSession_NeverTurnedSnapshotSurvivesResume(t *testing.T) {
	ctx := context.Background()
	s, principal := openContextTestStore(t)
	defer s.Close()
	binding := contextTestBinding(t)
	ensureContextSession(t, s, principal, binding)
	// No Commit() ever happens - simulating a turn that died before any
	// preparation existed, so adoptFailedTurnSnapshot's SaveAfterTurn call is
	// the ONLY persistence for this session.
	headRevision := uint64(0)
	snapshotPayload := []byte(`[{"role":"system","content":"sys"},{"role":"user","content":"the question that died before preparation"}]`)
	if err := s.SaveSession(ctx, principal, principal.SessionID, snapshotPayload, "model", "provider", 1, 1, 2, contextstate.SessionSaveOptions{SessionID: principal.SessionID, SessionRevision: &headRevision}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	payload, info, err := s.LoadSession(ctx, principal, principal.SessionID)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if string(payload) != string(snapshotPayload) {
		t.Fatalf("payload = %s, want the only-ever-saved snapshot, not empty", payload)
	}
	if info.SessionID != principal.SessionID {
		t.Fatalf("info.SessionID = %q, want %q", info.SessionID, principal.SessionID)
	}
}

// TestLoadSession_UnstampedSnapshotStaysConservative is the regression guard
// for the OTHER direction: a snapshot saved with no SessionRevision (nil -
// either a pre-migration row, or any future caller that does not stamp one)
// must keep today's safe default of preferring the live (possibly empty)
// state, exactly as it did before this column existed. Only a KNOWN revision
// may override "prefer live".
func TestLoadSession_UnstampedSnapshotStaysConservative(t *testing.T) {
	ctx := context.Background()
	s, principal := openContextTestStore(t)
	defer s.Close()
	binding := contextTestBinding(t)
	ensureContextSession(t, s, principal, binding)
	snapshotPayload := []byte(`[{"role":"system","content":"sys"},{"role":"user","content":"unstamped legacy snapshot"}]`)
	if err := s.SaveSession(ctx, principal, principal.SessionID, snapshotPayload, "model", "provider", 1, 1, 2, contextstate.SessionSaveOptions{SessionID: principal.SessionID}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	payload, _, err := s.LoadSession(ctx, principal, principal.SessionID)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if string(payload) != string(emptyContextPayload) {
		t.Fatalf("payload = %s, want emptyContextPayload for an unstamped snapshot with no completed checkpoint", payload)
	}
}

// TestLoadSession_StaleSnapshotDoesNotSurviveAClear extends the /clear
// regression coverage to a session with NO checkpoint at all (only ever
// saved via a snapshot): a stamped-but-stale snapshot (saved before a
// /clear advanced the head) must not resurrect, even though there is no
// checkpoint to prefer instead - the revision comparison alone must catch it.
func TestLoadSession_StaleSnapshotDoesNotSurviveAClear(t *testing.T) {
	ctx := context.Background()
	s, principal := openContextTestStore(t)
	defer s.Close()
	binding := contextTestBinding(t)
	ensureContextSession(t, s, principal, binding)
	headRevision := uint64(0)
	snapshotPayload := []byte(`[{"role":"system","content":"sys"},{"role":"user","content":"pre-clear content that must not resurrect"}]`)
	if err := s.SaveSession(ctx, principal, principal.SessionID, snapshotPayload, "model", "provider", 1, 1, 2, contextstate.SessionSaveOptions{SessionID: principal.SessionID, SessionRevision: &headRevision}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	clear := contextstate.AdvanceRequest{
		OperationID: "clear-1", Principal: principal, SessionID: principal.SessionID,
		Expected:        contextstate.Revision{},
		ExpectedBinding: binding, NewBinding: binding,
		NewSession: 1, NewDurable: 1, NewSourceSequence: 0,
		ClearActive: true, Reason: "clear",
	}
	if err := s.Advance(ctx, clear); err != nil {
		t.Fatalf("Advance (clear): %v", err)
	}
	payload, _, err := s.LoadSession(ctx, principal, principal.SessionID)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if strings.Contains(string(payload), "pre-clear content that must not resurrect") {
		t.Fatalf("payload = %s, want the cleared conversation to stay gone", payload)
	}
}

// TestLoadSession_PrefersSnapshotWithMoreMessagesThanCheckpoint verifies that a
// completed live checkpoint is authoritative over any snapshot in chat_sessions,
// even if the snapshot has more messages (e.g. following /clear or compaction).
func TestLoadSession_PrefersSnapshotWithMoreMessagesThanCheckpoint(t *testing.T) {
	ctx := context.Background()
	s, principal := openContextTestStore(t)
	defer s.Close()
	binding := contextTestBinding(t)
	// commitFirstMessageCheckpoint commits a 2-message checkpoint (user+assistant).
	commitFirstMessageCheckpoint(t, s, principal, binding, "hello")
	// The snapshot reflects an older/stale or pre-clear/pre-compaction state with more messages.
	snapshotPayload := []byte(`[{"role":"user","content":"hello"},{"role":"assistant","content":"ok"},{"role":"user","content":"extra snapshot message"}]`)
	if err := s.SaveSession(ctx, principal, principal.SessionID, snapshotPayload, binding.Model, binding.Provider, 2, 1, 3, contextstate.SessionSaveOptions{SessionID: principal.SessionID}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	payload, info, err := s.LoadSession(ctx, principal, principal.SessionID)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if !bytes.Contains(payload, []byte("hello")) || bytes.Contains(payload, []byte("extra snapshot message")) {
		t.Fatalf("payload = %s, want live checkpoint payload, not stale snapshot", payload)
	}
	if info.SessionID != principal.SessionID {
		t.Fatalf("info.SessionID = %q, want the live identity %q preserved for takeover", info.SessionID, principal.SessionID)
	}
}

// TestLoadSession_KeepsCheckpointWhenSnapshotHasNoMoreMessages verifies that
// a same-or-fewer-message snapshot also never shadows the live checkpoint.
func TestLoadSession_KeepsCheckpointWhenSnapshotHasNoMoreMessages(t *testing.T) {
	ctx := context.Background()
	s, principal := openContextTestStore(t)
	defer s.Close()
	binding := contextTestBinding(t)
	commitFirstMessageCheckpoint(t, s, principal, binding, "hello")
	snapshotPayload := []byte(`[{"role":"user","content":"stale snapshot"}]`)
	if err := s.SaveSession(ctx, principal, principal.SessionID, snapshotPayload, binding.Model, binding.Provider, 1, 1, 1, contextstate.SessionSaveOptions{SessionID: principal.SessionID}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	payload, info, err := s.LoadSession(ctx, principal, principal.SessionID)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if !bytes.Contains(payload, []byte("hello")) {
		t.Fatalf("payload = %s, want the checkpoint payload (contains \"hello\"), not the smaller snapshot", payload)
	}
	if info.SessionID != principal.SessionID {
		t.Fatalf("info.SessionID = %q, want %q", info.SessionID, principal.SessionID)
	}
}

// TestLiveContextSession_NoCompleteCheckpointServesEmptyPayload is the
// regression guard: with no complete checkpoint at all, resume must still
// serve the empty-context default exactly as before F1.
func TestLiveContextSession_NoCompleteCheckpointServesEmptyPayload(t *testing.T) {
	ctx := context.Background()
	s, principal := openContextTestStore(t)
	defer s.Close()
	binding := contextTestBinding(t)
	ensureContextSession(t, s, principal, binding)
	payload, info, err := s.LoadSession(ctx, principal, principal.SessionID)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if info.SessionID != principal.SessionID {
		t.Fatalf("info.SessionID = %q, want %q", info.SessionID, principal.SessionID)
	}
	if string(payload) != string(emptyContextPayload) {
		t.Fatalf("payload = %s, want emptyContextPayload", payload)
	}
}
