package storage

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// TestMigrateStopsWhenAnIntermediateMigrationFails pins the failure path of the
// v2 -> v3 -> v4 chain that the schema-version renumber introduced.
//
// The admission table is v4 and the payload-chunk table is v3, so a store at v2
// now runs two migrations to reach current. That made the intermediate step's
// error path reachable for the first time: before the renumber v3 was terminal
// and its error returned straight to the caller. If a failing v3 were allowed to
// fall through, v4 would publish user_version = 4 over a store with no
// context_payload_chunks table, and no later open would ever repair it - the
// exact class of damage the renumber exists to prevent.
func TestMigrateStopsWhenAnIntermediateMigrationFails(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "chain.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Bring the store honestly to a clean v2.
	if _, err := db.Exec(`CREATE TABLE context_schema_migrations(version INTEGER PRIMARY KEY, dirty INTEGER NOT NULL CHECK(dirty IN (0,1)))`); err != nil {
		t.Fatal(err)
	}
	if err := applyContextSchemaV1(db); err != nil {
		t.Fatal(err)
	}
	if err := applyContextSchemaV2(db); err != nil {
		t.Fatal(err)
	}

	// Occupy v3's table name so its CREATE TABLE fails. v3 does not use
	// IF NOT EXISTS, so this is the migration failing for a real reason rather
	// than a stubbed error.
	if _, err := db.Exec(`CREATE TABLE context_payload_chunks(placeholder INTEGER)`); err != nil {
		t.Fatal(err)
	}

	if err := migrateContextSchema(db); err == nil {
		t.Fatal("migrateContextSchema reported success while the v3 migration could not apply")
	}

	// user_version must not have advanced past the failed migration, and v4 must
	// not have run: the admission table is the witness that it did not.
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 2 {
		t.Fatalf("user_version = %d after a failed v3, want it left at 2", version)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='chat_session_admissions'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("v4 ran after v3 failed; a failing intermediate migration must stop the chain")
	}
}
