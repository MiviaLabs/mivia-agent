package storage

import (
	"database/sql"
	"fmt"
)

// context_sessions.lease_at stamps the unix-second time a live process last
// renewed write ownership of the row (SessionLeaseRenewer.RenewLease).
// ReclaimSession's conditional UPDATE checks this column so a live,
// actively-heartbeating owner is not silently evicted mid-turn by a second
// process that resumes the same session id: only a stale (or NULL,
// pre-migration) lease is reclaimable. NULL means either the row predates
// this migration or no heartbeat has renewed it yet - both treated as stale
// so a resumed process is not permanently blocked by an owner that will
// never renew.
func applyContextSchemaV14(db *sql.DB) error {
	if err := inMigrationTx(db, 14, "apply", func(tx *sql.Tx) error {
		if err := ensureContextSchemaV14Tx(tx); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT OR REPLACE INTO context_schema_migrations(version, dirty) VALUES(14, 1)`)
		return err
	}); err != nil {
		return err
	}
	return inMigrationTx(db, 14, "finalize", func(tx *sql.Tx) error {
		return execAll(tx, migrationStatement{sql: `PRAGMA user_version = 14`}, migrationStatement{sql: `UPDATE context_schema_migrations SET dirty = 0 WHERE version = 14`})
	})
}

func ensureContextSchemaV14(db *sql.DB) error {
	if err := ensureContextSchemaV13(db); err != nil {
		return err
	}
	return inMigrationTx(db, 14, "repair-contract", ensureContextSchemaV14Tx)
}

func ensureContextSchemaV14Tx(tx *sql.Tx) error {
	rows, err := tx.Query(`PRAGMA table_info(context_sessions)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var cid, notNull, key int
		var name, typ string
		var def any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &def, &key); err != nil {
			return err
		}
		if name == "lease_at" {
			found = typ == "INTEGER" && notNull == 0
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !found {
		if err := rows.Close(); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRow(`SELECT count(*) FROM pragma_table_info('context_sessions') WHERE name='lease_at'`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("unsupported context session lease_at schema")
		}
		if _, err := tx.Exec(`ALTER TABLE context_sessions ADD COLUMN lease_at INTEGER`); err != nil {
			return fmt.Errorf("add context session lease_at: %w", err)
		}
	}
	_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS context_sessions_v14_contract(ready INTEGER NOT NULL CHECK(ready = 1))`)
	return err
}
