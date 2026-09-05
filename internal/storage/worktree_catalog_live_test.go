package storage

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// seedLiveWorktreeSession registers an active worktree instance, binds the
// principal's session to it, and commits one checkpoint - the exact state a
// TUI worktree session is in after a successful turn and NO /save or /clear:
// a context_sessions row with instance_id set and a checkpoint, but no
// chat_sessions snapshot and no worktree_catalog_keys row.
func seedLiveWorktreeSession(t *testing.T, store *SQLite, principal contextstate.Principal, instance contextstate.WorktreeInstance, opener string) contextstate.BindingRevision {
	t.Helper()
	ctx := context.Background()
	catalog := any(store).(contextstate.WorktreeSessionCatalog)
	dir := filepath.Join(t.TempDir(), "worktrees", instance.Worktree)
	if err := catalog.BeginWorktreeCreation(ctx, principal, instance, dir); err != nil {
		t.Fatalf("BeginWorktreeCreation: %v", err)
	}
	if err := catalog.RegisterWorktreeInstance(ctx, principal, instance, dir); err != nil {
		t.Fatalf("RegisterWorktreeInstance: %v", err)
	}
	binding := contextTestBinding(t)
	if err := store.EnsureSession(ctx, contextstate.EnsureSessionRequest{Principal: principal, Binding: binding, WorktreeInstance: instance}); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}
	commit, err := interleaveCommitRequest(principal, instance, contextstate.Revision{}, binding, "turn-1", opener)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(ctx, commit); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	return binding
}

// A worktree session that only ever completed turns has no snapshot row.
// Resuming it by its own id through the instance-scoped loader must serve the
// live checkpoint, exactly as the plain LoadSession does for plain sessions -
// otherwise every turn-only worktree session in the /resume picker fails with
// "session not found" (the reported live defect), and the plain loader must
// keep refusing the instance-bound row (fail-closed for unbound readers).
func TestLoadWorktreeSessionServesLiveTurnOnlySession(t *testing.T) {
	ctx := context.Background()
	store, principal := openContextTestStore(t)
	defer store.Close()
	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	seedLiveWorktreeSession(t, store, principal, instance, "hello-from-worktree")

	payload, info, err := store.LoadWorktreeSession(ctx, principal, principal.SessionID, instance)
	if err != nil {
		t.Fatalf("LoadWorktreeSession turn-only = %v, want the live checkpoint", err)
	}
	if !bytes.Contains(payload, []byte("hello-from-worktree")) {
		t.Fatalf("payload = %s, want the live checkpoint payload", payload)
	}
	if info.SessionID != principal.SessionID {
		t.Fatalf("info.SessionID = %q, want %q so the loader reclaims instead of forking", info.SessionID, principal.SessionID)
	}
	if info.WorktreeInstance != instance {
		t.Fatalf("info.WorktreeInstance = %+v, want %+v", info.WorktreeInstance, instance)
	}
	if _, _, err := store.LoadSession(ctx, principal, principal.SessionID); !errors.Is(err, contextstate.ErrSessionNotFound) {
		t.Fatalf("plain LoadSession of an instance-bound live row = %v, want ErrSessionNotFound (fail closed)", err)
	}
}

// A worktree snapshot saved under the session's own id must still carry that
// id back to the caller: without it loadContextCatalog sees a fresh id, never
// reclaims, and the next turn forks a second context_sessions row.
func TestLoadWorktreeSessionSnapshotKeepsLiveIdentity(t *testing.T) {
	ctx := context.Background()
	store, principal := openContextTestStore(t)
	defer store.Close()
	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	binding := seedLiveWorktreeSession(t, store, principal, instance, "hello")
	if err := store.SaveSession(ctx, principal, principal.SessionID, []byte(`[{"role":"user","content":"hello"}]`), binding.Model, binding.Provider, 1, 1, 1, contextstate.SessionSaveOptions{SessionID: principal.SessionID, Worktree: instance.Worktree, WorktreeInstance: instance}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	_, info, err := store.LoadWorktreeSession(ctx, principal, principal.SessionID, instance)
	if err != nil {
		t.Fatalf("LoadWorktreeSession snapshot = %v", err)
	}
	if info.SessionID != principal.SessionID {
		t.Fatalf("info.SessionID = %q, want %q", info.SessionID, principal.SessionID)
	}
}

// The worktree twin of TestLoadSession_ClearedSessionStaysEmptyDespiteOlderCheckpoint:
// a snapshot older than a /clear must not be served. The snapshot's revision
// against the live head is the ONLY thing that distinguishes "the snapshot is
// this session's only copy" (a failed turn's adoptFailedTurnSnapshot) from
// "a clear superseded it" - a loader that prefers the snapshot whenever the
// live payload is empty resurrects a conversation the user explicitly purged.
func TestLoadWorktreeSessionDoesNotResurrectClearedConversation(t *testing.T) {
	ctx := context.Background()
	store, principal := openContextTestStore(t)
	defer store.Close()
	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	binding := seedLiveWorktreeSession(t, store, principal, instance, "sensitive-pre-clear-content")
	if err := store.SaveSession(ctx, principal, principal.SessionID, []byte(`[{"role":"user","content":"sensitive-pre-clear-content"}]`), binding.Model, binding.Provider, 1, 1, 1, contextstate.SessionSaveOptions{SessionID: principal.SessionID, Worktree: instance.Worktree, WorktreeInstance: instance}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	clear := contextstate.AdvanceRequest{
		OperationID: "clear-1", Principal: principal, SessionID: principal.SessionID,
		Expected:        contextstate.Revision{Session: 1, Durable: 1, Source: 1},
		ExpectedBinding: binding, NewBinding: binding,
		NewSession:        2,
		NewDurable:        2,
		NewSourceSequence: 1,
		ClearActive:       true, Reason: "clear", WorktreeInstance: instance,
	}
	if err := store.Advance(ctx, clear); err != nil {
		t.Fatalf("Advance (clear): %v", err)
	}
	payload, _, err := store.LoadWorktreeSession(ctx, principal, principal.SessionID, instance)
	if err != nil {
		t.Fatalf("LoadWorktreeSession after clear: %v", err)
	}
	if bytes.Contains(payload, []byte("sensitive-pre-clear-content")) {
		t.Fatalf("payload = %s, want the cleared worktree conversation to stay gone", payload)
	}
}

// Deleting a session must leave nothing loadable. Before the live fallback
// existed the worktree delete could get away with removing only the snapshot;
// now that the live row is served, a delete that does not tombstone it hands
// the whole "deleted" conversation back on the next resume.
func TestDeleteWorktreeSessionSnapshotLeavesNothingLoadable(t *testing.T) {
	ctx := context.Background()
	store, principal := openContextTestStore(t)
	defer store.Close()
	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	binding := seedLiveWorktreeSession(t, store, principal, instance, "deleted-content")
	if err := store.SaveSession(ctx, principal, principal.SessionID, []byte(`[{"role":"user","content":"deleted-content"}]`), binding.Model, binding.Provider, 1, 1, 1, contextstate.SessionSaveOptions{SessionID: principal.SessionID, Worktree: instance.Worktree, WorktreeInstance: instance}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	if err := store.DeleteWorktreeSessionSnapshot(ctx, principal, principal.SessionID, instance); err != nil {
		t.Fatalf("DeleteWorktreeSessionSnapshot: %v", err)
	}
	if _, _, err := store.LoadWorktreeSession(ctx, principal, principal.SessionID, instance); !errors.Is(err, contextstate.ErrSessionNotFound) {
		t.Fatalf("load after delete = %v, want ErrSessionNotFound - the deleted conversation is still served", err)
	}
	var tombstoned int
	if err := store.db.QueryRow(`SELECT tombstoned FROM context_sessions WHERE workspace_id=? AND subject_id=? AND session_id=?`, principal.WorkspaceID, principal.SubjectID, principal.SessionID).Scan(&tombstoned); err != nil {
		t.Fatal(err)
	}
	if tombstoned != 1 {
		t.Fatal("delete left the live context row untombstoned: its payloads are never revoked and no audit/tombstone record is written")
	}
}

// The plain sibling of the same class: a live projection's snapshot delete
// reports success while LoadSession's own live fallback keeps serving it.
func TestDeleteSessionSnapshotLeavesNothingLoadableForLiveProjection(t *testing.T) {
	ctx := context.Background()
	store, principal := openContextTestStore(t)
	defer store.Close()
	binding := contextTestBinding(t)
	commitFirstMessageCheckpoint(t, store, principal, binding, "deleted-content")
	if err := store.SaveSession(ctx, principal, principal.SessionID, []byte(`[{"role":"user","content":"deleted-content"}]`), binding.Model, binding.Provider, 1, 1, 1, contextstate.SessionSaveOptions{SessionID: principal.SessionID}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	if err := store.DeleteSessionSnapshot(ctx, principal, principal.SessionID); err != nil {
		t.Fatalf("DeleteSessionSnapshot: %v", err)
	}
	if _, _, err := store.LoadSession(ctx, principal, principal.SessionID); !errors.Is(err, contextstate.ErrSessionNotFound) {
		t.Fatalf("load after delete = %v, want ErrSessionNotFound", err)
	}
}
