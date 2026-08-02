package storage

import (
	"context"
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

func TestCatalogContextDeleteReportsAnUnreclaimableAdmissionRow(t *testing.T) {
	store, principal := admissionStore(t)
	if _, err := store.db.Exec(`INSERT INTO context_sessions(workspace_id,subject_id,session_id,capability_digest,session_revision,durable_revision,source_sequence,provider,model,binding_generation) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		principal.WorkspaceID, principal.SubjectID, "ctx", "cap", 1, 1, 1, "p", "m", 1); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSessionAdmission(context.Background(), principal, "ctx",
		contextstate.SessionAdmission{Agent: "reader", Digest: "d", Names: []string{"grep"}}); err != nil {
		t.Fatal(err)
	}
	blockDeleteOn(t, store, "chat_session_admissions")
	if err := store.DeleteSessionSnapshot(context.Background(), principal, "ctx"); err == nil {
		t.Fatal("context-backed delete reported success while the admission row was unreclaimable")
	}
}

// TestCatalogContextDeleteRollsBackOnAnUnreclaimableAdmissionRow drives the
// context-backed retention path directly: DeleteSessionSnapshot only reaches it
// after the snapshot delete finds no row.
func TestCatalogContextDeleteRollsBackOnAnUnreclaimableAdmissionRow(t *testing.T) {
	store, principal := admissionStore(t)
	if _, err := store.db.Exec(`INSERT INTO context_sessions(workspace_id,subject_id,session_id,capability_digest,session_revision,durable_revision,source_sequence,provider,model,binding_generation) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		principal.WorkspaceID, principal.SubjectID, "ctx", "cap", 1, 1, 1, "p", "m", 1); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSessionAdmission(context.Background(), principal, "ctx",
		contextstate.SessionAdmission{Agent: "reader", Digest: "d", Names: []string{"grep"}}); err != nil {
		t.Fatal(err)
	}
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
