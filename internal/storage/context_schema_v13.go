package storage

import (
	"database/sql"
	"fmt"
)

// chatSessions.session_revision stamps a projection row ("id is id") with the
// live context session's session_revision at the moment the snapshot was
// saved. Without it, resolveProjection could not tell "this snapshot is the
// only content this session ever had" (session_revision unchanged since save
// - nothing has advanced the head, so there is no completed checkpoint to
// prefer) apart from "this snapshot predates a /clear or a later commit"
// (session_revision has since advanced) - both look identical as bare bytes.
// NULL for rows saved before this migration: their true recency is
// unknowable, so resolveProjection treats NULL exactly like "stale",
// matching the safe (if conservative) behavior that predates this column.
func applyContextSchemaV13(db *sql.DB) error {
	if err := inMigrationTx(db, 13, "apply", func(tx *sql.Tx) error {
		if err := ensureContextSchemaV13Tx(tx); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT OR REPLACE INTO context_schema_migrations(version, dirty) VALUES(13, 1)`)
		return err
	}); err != nil {
		return err
	}
	return inMigrationTx(db, 13, "finalize", func(tx *sql.Tx) error {
		return execAll(tx, migrationStatement{sql: `PRAGMA user_version = 13`}, migrationStatement{sql: `UPDATE context_schema_migrations SET dirty = 0 WHERE version = 13`})
	})
}

func ensureContextSchemaV13(db *sql.DB) error {
	if err := ensureContextSchemaV11(db); err != nil {
		return err
	}
	return inMigrationTx(db, 13, "repair-contract", ensureContextSchemaV13Tx)
}

func ensureContextSchemaV13Tx(tx *sql.Tx) error {
	rows, err := tx.Query(`PRAGMA table_info(chat_sessions)`)
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
		if name == "session_revision" {
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
		if err := tx.QueryRow(`SELECT count(*) FROM pragma_table_info('chat_sessions') WHERE name='session_revision'`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("unsupported chat session session_revision schema")
		}
		if _, err := tx.Exec(`ALTER TABLE chat_sessions ADD COLUMN session_revision INTEGER`); err != nil {
			return fmt.Errorf("add chat session session_revision: %w", err)
		}
	}
	_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS chat_sessions_v13_contract(ready INTEGER NOT NULL CHECK(ready = 1))`)
	return err
}
