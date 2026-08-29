package storage

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// TestMigrationV14ConvergesFromEveryPriorVersion confirms a store migrated
// step-by-step from v13 (and from fresh) both converge on v14 with
// context_sessions.lease_at present, matching the ladder-continuation +
// fresh-create convergence pattern TestMigrationV12AddsTokenUsageEventsTable
// already uses for v12 - test structure only; the migration body itself
// follows v10's ALTER-column-plus-witness-table shape, not v12's bare
// CREATE TABLE.
func TestMigrationV14ConvergesFromEveryPriorVersion(t *testing.T) {
	apply := []func(*sql.DB) error{
		applyContextSchemaV1, applyContextSchemaV2, applyContextSchemaV3, applyContextSchemaV4,
		applyContextSchemaV5, applyContextSchemaV6, applyContextSchemaV7, applyContextSchemaV8,
		applyContextSchemaV9, applyContextSchemaV10, applyContextSchemaV11, applyContextSchemaV12,
		applyContextSchemaV13,
	}
	tests := []struct {
		name     string
		versions int
	}{
		{name: "fresh", versions: 0},
		{name: "v13", versions: 13},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "context.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if test.versions > 0 {
				if _, err := db.Exec(`CREATE TABLE context_schema_migrations(version INTEGER PRIMARY KEY, dirty INTEGER NOT NULL CHECK(dirty IN (0,1)))`); err != nil {
					t.Fatal(err)
				}
				for i := 0; i < test.versions; i++ {
					if err := apply[i](db); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := migrateContextSchema(db); err != nil {
				t.Fatalf("migrateContextSchema from %s: %v", test.name, err)
			}
			var version int
			if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
				t.Fatal(err)
			}
			if version != currentContextSchemaVersion {
				t.Fatalf("user_version = %d after %s, want %d", version, test.name, currentContextSchemaVersion)
			}
			if !contextVersionTablePresent(db, 14) {
				t.Fatalf("context_sessions_v14_contract is missing after %s", test.name)
			}
			assertLeaseAtColumnWorks(t, db, test.name)
		})
	}
}

// assertLeaseAtColumnWorks confirms context_sessions.lease_at exists as a
// nullable INTEGER and round-trips a real value, proving the DDL actually
// accepts what ReclaimSession/RenewLease write - split out of
// TestMigrationV14ConvergesFromEveryPriorVersion to keep that function under
// the repo's function-length soft cap.
func assertLeaseAtColumnWorks(t *testing.T, db *sql.DB, label string) {
	t.Helper()
	var found bool
	rows, err := db.Query(`PRAGMA table_info(context_sessions)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, key int
		var name, typ string
		var def any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &def, &key); err != nil {
			t.Fatal(err)
		}
		if name == "lease_at" && typ == "INTEGER" && notNull == 0 {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatalf("context_sessions.lease_at column is missing or wrong-typed after %s", label)
	}
	if _, err := db.Exec(`INSERT INTO context_sessions(workspace_id,subject_id,session_id,capability_digest,session_revision,durable_revision,source_sequence,provider,model,binding_generation,lease_at) VALUES('ws','subj','sess','digest',0,0,0,'provider','model',1,1700000000)`); err != nil {
		t.Fatalf("insert with lease_at: %v", err)
	}
	var leaseAt int64
	if err := db.QueryRow(`SELECT lease_at FROM context_sessions WHERE session_id='sess'`).Scan(&leaseAt); err != nil {
		t.Fatal(err)
	}
	if leaseAt != 1700000000 {
		t.Fatalf("lease_at = %d, want 1700000000", leaseAt)
	}
}
