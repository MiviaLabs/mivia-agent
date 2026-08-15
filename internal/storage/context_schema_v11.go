package storage

import (
	"database/sql"
	"fmt"
)

// chatSessionsV11BackfillSQL gives each catalog projection its own session id
// ("id is id, name is name"). Before v11 a chat_sessions row that projected a
// live context session used its session id as the catalog name; after v11 the
// projection keeps name as the catalog key and carries the session id in its
// own column. The backfill copies name into session_id only when a live
// context session with that session id exists for the same subject and the
// same worktree instance. The instance comparison is explicit and NULL-safe:
// NULL matches NULL only, so a projection bound to one instance never claims
// a live session of another.
const chatSessionsV11BackfillSQL = `UPDATE chat_sessions SET session_id = name WHERE session_id IS NULL AND EXISTS (SELECT 1 FROM context_sessions cs WHERE cs.workspace_id = chat_sessions.workspace_id AND cs.subject_id = chat_sessions.subject_id AND cs.session_id = chat_sessions.name AND cs.tombstoned = 0 AND ((cs.instance_id IS NULL AND chat_sessions.instance_id IS NULL) OR cs.instance_id = chat_sessions.instance_id))`

func applyContextSchemaV11(db *sql.DB) error {
	if err := inMigrationTx(db, 11, "apply", func(tx *sql.Tx) error {
		if err := ensureContextSchemaV11Tx(tx); err != nil {
			return err
		}
		if _, err := tx.Exec(chatSessionsV11BackfillSQL); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT OR REPLACE INTO context_schema_migrations(version, dirty) VALUES(11, 1)`)
		return err
	}); err != nil {
		return err
	}
	return inMigrationTx(db, 11, "finalize", func(tx *sql.Tx) error {
		return execAll(tx, migrationStatement{sql: `PRAGMA user_version = 11`}, migrationStatement{sql: `UPDATE context_schema_migrations SET dirty = 0 WHERE version = 11`})
	})
}

func ensureContextSchemaV11(db *sql.DB) error {
	if err := ensureContextSchemaV10(db); err != nil {
		return err
	}
	return inMigrationTx(db, 11, "repair-contract", ensureContextSchemaV11Tx)
}

func ensureContextSchemaV11Tx(tx *sql.Tx) error {
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
		if name == "session_id" {
			found = typ == "TEXT" && notNull == 0
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
		if err := tx.QueryRow(`SELECT count(*) FROM pragma_table_info('chat_sessions') WHERE name='session_id'`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("unsupported chat session session_id schema")
		}
		if _, err := tx.Exec(`ALTER TABLE chat_sessions ADD COLUMN session_id TEXT`); err != nil {
			return fmt.Errorf("add chat session session_id: %w", err)
		}
	}
	_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS chat_sessions_v11_contract(ready INTEGER NOT NULL CHECK(ready = 1))`)
	return err
}
