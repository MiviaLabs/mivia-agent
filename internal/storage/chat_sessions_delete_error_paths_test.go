package storage

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// The delete path's read failures and its last rollback arms.
//
// chat_sessions_delete_paths_test.go covers the writes. What is left is
// every point where the delete READS the store to decide what to do next:
// resolving a snapshot by its stored session_id, and loading the context
// session's revision. A read that fails must stop the delete, because the
// fallback the delete would otherwise take - "no snapshot matched, so
// tombstone the context session of that name" - silently no-ops for a name
// that is not context-backed, and the caller is told the session is gone
// while the row and its project association stay behind forever.

// seedSnapshotWithDivergedSessionID writes a snapshot whose session_id
// column does not equal its catalog name, the legacy shape the desktop
// app's delete-by-session_id path exists for.
func seedSnapshotWithDivergedSessionID(t *testing.T, s *SQLite, principal contextstate.Principal, name, sessionID string) {
	t.Helper()
	ctx := context.Background()
	if err := s.SaveSession(ctx, principal, name, []byte(`[{}]`), "model", "provider", 1, 1, 1, contextstate.SessionSaveOptions{Dir: "/tmp/project"}); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE chat_sessions SET session_id=? WHERE workspace_id=? AND subject_id=? AND name=?`,
		sessionID, principal.WorkspaceID, principal.SubjectID, name); err != nil {
		t.Fatalf("diverge session_id: %v", err)
	}
}

func countSnapshots(t *testing.T, s *SQLite, principal contextstate.Principal) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM chat_sessions WHERE workspace_id=? AND subject_id=?`,
		principal.WorkspaceID, principal.SubjectID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestDeleteSessionSnapshotStopsWhenTheSessionIDLookupFails: the by-name
// delete matched nothing, so the row is resolved by its stored session_id.
// With that column unreadable the delete does not know whether a snapshot
// exists, and must say so. Falling through would report ErrSessionNotFound -
// a caller reads that as "already gone" and stops retrying, leaving the
// snapshot and its chat_session_dirs row orphaned.
func TestDeleteSessionSnapshotStopsWhenTheSessionIDLookupFails(t *testing.T) {
	s, principal := openContextTestStore(t)
	defer s.Close()
	seedSnapshotWithDivergedSessionID(t, s, principal, "snapshot-name", "diverged-session-id")
	// A file whose session_id column is not there (a half-applied migration).
	if _, err := s.db.Exec(`ALTER TABLE chat_sessions RENAME COLUMN session_id TO session_id_legacy`); err != nil {
		t.Fatalf("rename column: %v", err)
	}

	err := s.DeleteSessionSnapshot(context.Background(), principal, "diverged-session-id")
	if err == nil {
		t.Fatal("the delete reported success with an unreadable session_id column")
	}
	if errors.Is(err, contextstate.ErrSessionNotFound) {
		t.Errorf("err = %v, want the read failure, not 'not found' - the snapshot is still there", err)
	}
	if !strings.Contains(err.Error(), "session_id") {
		t.Errorf("err = %q, want it to name the failed lookup", err.Error())
	}
	if n := countSnapshots(t, s, principal); n != 1 {
		t.Errorf("%d snapshots survived a failed delete, want 1", n)
	}
}

// TestDeleteSessionSnapshotReportsAFailedSecondDelete: the session_id lookup
// resolved a name, and the delete of THAT name failed. The error must
// surface: falling through to the context-session path would tombstone
// nothing, report ErrSessionNotFound, and leave the snapshot behind.
func TestDeleteSessionSnapshotReportsAFailedSecondDelete(t *testing.T) {
	s, principal := openContextTestStore(t)
	defer s.Close()
	seedSnapshotWithDivergedSessionID(t, s, principal, "snapshot-name", "diverged-session-id")
	// The trigger fires per deleted row, so the first, by-name delete (which
	// matches nothing) still returns cleanly; only the resolved one fails.
	failOn(t, s, "fail_snapshot_delete", "DELETE", "chat_sessions")

	err := s.DeleteSessionSnapshot(context.Background(), principal, "diverged-session-id")
	if err == nil {
		t.Fatal("the delete reported success with a failing row delete")
	}
	if !strings.Contains(err.Error(), "injected") {
		t.Errorf("err = %v, want the injected delete failure", err)
	}
	if n := countSnapshots(t, s, principal); n != 1 {
		t.Errorf("%d snapshots survived a failed delete, want 1", n)
	}
}

// TestResolveSnapshotNameBySessionIDReportsAReadFailure: the resolver
// reports "no such snapshot" only for a real empty result. Any other read
// failure must come back as an error, or the caller cannot tell an absent
// row from an unreadable store.
func TestResolveSnapshotNameBySessionIDReportsAReadFailure(t *testing.T) {
	s, principal := openContextTestStore(t)
	defer s.Close()
	if _, err := s.db.Exec(`ALTER TABLE chat_sessions RENAME COLUMN session_id TO session_id_legacy`); err != nil {
		t.Fatalf("rename column: %v", err)
	}

	name, ok, err := s.resolveSnapshotNameBySessionID(context.Background(), principal, "any-id")
	if err == nil {
		t.Fatalf("an unreadable store resolved to (%q, %v)", name, ok)
	}
	if ok || name != "" {
		t.Errorf("resolve = (%q, %v) on failure, want the zero result", name, ok)
	}
}

// TestDeleteCatalogContextSessionReportsAFailedBegin: with no transaction
// there is nothing to roll back and nothing was written. The delete must
// report the failure rather than the "not found" its caller would otherwise
// see for a session that is very much still there.
func TestDeleteCatalogContextSessionReportsAFailedBegin(t *testing.T) {
	s, principal := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, principal)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := s.deleteCatalogContextSession(ctx, principal, principal.SessionID)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	var tombstoned int
	if err := s.db.QueryRow(`SELECT tombstoned FROM context_sessions WHERE session_id=?`, principal.SessionID).Scan(&tombstoned); err != nil {
		t.Fatal(err)
	}
	if tombstoned != 0 {
		t.Error("a delete that never began a transaction tombstoned the session")
	}
}

// TestDeleteCatalogContextSessionReportsAnUnreadableSessionRow: the revision
// read is what the tombstone is written against. A row it cannot read is not
// ErrNoRows - reporting "not found" would tell the caller the session was
// already deleted while it is still live.
func TestDeleteCatalogContextSessionReportsAnUnreadableSessionRow(t *testing.T) {
	s, principal := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, principal)
	execUnchecked(t, s, `UPDATE context_sessions SET session_revision='not-a-number' WHERE session_id=?`, principal.SessionID)

	err := s.DeleteSessionSnapshot(context.Background(), principal, principal.SessionID)
	if err == nil {
		t.Fatal("a session with an unreadable revision was reported deleted")
	}
	if errors.Is(err, contextstate.ErrSessionNotFound) {
		t.Errorf("err = %v, want the read failure, not 'not found'", err)
	}
	var tombstoned int
	if err := s.db.QueryRow(`SELECT tombstoned FROM context_sessions WHERE session_id=?`, principal.SessionID).Scan(&tombstoned); err != nil {
		t.Fatal(err)
	}
	if tombstoned != 0 {
		t.Error("the session was tombstoned by a delete that could not read it")
	}
}

// TestDeleteRollsBackWhenThePayloadRevocationFails: revoking the session's
// payloads is a step of the same transaction as the tombstone. If it fails
// and the delete returned anyway, the session would stay live with its
// payloads still readable - a delete the user was told had happened.
func TestDeleteRollsBackWhenThePayloadRevocationFails(t *testing.T) {
	s, principal := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, principal)

	rec := payloadFor(t, principal, "payload body")
	seedPayloadRow(t, s, principal, rec.Ref, rec.Ref.Size, 0, rec.Data)
	failOn(t, s, "fail_payload_revoke", "UPDATE", "context_payloads")

	if err := s.DeleteSessionSnapshot(context.Background(), principal, principal.SessionID); err == nil {
		t.Fatal("the delete reported success with a failing payload revocation")
	}
	var tombstoned, revoked int
	if err := s.db.QueryRow(`SELECT tombstoned FROM context_sessions WHERE session_id=?`, principal.SessionID).Scan(&tombstoned); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT revoked FROM context_payloads WHERE ref=?`, rec.Ref.Ref).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	if tombstoned != 0 || revoked != 0 {
		t.Errorf("a failed delete left tombstoned=%d revoked=%d, want both 0", tombstoned, revoked)
	}
	var audits int
	if err := s.db.QueryRow(`SELECT count(*) FROM context_audits WHERE session_id=?`, principal.SessionID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 0 {
		t.Errorf("a failed delete left %d audit rows behind", audits)
	}
}

// TestDeleteReportsAFailedCommit: the tombstone, the audit row, the
// revocations and the admission sweep are one transaction, and a COMMIT that
// fails means none of them landed. Returning nil there would be the worst
// outcome of all: the caller told the session is gone while every row it
// wrote is still in the store.
//
// A commit is failed by deferring foreign-key enforcement to commit time and
// arming a trigger that writes an orphan row, which is a real deferred
// constraint failure rather than a production seam. defer_foreign_keys is
// connection-scoped and clears itself at the end of each transaction, so the
// pool is pinned to one connection and the tombstone transaction is entered
// directly.
func TestDeleteReportsAFailedCommit(t *testing.T) {
	s, principal := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, principal)

	if _, err := s.db.Exec(`CREATE TRIGGER orphan_on_tombstone AFTER INSERT ON context_tombstones
                BEGIN INSERT INTO context_payload_chunks(ref,chunk_index,chunk_count,data) VALUES('no-such-payload-ref',0,1,x'41'); END`); err != nil {
		t.Fatalf("arm orphan trigger: %v", err)
	}
	s.writeDB.SetMaxOpenConns(1)
	if _, err := s.writeDB.Exec(`PRAGMA defer_foreign_keys=ON`); err != nil {
		t.Fatalf("defer foreign keys: %v", err)
	}

	err := s.deleteCatalogContextSession(context.Background(), principal, principal.SessionID)
	if err == nil {
		t.Fatal("the delete reported success with a failing commit")
	}
	if !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Fatalf("err = %v, want the deferred constraint failure at commit", err)
	}
	var tombstoned int
	if err := s.db.QueryRow(`SELECT tombstoned FROM context_sessions WHERE session_id=?`, principal.SessionID).Scan(&tombstoned); err != nil {
		t.Fatal(err)
	}
	if tombstoned != 0 {
		t.Error("a delete whose commit failed left the session tombstoned")
	}
	var tombs int
	if err := s.db.QueryRow(`SELECT count(*) FROM context_tombstones WHERE session_id=?`, principal.SessionID).Scan(&tombs); err != nil {
		t.Fatal(err)
	}
	if tombs != 0 {
		t.Errorf("a failed commit left %d tombstone rows behind", tombs)
	}
}

// TestDeleteSessionSnapshotRowPropagatesANonNotFoundTombstoneError covers
// deleteSessionSnapshotRow's own tombstoneContextSessionTx error check
// (chat_sessions_delete.go:132-134): the live row behind a plain (non-
// worktree) snapshot is bound to a worktree instance, so
// requireWorktreeSessionBinding refuses the plain-namespace tombstone with
// ErrWorktreeDeleted - a real error distinct from ErrSessionNotFound, which
// this function must not swallow.
func TestDeleteSessionSnapshotRowPropagatesANonNotFoundTombstoneError(t *testing.T) {
	s, principal := openContextTestStore(t)
	defer s.Close()
	ctx := context.Background()

	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1111111111111111"}
	seedLiveWorktreeSession(t, s, principal, instance, "opener")

	// A plain (instance_id NULL) snapshot row whose session_id points at the
	// worktree-bound live session above - the legacy/diverged shape this
	// whole file's helper (seedSnapshotWithDivergedSessionID) already models,
	// just pointed at a worktree-bound row instead of a missing one.
	if err := s.SaveSession(ctx, principal, "plain-snapshot", []byte(`[{}]`), "model", "provider", 1, 1, 1, contextstate.SessionSaveOptions{Dir: "/tmp/project"}); err != nil {
		t.Fatalf("seed plain snapshot: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE chat_sessions SET session_id=? WHERE workspace_id=? AND subject_id=? AND name=?`,
		principal.SessionID, principal.WorkspaceID, principal.SubjectID, "plain-snapshot"); err != nil {
		t.Fatalf("point snapshot at the worktree-bound session: %v", err)
	}

	_, err := s.deleteSessionSnapshotRow(ctx, principal, "plain-snapshot")
	if !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("deleteSessionSnapshotRow = %v, want ErrWorktreeDeleted from the binding mismatch", err)
	}
	if n := countSnapshots(t, s, principal); n != 1 {
		t.Errorf("%d snapshots survived a failed delete, want 1 (rolled back)", n)
	}
}
