package storage

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// seedContextCrashState builds a store interrupted between migration v's apply
// phase and its finalize phase: every migration below v fully applied, v's
// tables committed, the (v,1) row committed, and user_version still v-1. The
// real migration functions produce the DDL so the seed cannot drift from it.
func seedContextCrashState(t *testing.T, v int) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "crash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE context_schema_migrations(version INTEGER PRIMARY KEY, dirty INTEGER NOT NULL CHECK(dirty IN (0,1)))`); err != nil {
		t.Fatal(err)
	}
	apply := []func(*sql.DB) error{applyContextSchemaV1, applyContextSchemaV2, applyContextSchemaV3}
	for i := 0; i < v; i++ {
		if err := apply[i](db); err != nil {
			t.Fatalf("seed migration v%d: %v", i+1, err)
		}
	}
	if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, v-1)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE context_schema_migrations SET dirty = 1 WHERE version = ?`, v); err != nil {
		t.Fatal(err)
	}
	return db
}

// TestMigrateRecoversFromApplyPhaseCrash pins the only reachable interrupted
// state for every shipped migration. Before the repair re-drove the finalize
// phase, reopening such a store re-ran the migration and failed permanently on
// "table already exists".
func TestMigrateRecoversFromApplyPhaseCrash(t *testing.T) {
	for v := 1; v <= currentContextSchemaVersion; v++ {
		t.Run(fmt.Sprintf("v%d", v), func(t *testing.T) {
			db := seedContextCrashState(t, v)
			if err := migrateContextSchema(db); err != nil {
				t.Fatalf("migrateContextSchema after v%d crash: %v", v, err)
			}
			assertContextSchemaClean(t, db)
			// A second open must be a no-op, not a fresh failure.
			if err := migrateContextSchema(db); err != nil {
				t.Fatalf("reopen after repair: %v", err)
			}
			assertContextSchemaClean(t, db)
		})
	}
}

func assertContextSchemaClean(t *testing.T, db *sql.DB) {
	t.Helper()
	version, dirty, err := contextSchemaState(db)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if version != currentContextSchemaVersion {
		t.Fatalf("user_version = %d, want %d", version, currentContextSchemaVersion)
	}
	if dirty {
		t.Fatal("schema still dirty after repair")
	}
	for _, table := range []string{"context_sessions", "chat_sessions", "chat_session_admissions"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("table %s missing after repair", table)
		}
	}
}

// TestMigrateReportsDirtyWhenApplyPhaseNeverCommitted keeps the repair honest:
// a dirty row without its tables is not a finalize-phase interruption, so it
// must surface rather than be cleared.
func TestMigrateReportsDirtyWhenApplyPhaseNeverCommitted(t *testing.T) {
	db := seedContextCrashState(t, 3)
	if _, err := db.Exec(`DROP TABLE chat_session_admissions`); err != nil {
		t.Fatal(err)
	}
	err := migrateContextSchema(db)
	if err == nil {
		t.Fatal("migrateContextSchema must refuse a store whose apply phase never committed")
	}
	var dirty int
	if err := db.QueryRow(`SELECT dirty FROM context_schema_migrations WHERE version = 3`).Scan(&dirty); err != nil {
		t.Fatal(err)
	}
	if dirty != 1 {
		t.Fatalf("dirty = %d, want the flag left set", dirty)
	}
}

func TestContextVersionTableHasNoWitnessForAnUnknownVersion(t *testing.T) {
	// An unknown version has no table whose presence proves its apply phase
	// committed, so repair must decline rather than guess.
	if got := contextVersionTable(99); got != "" {
		t.Fatalf("contextVersionTable(99) = %q, want no witness", got)
	}
	store, _ := admissionStore(t)
	repaired, err := finalizeContextVersion(store.db, 99, 0)
	if err != nil || repaired {
		t.Fatalf("finalizeContextVersion(99) = (%v, %v), want (false, nil)", repaired, err)
	}
}

func TestFinalizeContextVersionReportsFailures(t *testing.T) {
	store, _ := admissionStore(t)
	if _, err := store.db.Exec(`CREATE TRIGGER block_repair BEFORE UPDATE ON context_schema_migrations BEGIN SELECT RAISE(ABORT, 'blocked'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := finalizeContextVersion(store.db, 3, 3); err == nil {
		t.Fatal("repair reported success while the dirty clear was blocked")
	}
	// The blocked repair must also surface through migrateContextSchema.
	if _, err := store.db.Exec(`UPDATE context_schema_migrations SET dirty = 1 WHERE version = 3`); err != nil {
		// The trigger blocks the UPDATE, so drive the row in directly.
		if _, err := store.db.Exec(`INSERT OR REPLACE INTO context_schema_migrations(version, dirty) VALUES(3, 1)`); err != nil {
			t.Fatal(err)
		}
	}
	if err := migrateContextSchema(store.db); err == nil {
		t.Fatal("migrate reported success while repair was blocked")
	}
	// An unreadable store has no witness, so repair declines rather than
	// reporting a failure it cannot act on.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if repaired, err := finalizeContextVersion(store.db, 3, 3); repaired || err != nil {
		t.Fatalf("finalizeContextVersion on a closed store = (%v, %v), want (false, nil)", repaired, err)
	}
	if contextVersionTablePresent(store.db, 3) {
		t.Fatal("a closed store reported a witness table")
	}
}

func TestRepairReportsAnUnreadableSchemaVersion(t *testing.T) {
	// Repair decides what to re-drive from the version it reads; without it
	// there is no safe default, so the failure must surface rather than be
	// treated as "nothing to repair".
	store, _ := admissionStore(t)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := repairContextSchema(store.db); err == nil {
		t.Fatal("repair proceeded without reading the schema version")
	}
}
