package storage

import (
	"database/sql"
	"fmt"
)

// context_sessions.lease_holder records WHO holds the lease lease_at marks
// fresh: an opaque same-host identity token minted by RenewLease
// (lease_liveness.go: version|host|boot_id|pid|starttime). ReclaimSession
// may take over a FRESH lease when this token proves the holder is dead
// (same host and boot, pid gone or reused) - a crashed process then blocks
// resume for zero seconds instead of the full lease TTL. NULL means the row
// predates this migration or the holder could not identify itself; both
// fall back to the pure-TTL wait, never to eviction.
func applyContextSchemaV15(db *sql.DB) error {
	if err := inMigrationTx(db, 15, "apply", func(tx *sql.Tx) error {
		if err := ensureContextSchemaV15Tx(tx); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT OR REPLACE INTO context_schema_migrations(version, dirty) VALUES(15, 1)`)
		return err
	}); err != nil {
		return err
	}
	return inMigrationTx(db, 15, "finalize", func(tx *sql.Tx) error {
		return execAll(tx, migrationStatement{sql: `PRAGMA user_version = 15`}, migrationStatement{sql: `UPDATE context_schema_migrations SET dirty = 0 WHERE version = 15`})
	})
}

func ensureContextSchemaV15(db *sql.DB) error {
	if err := ensureContextSchemaV14(db); err != nil {
		return err
	}
	return inMigrationTx(db, 15, "repair-contract", ensureContextSchemaV15Tx)
}

func ensureContextSchemaV15Tx(tx *sql.Tx) error {
	var count int
	if err := tx.QueryRow(`SELECT count(*) FROM pragma_table_info('context_sessions') WHERE name='lease_holder'`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		if _, err := tx.Exec(`ALTER TABLE context_sessions ADD COLUMN lease_holder TEXT`); err != nil {
			return fmt.Errorf("add context session lease_holder: %w", err)
		}
	}
	_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS context_sessions_v15_contract(ready INTEGER NOT NULL CHECK(ready = 1))`)
	return err
}
