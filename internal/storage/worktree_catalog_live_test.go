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
