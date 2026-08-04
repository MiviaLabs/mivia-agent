package storage

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// These tests drive the failure branches of the session-directory metadata
// paths added with the chat_session_dirs table (schema v5): every one of them
// needs the underlying SQL statement to fail, so each test deliberately breaks
// the target statement while leaving every earlier statement in the same
// transaction working. The dir table is dropped (rather than gated by a row
// trigger) because a DELETE that matches zero rows would never fire a trigger,
// and the orphan-sweep branch below is exactly the zero-row case.

// dropSessionDirsTable removes chat_session_dirs so any INSERT or DELETE
// against it fails with "no such table", deterministically and regardless of
// how many rows the statement would touch.
func dropSessionDirsTable(t *testing.T, store *SQLite) {
	t.Helper()
	if _, err := store.db.Exec(`DROP TABLE chat_session_dirs`); err != nil {
		t.Fatalf("drop chat_session_dirs: %v", err)
	}
}

// TestSaveSessionSnapshotInsertFailure covers chat_sessions.go's SaveSession
// transaction body error branch: when the snapshot INSERT itself fails, the
// error is returned and the transaction rolls back (no dir row leaks).
func TestSaveSessionSnapshotInsertFailure(t *testing.T) {
	store, principal := openContextTestStore(t)
	defer store.Close()
	ctx := context.Background()

	if _, err := store.db.Exec(`CREATE TRIGGER fail_chat_sessions_insert BEFORE INSERT ON chat_sessions BEGIN SELECT RAISE(ABORT, 'injected save failure'); END`); err != nil {
		t.Fatalf("create failing insert trigger: %v", err)
	}

	catalog := contextstate.SessionCatalog(store)
	if err := catalog.SaveSession(ctx, principal, "snap", []byte(`[{"role":"user"}]`), "model", "provider", 1, 2, 1, contextstate.SessionSaveOptions{Dir: "/repo", Worktree: "wt"}); err == nil {
		t.Fatal("SaveSession with failing snapshot INSERT unexpectedly succeeded")
	}

	// The transaction rolled back: no snapshot and no dir row may exist.
	var snapshots, dirs int
	if err := store.db.QueryRow(`SELECT count(*) FROM chat_sessions WHERE name=?`, "snap").Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT count(*) FROM chat_session_dirs WHERE name=?`, "snap").Scan(&dirs); err != nil {
		t.Fatal(err)
	}
	if snapshots != 0 || dirs != 0 {
		t.Fatalf("torn save leaked rows snapshots=%d dirs=%d", snapshots, dirs)
	}
}

// TestDeleteSnapshotOrphanDirSweepError covers the deleteContextSessionOrOrphanedAdmission
// dir-sweep error branch: a name that owns no snapshot and no context session
// still sweeps orphaned admission/dir rows, and a dir-sweep failure must
// surface instead of being swallowed.
func TestDeleteSnapshotOrphanDirSweepError(t *testing.T) {
	store, principal := openContextTestStore(t)
	defer store.Close()
	ctx := context.Background()

	// Seed an orphaned dir row for the ghost name so the sweep has a target,
	// then break the dir table so the sweep DELETE fails.
	if _, err := store.db.ExecContext(ctx, upsertSessionDirSQL, principal.WorkspaceID, principal.SubjectID, "ghost", "/orphan", ""); err != nil {
		t.Fatal(err)
	}
	dropSessionDirsTable(t, store)

	catalog := contextstate.SessionCatalog(store)
	if err := catalog.DeleteSessionSnapshot(ctx, principal, "ghost"); err == nil {
		t.Fatal("DeleteSessionSnapshot with failing dir sweep unexpectedly succeeded")
	}

	// The admission sweep ran before the dir sweep failed; a rollback of the
	// dir delete is not applicable here (both sweeps are standalone Execs), so
	// the orphaned admission row must still be gone.
	var admissions int
	if err := store.db.QueryRow(`SELECT count(*) FROM chat_session_admissions WHERE name=?`, "ghost").Scan(&admissions); err != nil {
		t.Fatal(err)
	}
	if admissions != 0 {
		t.Fatalf("admission sweep left %d orphan rows", admissions)
	}
}

// TestDeleteContextSessionDirDeleteError covers deleteCatalogContextSession's
// dir-delete error branch: the full retention lifecycle (tombstone, audit,
// tombstone row, admission reclaim) runs, and a dir-delete failure rolls the
// whole lifecycle back.
func TestDeleteContextSessionDirDeleteError(t *testing.T) {
	store, principal := openContextTestStore(t)
	defer store.Close()
	ctx := context.Background()

	binding := contextTestBinding(t)
	ensureContextSession(t, store, principal, binding)
	dropSessionDirsTable(t, store)

	catalog := contextstate.SessionCatalog(store)
	if err := catalog.DeleteSessionSnapshot(ctx, principal, principal.SessionID); err == nil {
		t.Fatal("DeleteSessionSnapshot with failing context dir delete unexpectedly succeeded")
	}

	// The lifecycle rolled back: the session must still be live.
	var tombstoned int
	if err := store.db.QueryRow(`SELECT tombstoned FROM context_sessions WHERE session_id=?`, principal.SessionID).Scan(&tombstoned); err != nil {
		t.Fatal(err)
	}
	if tombstoned != 0 {
		t.Fatalf("context session tombstoned=%d after rolled-back delete", tombstoned)
	}
}

// TestPruneSessionSnapshotsDirDeleteError covers PruneSessionSnapshots'
// dir-delete error branch: the snapshot is deleted inside the prune
// transaction, and a dir-delete failure must roll the whole prune back.
func TestPruneSessionSnapshotsDirDeleteError(t *testing.T) {
	store, principal := openContextTestStore(t)
	defer store.Close()
	ctx := context.Background()

	catalog := contextstate.SessionCatalog(store)
	if err := catalog.SaveSession(ctx, principal, "snap", []byte(`[{"role":"user"}]`), "model", "provider", 1, 2, 1, contextstate.SessionSaveOptions{Dir: "/repo", Worktree: "wt"}); err != nil {
		t.Fatal(err)
	}
	dropSessionDirsTable(t, store)

	if err := catalog.PruneSessionSnapshots(ctx, principal, []string{"snap"}); err == nil {
		t.Fatal("PruneSessionSnapshots with failing dir delete unexpectedly succeeded")
	}

	// The prune rolled back: the snapshot must still be present.
	var count int
	if err := store.db.QueryRow(`SELECT count(*) FROM chat_sessions WHERE name=?`, "snap").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("prune leaked the delete despite rollback (%d snapshot rows)", count)
	}
}

// TestEnsureSessionDirUpsertError covers EnsureSession's chat_session_dirs
// upsert error branch: a live session created with valid directory metadata
// whose dir record cannot be written must roll the whole creation back.
func TestEnsureSessionDirUpsertError(t *testing.T) {
	store, principal := openContextTestStore(t)
	defer store.Close()
	ctx := context.Background()

	binding := contextTestBinding(t)
	dropSessionDirsTable(t, store)

	if err := store.EnsureSession(ctx, contextstate.EnsureSessionRequest{Principal: principal, Binding: binding, Dir: "/repo", Worktree: "wt"}); err == nil {
		t.Fatal("EnsureSession with failing dir upsert unexpectedly succeeded")
	}

	// The creation rolled back: no session row may exist.
	var count int
	if err := store.db.QueryRow(`SELECT count(*) FROM context_sessions WHERE session_id=?`, principal.SessionID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("EnsureSession leaked a session row after rolled-back dir upsert (%d rows)", count)
	}
}

// TestEnsureSessionRejectsInvalidDirMetadata covers validateEnsureRequest's
// directory validation error branch: NUL bytes and oversized strings in either
// Dir or Worktree are rejected before any database work happens.
func TestEnsureSessionRejectsInvalidDirMetadata(t *testing.T) {
	store, principal := openContextTestStore(t)
	defer store.Close()
	ctx := context.Background()
	binding := contextTestBinding(t)

	invalid := []contextstate.EnsureSessionRequest{
		{Principal: principal, Binding: binding, Dir: "bad\x00dir"},
		{Principal: principal, Binding: binding, Worktree: "bad\x00wt"},
		{Principal: principal, Binding: binding, Dir: strings.Repeat("x", contextstate.MaxSessionDirBytes+1)},
		{Principal: principal, Binding: binding, Worktree: strings.Repeat("y", contextstate.MaxSessionDirBytes+1)},
	}
	for i, request := range invalid {
		if err := store.EnsureSession(ctx, request); !errors.Is(err, contextstate.ErrInvalidDTO) {
			t.Fatalf("EnsureSession invalid metadata case %d error = %v, want ErrInvalidDTO", i, err)
		}
	}

	// No session may have been created by any rejected request.
	var count int
	if err := store.db.QueryRow(`SELECT count(*) FROM context_sessions WHERE session_id=?`, principal.SessionID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rejected EnsureSession requests created %d session rows", count)
	}
}
