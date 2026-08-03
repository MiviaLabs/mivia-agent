package storage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

func admissionStore(t *testing.T) (*SQLite, contextstate.Principal) {
	t.Helper()
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, contextstate.Principal{WorkspaceID: "ws1", SubjectID: "subj1", SessionID: "sess1"}
}

func TestSessionAdmissionRoundTrip(t *testing.T) {
	store, principal := admissionStore(t)
	ctx := context.Background()
	want := contextstate.SessionAdmission{Agent: "reader", Digest: "d1", Names: []string{"grep", "glob"}}
	if err := store.SaveSessionAdmission(ctx, principal, "snap", want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := store.LoadSessionAdmission(ctx, principal, "snap")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Agent != want.Agent || got.Digest != want.Digest || len(got.Names) != 2 ||
		got.Names[0] != "grep" || got.Names[1] != "glob" {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
}

func TestSessionAdmissionOverwritesInPlace(t *testing.T) {
	store, principal := admissionStore(t)
	ctx := context.Background()
	if err := store.SaveSessionAdmission(ctx, principal, "snap",
		contextstate.SessionAdmission{Agent: "reader", Digest: "d1", Names: []string{"grep"}}); err != nil {
		t.Fatalf("save 1: %v", err)
	}
	if err := store.SaveSessionAdmission(ctx, principal, "snap",
		contextstate.SessionAdmission{Agent: "writer", Digest: "d2", Names: []string{"glob"}}); err != nil {
		t.Fatalf("save 2: %v", err)
	}
	got, err := store.LoadSessionAdmission(ctx, principal, "snap")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Agent != "writer" || got.Digest != "d2" || len(got.Names) != 1 || got.Names[0] != "glob" {
		t.Fatalf("record = %+v, want the second save", got)
	}
}

// TestSessionAdmissionEmptySetDeletesTheRow: resuming a session that admitted
// nothing must not resurrect an older set.
func TestSessionAdmissionEmptySetDeletesTheRow(t *testing.T) {
	store, principal := admissionStore(t)
	ctx := context.Background()
	if err := store.SaveSessionAdmission(ctx, principal, "snap",
		contextstate.SessionAdmission{Agent: "reader", Digest: "d1", Names: []string{"grep"}}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := store.SaveSessionAdmission(ctx, principal, "snap",
		contextstate.SessionAdmission{Agent: "reader", Digest: "d1"}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got, err := store.LoadSessionAdmission(ctx, principal, "snap")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got.Names) != 0 || got.Agent != "" {
		t.Fatalf("record = %+v, want the zero value after clearing", got)
	}
}

func TestSessionAdmissionMissingRowIsNotAnError(t *testing.T) {
	store, principal := admissionStore(t)
	got, err := store.LoadSessionAdmission(context.Background(), principal, "never-saved")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got.Names) != 0 {
		t.Fatalf("record = %+v, want the zero value", got)
	}
}

func TestSessionAdmissionIsScopedToItsOwner(t *testing.T) {
	store, principal := admissionStore(t)
	ctx := context.Background()
	if err := store.SaveSessionAdmission(ctx, principal, "snap",
		contextstate.SessionAdmission{Agent: "reader", Digest: "d1", Names: []string{"grep"}}); err != nil {
		t.Fatalf("save: %v", err)
	}
	other := contextstate.Principal{WorkspaceID: "ws1", SubjectID: "other", SessionID: "sess2"}
	got, err := store.LoadSessionAdmission(ctx, other, "snap")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got.Names) != 0 {
		t.Fatalf("another subject read %v", got.Names)
	}
}

func TestSessionAdmissionRejectsInvalidInput(t *testing.T) {
	store, principal := admissionStore(t)
	ctx := context.Background()
	record := contextstate.SessionAdmission{Agent: "reader", Digest: "d1", Names: []string{"grep"}}
	if err := store.SaveSessionAdmission(ctx, contextstate.Principal{}, "snap", record); err == nil {
		t.Fatal("save accepted an invalid principal")
	}
	if err := store.SaveSessionAdmission(ctx, principal, "bad/name", record); err == nil {
		t.Fatal("save accepted a path-shaped session name")
	}
	if _, err := store.LoadSessionAdmission(ctx, contextstate.Principal{}, "snap"); err == nil {
		t.Fatal("load accepted an invalid principal")
	}
	if _, err := store.LoadSessionAdmission(ctx, principal, "bad/name"); err == nil {
		t.Fatal("load accepted a path-shaped session name")
	}
}

func TestSessionAdmissionRejectsAnOversizedSet(t *testing.T) {
	store, principal := admissionStore(t)
	previous := contextstate.CurrentLimits()
	t.Cleanup(func() { contextstate.SetLimits(previous) })
	bounded := previous
	bounded.SessionStateBytes = 64
	contextstate.SetLimits(bounded)
	names := make([]string, 0, 32)
	for i := 0; i < 32; i++ {
		names = append(names, "aaaaaa")
	}
	err := store.SaveSessionAdmission(context.Background(), principal, "snap",
		contextstate.SessionAdmission{Agent: "reader", Digest: "d1", Names: names})
	if !errors.Is(err, contextstate.ErrInvalidDTO) {
		t.Fatalf("error = %v, want ErrInvalidDTO for an oversized set", err)
	}
}

// TestContextSchemaV4AddsTheAdmissionTable pins the migration itself: a store
// opened below v4 must reach v4 with the admission table present and the dirty
// flag cleared.
//
// The admission table is v4, not v3: plan tools/05 and the multi-chunk payload
// migration were developed in parallel and both claimed v3, and v3 shipped
// first. See applyContextSchemaV4.
func TestContextSchemaV4AddsTheAdmissionTable(t *testing.T) {
	store, _ := admissionStore(t)
	var version int
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 4 {
		t.Fatalf("user_version = %d, want 4", version)
	}
	var count int
	if err := store.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='chat_session_admissions'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("chat_session_admissions table is missing after migration")
	}
	var dirty int
	if err := store.db.QueryRow(`SELECT dirty FROM context_schema_migrations WHERE version = 4`).Scan(&dirty); err != nil {
		t.Fatal(err)
	}
	if dirty != 0 {
		t.Fatalf("dirty = %d, want 0", dirty)
	}
}

func TestLoadSessionAdmissionRejectsACorruptedRow(t *testing.T) {
	store, principal := admissionStore(t)
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO chat_session_admissions(workspace_id,subject_id,name,agent,digest,names,updated_at) VALUES(?,?,?,?,?,?,?)`,
		principal.WorkspaceID, principal.SubjectID, "snap", "reader", "d1", "not json", "now"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadSessionAdmission(ctx, principal, "snap"); !errors.Is(err, contextstate.ErrInvalidDTO) {
		t.Fatalf("error = %v, want ErrInvalidDTO for a corrupted row", err)
	}
}

func TestApplyContextMigrationReportsFailures(t *testing.T) {
	store, _ := admissionStore(t)
	// A statement that cannot execute must roll back and be reported, never
	// leave the schema half-applied at a bumped version.
	if err := applyContextMigration(store.db, 99, `THIS IS NOT SQL`); err == nil {
		t.Fatal("invalid DDL was accepted")
	}
	var version int
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != currentContextSchemaVersion {
		t.Fatalf("version = %d, want the failed migration to leave %d", version, currentContextSchemaVersion)
	}
	// Without the bookkeeping table the migration cannot record itself.
	if _, err := store.db.Exec(`DROP TABLE context_schema_migrations`); err != nil {
		t.Fatal(err)
	}
	if err := applyContextMigration(store.db, 98, `CREATE TABLE probe(x INTEGER)`); err == nil {
		t.Fatal("migration succeeded with no bookkeeping table")
	}
	// A closed database cannot even begin.
	closed, err := OpenSQLite(filepath.Join(t.TempDir(), "closed.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := applyContextMigration(closed.db, 97, `CREATE TABLE probe(x INTEGER)`); err == nil {
		t.Fatal("migration began on a closed database")
	}
}

// TestContextSchemaV3RepairsADirtyFlag mirrors the v1/v2 recovery contract: a
// store that crashed between the v3 table commit and the dirty clear must be
// repaired rather than reported as permanently dirty.
func TestContextSchemaV3RepairsADirtyFlag(t *testing.T) {
	store, _ := admissionStore(t)
	if _, err := store.db.Exec(`UPDATE context_schema_migrations SET dirty = 1 WHERE version = 3`); err != nil {
		t.Fatal(err)
	}
	if err := migrateContextSchema(store.db); err != nil {
		t.Fatalf("migrate on a stuck v3 store: %v", err)
	}
	var dirty int
	if err := store.db.QueryRow(`SELECT dirty FROM context_schema_migrations WHERE version = 4`).Scan(&dirty); err != nil {
		t.Fatal(err)
	}
	if dirty != 0 {
		t.Fatalf("dirty = %d, want 0 after repair", dirty)
	}
}

func TestApplyContextMigrationReportsAFinalizeFailure(t *testing.T) {
	store, _ := admissionStore(t)
	// A trigger that aborts the dirty-flag clear makes the finalize phase fail
	// after the schema phase already committed.
	if _, err := store.db.Exec(`CREATE TRIGGER block_clear BEFORE UPDATE ON context_schema_migrations BEGIN SELECT RAISE(ABORT, 'blocked'); END`); err != nil {
		t.Fatal(err)
	}
	err := applyContextMigration(store.db, 96, `CREATE TABLE probe(x INTEGER)`)
	if err == nil || !strings.Contains(err.Error(), "finalize") {
		t.Fatalf("error = %v, want a finalize-phase failure", err)
	}
}

func TestLoadSessionAdmissionReportsAQueryFailure(t *testing.T) {
	store, principal := admissionStore(t)
	if _, err := store.db.Exec(`DROP TABLE chat_session_admissions`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadSessionAdmission(context.Background(), principal, "snap"); err == nil {
		t.Fatal("a missing table was reported as an empty record")
	}
}

// TestMigrateStopsWhenV2Fails: a failed intermediate migration must abort the
// chain rather than let v3 run against a schema that never landed.
func TestMigrateStopsWhenV2Fails(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "v1.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS context_schema_migrations(version INTEGER PRIMARY KEY, dirty INTEGER NOT NULL CHECK(dirty IN (0,1)))`); err != nil {
		t.Fatal(err)
	}
	for _, statement := range contextSchemaStatements() {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`PRAGMA user_version = 1`); err != nil {
		t.Fatal(err)
	}
	// v2 creates chat_sessions; a table already under that name makes it fail.
	if _, err := db.Exec(`CREATE TABLE chat_sessions(x INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if err := migrateContextSchema(db); err == nil {
		t.Fatal("the migration chain continued past a failed v2")
	}
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("version = %d, want the store left at 1", version)
	}
}
