package storage

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// seedAdmittedSession writes a snapshot and its admission record so a delete
// path has both rows to reclaim.
func seedAdmittedSession(t *testing.T, store *SQLite, principal contextstate.Principal, name string) {
	t.Helper()
	ctx := context.Background()
	if err := store.SaveSession(ctx, principal, name, []byte(`[{"role":"user"}]`), "m", "p", 1, 1, 1); err != nil {
		t.Fatalf("save session %s: %v", name, err)
	}
	if err := store.SaveSessionAdmission(ctx, principal, name,
		contextstate.SessionAdmission{Agent: "reader", Digest: "d1", Names: []string{"grep"}}); err != nil {
		t.Fatalf("save admission %s: %v", name, err)
	}
}

func assertNoAdmission(t *testing.T, store *SQLite, principal contextstate.Principal, name string) {
	t.Helper()
	got, err := store.LoadSessionAdmission(context.Background(), principal, name)
	if err != nil {
		t.Fatalf("load admission %s: %v", name, err)
	}
	if got.Agent != "" || got.Digest != "" || len(got.Names) != 0 {
		t.Fatalf("admission for deleted session %s survived: %+v", name, got)
	}
}

// TestDeleteSessionSnapshotReclaimsAdmission pins that a user-requested delete
// takes the session's admitted tool names with it. chat_session_admissions has
// no foreign key, so nothing else would ever reclaim the row.
func TestDeleteSessionSnapshotReclaimsAdmission(t *testing.T) {
	store, principal := admissionStore(t)
	seedAdmittedSession(t, store, principal, "mysess")
	if err := store.DeleteSessionSnapshot(context.Background(), principal, "mysess"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	assertNoAdmission(t, store, principal, "mysess")
}

// TestPruneSessionSnapshotsReclaimsAdmissions covers the bulk path.
func TestPruneSessionSnapshotsReclaimsAdmissions(t *testing.T) {
	store, principal := admissionStore(t)
	seedAdmittedSession(t, store, principal, "s1")
	seedAdmittedSession(t, store, principal, "s2")
	if err := store.PruneSessionSnapshots(context.Background(), principal, []string{"s2"}); err != nil {
		t.Fatalf("prune: %v", err)
	}
	assertNoAdmission(t, store, principal, "s2")
	kept, err := store.LoadSessionAdmission(context.Background(), principal, "s1")
	if err != nil {
		t.Fatalf("load kept: %v", err)
	}
	if len(kept.Names) != 1 {
		t.Fatalf("prune removed an unrelated admission: %+v", kept)
	}
}

// TestAdmissionRowsDoNotAccumulate is the growth check: repeated create/delete
// cycles must leave the admission table as empty as the catalog it shadows.
func TestAdmissionRowsDoNotAccumulate(t *testing.T) {
	store, principal := admissionStore(t)
	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("cycle%d", i)
		seedAdmittedSession(t, store, principal, name)
		if err := store.DeleteSessionSnapshot(context.Background(), principal, name); err != nil {
			t.Fatalf("delete %s: %v", name, err)
		}
	}
	var sessions, admissions int
	if err := store.db.QueryRow(`SELECT count(*) FROM chat_sessions`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT count(*) FROM chat_session_admissions`).Scan(&admissions); err != nil {
		t.Fatal(err)
	}
	if sessions != 0 || admissions != 0 {
		t.Fatalf("chat_sessions=%d chat_session_admissions=%d, want 0 and 0", sessions, admissions)
	}
}

// blockDeleteOn installs a trigger that aborts deletes on table, so the
// transactional delete paths can be driven into their rollback branches.
func blockDeleteOn(t *testing.T, store *SQLite, table string) {
	t.Helper()
	stmt := "CREATE TRIGGER block_" + table + " BEFORE DELETE ON " + table + " BEGIN SELECT RAISE(ABORT, 'blocked'); END"
	if _, err := store.db.Exec(stmt); err != nil {
		t.Fatal(err)
	}
}

func seedAdmission(t *testing.T, store *SQLite, principal contextstate.Principal, name string) {
	t.Helper()
	if _, err := store.db.Exec(`INSERT INTO chat_sessions(workspace_id,subject_id,name,model,provider,messages,created_at,updated_at,turn_count,token_count,message_count) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		principal.WorkspaceID, principal.SubjectID, name, "m", "p", []byte("[]"), "now", "now", 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSessionAdmission(context.Background(), principal, name,
		contextstate.SessionAdmission{Agent: "reader", Digest: "d", Names: []string{"grep"}}); err != nil {
		t.Fatal(err)
	}
}

// TestDeleteRollsBackWhenTheAdmissionRowCannotBeReclaimed: the snapshot and its
// admission record are one transaction, so a failure to reclaim the record must
// not leave the snapshot deleted.
func TestDeleteRollsBackWhenTheAdmissionRowCannotBeReclaimed(t *testing.T) {
	store, principal := admissionStore(t)
	seedAdmission(t, store, principal, "snap")
	blockDeleteOn(t, store, "chat_session_admissions")
	if err := store.DeleteSessionSnapshot(context.Background(), principal, "snap"); err == nil {
		t.Fatal("delete reported success while the admission row was unreclaimable")
	}
	var snapshots int
	if err := store.db.QueryRow(`SELECT count(*) FROM chat_sessions WHERE name='snap'`).Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if snapshots != 1 {
		t.Fatalf("snapshot rows = %d, want the delete rolled back", snapshots)
	}
}

func TestDeleteReportsASnapshotFailure(t *testing.T) {
	store, principal := admissionStore(t)
	seedAdmission(t, store, principal, "snap")
	blockDeleteOn(t, store, "chat_sessions")
	if err := store.DeleteSessionSnapshot(context.Background(), principal, "snap"); err == nil {
		t.Fatal("delete reported success while the snapshot row was undeletable")
	}
}

func TestDeleteReportsAFailureToBegin(t *testing.T) {
	store, principal := admissionStore(t)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteSessionSnapshot(context.Background(), principal, "snap"); err == nil {
		t.Fatal("delete began a transaction on a closed store")
	}
}

func TestPruneReportsAnUnreclaimableAdmissionRow(t *testing.T) {
	store, principal := admissionStore(t)
	seedAdmission(t, store, principal, "snap")
	blockDeleteOn(t, store, "chat_session_admissions")
	if err := store.PruneSessionSnapshots(context.Background(), principal, []string{"snap"}); err == nil {
		t.Fatal("prune reported success while the admission row was unreclaimable")
	}
}

// blockInsertOn installs a trigger that aborts inserts on table, so a
// retention transaction can be driven into its rollback branch without
// touching the admission delete that runs at the end of it.
func blockInsertOn(t *testing.T, store *SQLite, table string) {
	t.Helper()
	stmt := "CREATE TRIGGER block_insert_" + table + " BEFORE INSERT ON " + table + " BEGIN SELECT RAISE(ABORT, 'blocked'); END"
	if _, err := store.db.Exec(stmt); err != nil {
		t.Fatal(err)
	}
}

func seedContextBackedSession(t *testing.T, store *SQLite, principal contextstate.Principal, sessionID string) {
	t.Helper()
	if _, err := store.db.Exec(`INSERT INTO context_sessions(workspace_id,subject_id,session_id,capability_digest,session_revision,durable_revision,source_sequence,provider,model,binding_generation) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		principal.WorkspaceID, principal.SubjectID, sessionID, "cap", 1, 1, 1, "p", "m", 1); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSessionAdmission(context.Background(), principal, sessionID,
		contextstate.SessionAdmission{Agent: "reader", Digest: "d", Names: []string{"grep"}}); err != nil {
		t.Fatal(err)
	}
}

func countAdmissionRows(t *testing.T, store *SQLite, name string) int {
	t.Helper()
	var rows int
	if err := store.db.QueryRow(`SELECT count(*) FROM chat_session_admissions WHERE name=?`, name).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

// TestCatalogContextDeleteKeepsTheAdmissionRowWhenRetentionFails pins the
// atomicity of the context-backed delete seen through the public entry point.
// The snapshot transaction must not reclaim the admission row on behalf of a
// retention transaction that has not committed yet: if retention fails the
// session survives, so its admission record has to survive with it.
func TestCatalogContextDeleteKeepsTheAdmissionRowWhenRetentionFails(t *testing.T) {
	store, principal := admissionStore(t)
	seedContextBackedSession(t, store, principal, "ctx")
	blockInsertOn(t, store, "context_tombstones")
	if err := store.DeleteSessionSnapshot(context.Background(), principal, "ctx"); err == nil {
		t.Fatal("context-backed delete reported success while retention was blocked")
	}
	var tombstoned int
	if err := store.db.QueryRow(`SELECT tombstoned FROM context_sessions WHERE session_id='ctx'`).Scan(&tombstoned); err != nil {
		t.Fatal(err)
	}
	if tombstoned != 0 {
		t.Fatal("the session was tombstoned even though the retention transaction rolled back")
	}
	if rows := countAdmissionRows(t, store, "ctx"); rows != 1 {
		t.Fatalf("admission rows = %d, want the record kept alongside the surviving session", rows)
	}
	record, err := store.LoadSessionAdmission(context.Background(), principal, "ctx")
	if err != nil {
		t.Fatal(err)
	}
	if len(record.Names) != 1 {
		t.Fatalf("admitted tools lost from a session that was never deleted: %+v", record)
	}
}

// blockAdmissionDeleteAfterTombstone aborts an admission delete only once the
// owning context session is tombstoned. The tombstone is written by the
// retention transaction itself, so the trigger can only fire from inside it -
// which is what makes the assertions below evidence about the retention path
// rather than about the snapshot transaction that runs first.
func blockAdmissionDeleteAfterTombstone(t *testing.T, store *SQLite) {
	t.Helper()
	if _, err := store.db.Exec(`CREATE TRIGGER block_retention_admission BEFORE DELETE ON chat_session_admissions WHEN EXISTS(SELECT 1 FROM context_sessions WHERE session_id=OLD.name AND workspace_id=OLD.workspace_id AND subject_id=OLD.subject_id AND tombstoned=1) BEGIN SELECT RAISE(ABORT, 'blocked'); END`); err != nil {
		t.Fatal(err)
	}
}

// TestCatalogContextDeleteReportsAnUnreclaimableAdmissionRow drives the
// retention path through DeleteSessionSnapshot and asserts the retention
// transaction itself was entered and rolled back, not that some earlier
// transaction failed first.
func TestCatalogContextDeleteReportsAnUnreclaimableAdmissionRow(t *testing.T) {
	store, principal := admissionStore(t)
	seedContextBackedSession(t, store, principal, "ctx")
	blockAdmissionDeleteAfterTombstone(t, store)
	if err := store.DeleteSessionSnapshot(context.Background(), principal, "ctx"); err == nil {
		t.Fatal("context-backed delete reported success while the admission row was unreclaimable")
	}
	var audits int
	if err := store.db.QueryRow(`SELECT count(*) FROM context_audits WHERE session_id='ctx'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 0 {
		t.Fatalf("audit rows = %d, want the retention transaction rolled back", audits)
	}
	var tombstoned int
	if err := store.db.QueryRow(`SELECT tombstoned FROM context_sessions WHERE session_id='ctx'`).Scan(&tombstoned); err != nil {
		t.Fatal(err)
	}
	if tombstoned != 0 {
		t.Fatal("the session was tombstoned even though the transaction rolled back")
	}
	if rows := countAdmissionRows(t, store, "ctx"); rows != 1 {
		t.Fatalf("admission rows = %d, want the record kept alongside the surviving session", rows)
	}
}

// TestDeleteReclaimsAnOrphanedAdmissionRow covers the case neither delete path
// matches: an admission record whose session is already gone. The delete
// reports not-found, but must not leave the orphan behind - nothing else ever
// reclaims chat_session_admissions.
func TestDeleteReclaimsAnOrphanedAdmissionRow(t *testing.T) {
	store, principal := admissionStore(t)
	if err := store.SaveSessionAdmission(context.Background(), principal, "gone",
		contextstate.SessionAdmission{Agent: "reader", Digest: "d", Names: []string{"grep"}}); err != nil {
		t.Fatal(err)
	}
	err := store.DeleteSessionSnapshot(context.Background(), principal, "gone")
	if !errors.Is(err, contextstate.ErrSessionNotFound) {
		t.Fatalf("delete err = %v, want ErrSessionNotFound", err)
	}
	if rows := countAdmissionRows(t, store, "gone"); rows != 0 {
		t.Fatalf("admission rows = %d, want the orphan reclaimed", rows)
	}
}

// TestDeleteReportsAnUnreclaimableOrphanedAdmissionRow: the orphan sweep is a
// write, so its failure has to surface rather than be swallowed behind the
// not-found result.
func TestDeleteReportsAnUnreclaimableOrphanedAdmissionRow(t *testing.T) {
	store, principal := admissionStore(t)
	if err := store.SaveSessionAdmission(context.Background(), principal, "gone",
		contextstate.SessionAdmission{Agent: "reader", Digest: "d", Names: []string{"grep"}}); err != nil {
		t.Fatal(err)
	}
	blockDeleteOn(t, store, "chat_session_admissions")
	err := store.DeleteSessionSnapshot(context.Background(), principal, "gone")
	if err == nil || errors.Is(err, contextstate.ErrSessionNotFound) {
		t.Fatalf("delete err = %v, want the failed orphan reclaim reported", err)
	}
}

// TestCatalogContextDeleteRollsBackOnAnUnreclaimableAdmissionRow drives the
// context-backed retention path directly: DeleteSessionSnapshot only reaches it
// after the snapshot delete finds no row.
func TestCatalogContextDeleteRollsBackOnAnUnreclaimableAdmissionRow(t *testing.T) {
	store, principal := admissionStore(t)
	seedContextBackedSession(t, store, principal, "ctx")
	blockDeleteOn(t, store, "chat_session_admissions")
	if err := store.deleteCatalogContextSession(context.Background(), principal, "ctx"); err == nil {
		t.Fatal("retention delete reported success while the admission row was unreclaimable")
	}
	var tombstoned int
	if err := store.db.QueryRow(`SELECT tombstoned FROM context_sessions WHERE session_id='ctx'`).Scan(&tombstoned); err != nil {
		t.Fatal(err)
	}
	if tombstoned != 0 {
		t.Fatal("the session was tombstoned even though the transaction rolled back")
	}
}

// TestPruneLeavesALiveContextSessionsAdmissionAlone: prune names snapshots. A
// name that matches no snapshot may still be a running context-backed session,
// and stripping its admitted tool set would silently shrink a live surface.
func TestPruneLeavesALiveContextSessionsAdmissionAlone(t *testing.T) {
	store, principal := admissionStore(t)
	seedContextBackedSession(t, store, principal, "ctx")
	if err := store.PruneSessionSnapshots(context.Background(), principal, []string{"ctx"}); err != nil {
		t.Fatalf("prune: %v", err)
	}
	got, err := store.LoadSessionAdmission(context.Background(), principal, "ctx")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got.Names) == 0 {
		t.Fatal("prune reclaimed the admission row of a session it did not delete")
	}
	var alive int
	if err := store.db.QueryRow(`SELECT count(*) FROM context_sessions WHERE session_id='ctx' AND tombstoned=0`).Scan(&alive); err != nil {
		t.Fatal(err)
	}
	if alive != 1 {
		t.Fatal("precondition: the context session should still be live")
	}
}

func TestPruneReportsAFailureToCountTheSnapshotDelete(t *testing.T) {
	// The prune loop decides whether to reclaim the admission row from the
	// snapshot delete's row count, so a store that cannot report one must
	// abort rather than guess.
	store, principal := admissionStore(t)
	seedAdmission(t, store, principal, "snap")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.PruneSessionSnapshots(context.Background(), principal, []string{"snap"}); err == nil {
		t.Fatal("prune proceeded on a closed store")
	}
}

func TestPruneReportsASnapshotDeleteFailure(t *testing.T) {
	store, principal := admissionStore(t)
	seedAdmission(t, store, principal, "snap")
	blockDeleteOn(t, store, "chat_sessions")
	if err := store.PruneSessionSnapshots(context.Background(), principal, []string{"snap"}); err == nil {
		t.Fatal("prune reported success while the snapshot row was undeletable")
	}
	got, err := store.LoadSessionAdmission(context.Background(), principal, "snap")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Names) == 0 {
		t.Fatal("a rolled-back prune still reclaimed the admission row")
	}
}
