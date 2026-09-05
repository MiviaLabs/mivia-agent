package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// TestLoadWorktreeSessionPropagatesLiveLoadError covers the lerr!=nil branch
// LoadWorktreeSession takes when its live-fallback read
// (s.loadLiveContextSession) fails for a real reason instead of the ordinary
// "no live row" case. A turn-only worktree session (no snapshot row) always
// goes through this fallback, so dropping context_checkpoints - a table the
// live-session SELECT itself joins against in a subquery - forces that same
// SELECT to fail with a genuine "no such table" error rather than
// sql.ErrNoRows, proving the error is returned to the caller and not
// swallowed into a false ErrSessionNotFound.
func TestLoadWorktreeSessionPropagatesLiveLoadError(t *testing.T) {
	ctx := context.Background()
	store, principal := openContextTestStore(t)
	defer store.Close()
	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	seedLiveWorktreeSession(t, store, principal, instance, "turn-only-content")

	mustCoverageTrigger(t, store, `DROP TABLE context_checkpoints`)

	_, _, err := store.LoadWorktreeSession(ctx, principal, principal.SessionID, instance)
	if err == nil {
		t.Fatal("LoadWorktreeSession = nil, want the live-load failure propagated")
	}
	if errors.Is(err, contextstate.ErrSessionNotFound) {
		t.Fatalf("LoadWorktreeSession = %v, want a real load error, not a false ErrSessionNotFound", err)
	}
}

// TestLoadWorktreeSessionPropagatesResolveProjectionError covers the sibling
// err!=nil branch inside resolveProjection: once a worktree snapshot carries
// a live catalogSessionID (the "id is id" projection case), the loader must
// still surface a real failure from its own inner loadLiveContextSession call
// instead of silently falling back to the snapshot payload.
func TestLoadWorktreeSessionPropagatesResolveProjectionError(t *testing.T) {
	ctx := context.Background()
	store, principal := openContextTestStore(t)
	defer store.Close()
	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	binding := seedLiveWorktreeSession(t, store, principal, instance, "hello")
	if err := store.SaveSession(ctx, principal, principal.SessionID, []byte(`[{"role":"user","content":"hello"}]`), binding.Model, binding.Provider, 1, 1, 1, contextstate.SessionSaveOptions{SessionID: principal.SessionID, Worktree: instance.Worktree, WorktreeInstance: instance}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	mustCoverageTrigger(t, store, `DROP TABLE context_checkpoints`)

	_, _, err := store.LoadWorktreeSession(ctx, principal, principal.SessionID, instance)
	if err == nil {
		t.Fatal("LoadWorktreeSession = nil, want resolveProjection's load failure propagated")
	}
	if errors.Is(err, contextstate.ErrSessionNotFound) {
		t.Fatalf("LoadWorktreeSession = %v, want a real load error, not a false ErrSessionNotFound", err)
	}
}

// TestDeleteWorktreeSessionSnapshotPropagatesCatalogKeyDeleteError covers the
// DELETE FROM worktree_catalog_keys error branch inside
// DeleteWorktreeSessionSnapshot (distinct from the identically-shaped delete
// inside PruneWorktreeSessionSnapshots, already covered by
// TestWorktreeCatalogWriteFailureCoverage/catalog_prune): the same
// block-on-delete trigger, exercised through the single-name delete path
// instead of prune's loop, so a write failure on that side table cannot be
// reported as a successful delete.
func TestDeleteWorktreeSessionSnapshotPropagatesCatalogKeyDeleteError(t *testing.T) {
	store, principal, instance, _ := seedManagedCatalogKey(t)
	defer store.Close()
	mustCoverageTrigger(t, store, `CREATE TRIGGER block_delete_key_delete BEFORE DELETE ON worktree_catalog_keys BEGIN SELECT RAISE(ABORT,'blocked'); END`)

	err := store.DeleteWorktreeSessionSnapshot(context.Background(), principal, "managed", instance)
	if err == nil {
		t.Fatal("DeleteWorktreeSessionSnapshot = nil, want the catalog key delete failure propagated")
	}
}

// TestDeleteWorktreeSessionSnapshotPropagatesTombstoneBindingMismatch covers
// the non-ErrSessionNotFound branch of DeleteWorktreeSessionSnapshot's
// tombstoneContextSessionTx error check. A snapshot named "session" is saved
// under instanceB's catalog while a DIFFERENT live session, also named
// "session" (global uniqueness is workspace+subject+session_id, not
// instance-scoped), is live under instanceA. Deleting the instanceB snapshot
// finds the instanceB catalog key fine, but tombstoneContextSessionTx then
// finds the instanceA-bound live row under that same session id and refuses
// it (ErrWorktreeDeleted, via requireWorktreeSessionBinding) rather than
// ErrSessionNotFound - proving the delete does not swallow a real binding
// violation as if nothing were there to tombstone.
func TestDeleteWorktreeSessionSnapshotPropagatesTombstoneBindingMismatch(t *testing.T) {
	ctx := context.Background()
	store, principal := openContextTestStore(t)
	defer store.Close()
	instanceA := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1111111111111111"}
	instanceB := contextstate.WorktreeInstance{Worktree: "wt-b", ID: "wt_2222222222222222"}
	seedLiveWorktreeSession(t, store, principal, instanceA, "live-under-a")

	worktreeDirB := filepath.Join(t.TempDir(), "worktrees", instanceB.Worktree)
	if err := registerCleanupInstance(ctx, store, principal, instanceB, worktreeDirB); err != nil {
		t.Fatalf("registerCleanupInstance instanceB: %v", err)
	}
	if err := store.SaveSession(ctx, principal, principal.SessionID, []byte(`[{"role":"user","content":"snapshot-under-b"}]`), "model", "provider", 1, 1, 1, contextstate.SessionSaveOptions{Worktree: instanceB.Worktree, WorktreeInstance: instanceB}); err != nil {
		t.Fatalf("SaveSession under instanceB: %v", err)
	}

	err := store.DeleteWorktreeSessionSnapshot(ctx, principal, principal.SessionID, instanceB)
	if !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("DeleteWorktreeSessionSnapshot cross-instance binding mismatch = %v, want ErrWorktreeDeleted", err)
	}
}

// TestWorktreeSessionBindingRejectsInvalidPrincipal covers the p.Validate()
// error branch: a zero-value Principal fails identifier validation before
// any query runs.
func TestWorktreeSessionBindingRejectsInvalidPrincipal(t *testing.T) {
	store, _ := openContextTestStore(t)
	defer store.Close()
	_, found, err := store.WorktreeSessionBinding(context.Background(), contextstate.Principal{}, "session")
	if err == nil {
		t.Fatal("WorktreeSessionBinding with zero Principal = nil error, want a validation error")
	}
	if found {
		t.Fatal("WorktreeSessionBinding with zero Principal reported found=true")
	}
}

// TestWorktreeSessionBindingRejectsInvalidSessionID covers
// validateSessionCatalogName's error branch: an empty id is rejected before
// the lookup query runs.
func TestWorktreeSessionBindingRejectsInvalidSessionID(t *testing.T) {
	store, principal := openContextTestStore(t)
	defer store.Close()
	_, found, err := store.WorktreeSessionBinding(context.Background(), principal, "")
	if err == nil {
		t.Fatal("WorktreeSessionBinding with empty session id = nil error, want a validation error")
	}
	if found {
		t.Fatal("WorktreeSessionBinding with empty session id reported found=true")
	}
}

// TestWorktreeSessionBindingPropagatesQueryError covers the query's own
// err!=nil branch (distinct from sql.ErrNoRows): dropping worktree_instances,
// a table the binding lookup LEFT JOINs against, forces a genuine "no such
// table" failure that must reach the caller rather than being read as "not
// found".
func TestWorktreeSessionBindingPropagatesQueryError(t *testing.T) {
	store, principal := openContextTestStore(t)
	defer store.Close()
	mustCoverageTrigger(t, store, `DROP TABLE worktree_instances`)

	_, found, err := store.WorktreeSessionBinding(context.Background(), principal, principal.SessionID)
	if err == nil {
		t.Fatal("WorktreeSessionBinding = nil error, want the query failure propagated")
	}
	if found {
		t.Fatal("WorktreeSessionBinding reported found=true despite a query error")
	}
}

// TestWorktreeSessionBindingPlainSessionNotBound covers the
// !instanceID.Valid branch: a plain (non-worktree) live session has a NULL
// instance_id, so the binding lookup must report found=false with no error -
// the caller (the plain loader) owns that session, not the worktree one.
func TestWorktreeSessionBindingPlainSessionNotBound(t *testing.T) {
	store, principal := openContextTestStore(t)
	defer store.Close()
	binding := contextTestBinding(t)
	commitFirstMessageCheckpoint(t, store, principal, binding, "plain-session-content")

	info, found, err := store.WorktreeSessionBinding(context.Background(), principal, principal.SessionID)
	if err != nil {
		t.Fatalf("WorktreeSessionBinding plain session: %v", err)
	}
	if found {
		t.Fatalf("WorktreeSessionBinding plain session reported found=true, info=%+v", info)
	}
}

// TestWorktreeSessionBindingRefusesDeletedInstance covers the final guard:
// a live row bound to an instance whose worktree_instances row has since
// moved to 'deleted' must be refused (ErrWorktreeDeleted, found=false)
// rather than silently degrading to "unbound" - degrading here is exactly
// what let a worktree session resume detached from a worktree that no
// longer exists.
func TestWorktreeSessionBindingRefusesDeletedInstance(t *testing.T) {
	ctx := context.Background()
	store, principal := openContextTestStore(t)
	defer store.Close()
	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	seedLiveWorktreeSession(t, store, principal, instance, "will-be-deleted")

	if _, err := store.db.ExecContext(ctx, `UPDATE worktree_instances SET state=? WHERE workspace_id=? AND instance_id=?`, contextstate.WorktreeDeleted, principal.WorkspaceID, instance.ID); err != nil {
		t.Fatalf("mark instance deleted: %v", err)
	}

	info, found, err := store.WorktreeSessionBinding(ctx, principal, principal.SessionID)
	if !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("WorktreeSessionBinding against a deleted instance = %v, want ErrWorktreeDeleted", err)
	}
	if found {
		t.Fatalf("WorktreeSessionBinding against a deleted instance reported found=true, info=%+v", info)
	}
}
