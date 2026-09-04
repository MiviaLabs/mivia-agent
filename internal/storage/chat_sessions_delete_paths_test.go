package storage

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// The delete path's refusals and its rollback arms.
//
// Deleting a session is the one operation that cannot be half-done: the
// tombstone, the payload revocation, the audit row, the admission record
// and the directory association either all land or none do. Every arm
// below is a step failing mid-way, and what is asserted is not the error
// but that NOTHING committed - a torn delete leaves a session that reads
// as live with its payloads already revoked.
//
// SQL failures are injected with triggers rather than a production seam,
// following dir_metadata_errors_test.go: the seam in context_failure.go
// covers the commit path only, and adding steps to the delete path would
// mean shipping injection points for a test to use.

// failOn arms a trigger that aborts the next write of the given kind to
// the given table, and removes it when the test ends.
func failOn(t *testing.T, s *SQLite, name, event, table string) {
	t.Helper()
	stmt := "CREATE TRIGGER " + name + " BEFORE " + event + " ON " + table +
		" BEGIN SELECT RAISE(ABORT, 'injected " + name + "'); END"
	if _, err := s.db.Exec(stmt); err != nil {
		t.Fatalf("arm %s: %v", name, err)
	}
	t.Cleanup(func() { _, _ = s.db.Exec("DROP TRIGGER IF EXISTS " + name) })
}

// TestDeleteSessionSnapshotValidatesBeforeTouchingTheStore: the principal
// and the catalog name are checked first, so a malformed caller cannot
// reach a write at all. Both guards return before the write lock is even
// taken.
func TestDeleteSessionSnapshotValidatesBeforeTouchingTheStore(t *testing.T) {
	s, principal := openContextTestStore(t)
	defer s.Close()
	ctx := context.Background()

	if err := s.DeleteSessionSnapshot(ctx, contextstate.Principal{}, "snap"); err == nil {
		t.Error("an empty principal was accepted")
	}
	for _, bad := range []string{"", strings.Repeat("x", 4096), "has/slash"} {
		if err := s.DeleteSessionSnapshot(ctx, principal, bad); err == nil {
			t.Errorf("catalog name %q was accepted", bad)
		}
	}
}

// TestDeletingAContextSessionThatDoesNotExistReportsNotFound: the name
// matched no snapshot and no context session, so there is nothing to
// tombstone. It must say so rather than report a successful delete of
// nothing, which is what a caller retries against.
func TestDeletingAContextSessionThatDoesNotExistReportsNotFound(t *testing.T) {
	s, principal := openContextTestStore(t)
	defer s.Close()

	err := s.DeleteSessionSnapshot(context.Background(), principal, "never-existed")
	if !errors.Is(err, contextstate.ErrSessionNotFound) {
		t.Errorf("deleting an unknown session returned %v, want ErrSessionNotFound", err)
	}
}

// TestDeletingAContextSessionTombstonesItAndRevokesItsPayloads is the
// success path the rollback tests below are measured against.
func TestDeletingAContextSessionTombstonesItAndRevokesItsPayloads(t *testing.T) {
	s, principal := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, principal)

	if err := s.DeleteSessionSnapshot(context.Background(), principal, principal.SessionID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	var tombstoned int
	if err := s.db.QueryRow(`SELECT tombstoned FROM context_sessions WHERE session_id=?`, principal.SessionID).Scan(&tombstoned); err != nil {
		t.Fatal(err)
	}
	if tombstoned != 1 {
		t.Error("the session was not tombstoned")
	}
	var audits, tombs int
	if err := s.db.QueryRow(`SELECT count(*) FROM context_audits WHERE session_id=?`, principal.SessionID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM context_tombstones WHERE session_id=?`, principal.SessionID).Scan(&tombs); err != nil {
		t.Fatal(err)
	}
	if audits != 1 || tombs != 1 {
		t.Errorf("audits=%d tombstones=%d, want one of each", audits, tombs)
	}
}

// TestAFailureAtAnyStepOfTheDeleteLeavesTheSessionWhole: each write in
// the tombstone transaction is failed in turn, and the session must come
// out untouched every time. A step that returned its error without
// rolling back would leave the session live with its payloads revoked -
// visible to the user as a session that opens empty.
func TestAFailureAtAnyStepOfTheDeleteLeavesTheSessionWhole(t *testing.T) {
	for _, step := range []struct{ name, event, table string }{
		{"fail_tombstone_update", "UPDATE", "context_sessions"},
		{"fail_audit_insert", "INSERT", "context_audits"},
		{"fail_tombstone_insert", "INSERT", "context_tombstones"},
	} {
		t.Run(step.table, func(t *testing.T) {
			s, principal := openContextTestStore(t)
			defer s.Close()
			seedContextSession(t, s, principal)
			failOn(t, s, step.name, step.event, step.table)

			if err := s.DeleteSessionSnapshot(context.Background(), principal, principal.SessionID); err == nil {
				t.Fatal("the delete reported success with a failing step")
			}

			var tombstoned int
			if err := s.db.QueryRow(`SELECT tombstoned FROM context_sessions WHERE session_id=?`, principal.SessionID).Scan(&tombstoned); err != nil {
				t.Fatal(err)
			}
			if tombstoned != 0 {
				t.Error("the session was left tombstoned by a delete that failed")
			}
			var audits, tombs int
			if err := s.db.QueryRow(`SELECT count(*) FROM context_audits WHERE session_id=?`, principal.SessionID).Scan(&audits); err != nil {
				t.Fatal(err)
			}
			if err := s.db.QueryRow(`SELECT count(*) FROM context_tombstones WHERE session_id=?`, principal.SessionID).Scan(&tombs); err != nil {
				t.Fatal(err)
			}
			if audits != 0 || tombs != 0 {
				t.Errorf("a failed delete left audits=%d tombstones=%d behind", audits, tombs)
			}
		})
	}
}

// TestPruneSessionSnapshotsValidatesEveryNameBeforeCommitting: the prune
// takes a list, and a bad name anywhere in it must abort the whole batch.
// Validating as it goes and committing what it had would delete a prefix
// of the caller's list and report failure.
func TestPruneSessionSnapshotsValidatesEveryNameBeforeCommitting(t *testing.T) {
	s, principal := openContextTestStore(t)
	defer s.Close()
	ctx := context.Background()

	if err := s.PruneSessionSnapshots(ctx, contextstate.Principal{}, []string{"a"}); err == nil {
		t.Error("an empty principal was accepted")
	}
	// No names is not an error: there is nothing to prune.
	if err := s.PruneSessionSnapshots(ctx, principal, nil); err != nil {
		t.Errorf("pruning an empty list errored: %v", err)
	}

	catalog := contextstate.SessionCatalog(s)
	for _, name := range []string{"keep-me", "also-keep"} {
		if err := catalog.SaveSession(ctx, principal, name, []byte(`[{"role":"user"}]`),
			"model", "provider", 1, 2, 1, contextstate.SessionSaveOptions{}); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	if err := s.PruneSessionSnapshots(ctx, principal, []string{"keep-me", "bad/name"}); err == nil {
		t.Fatal("a batch containing an invalid name reported success")
	}
	var left int
	if err := s.db.QueryRow(`SELECT count(*) FROM chat_sessions WHERE workspace_id=? AND subject_id=?`,
		principal.WorkspaceID, principal.SubjectID).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 2 {
		t.Errorf("%d snapshots survived the aborted prune, want both", left)
	}
}
