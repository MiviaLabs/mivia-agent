package storage

import (
	"database/sql"
	"fmt"
)

func ensureContextSchemaV7(db *sql.DB) error {
	return inMigrationTx(db, 7, "repair-contract", ensureContextSchemaV7Tx)
}

func applyContextSchemaV7(db *sql.DB) error {
	if err := inMigrationTx(db, 7, "apply", func(tx *sql.Tx) error {
		if err := ensureContextSchemaV7Tx(tx); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT OR REPLACE INTO context_schema_migrations(version, dirty) VALUES(7, 1)`)
		return err
	}); err != nil {
		return err
	}
	return inMigrationTx(db, 7, "finalize", func(tx *sql.Tx) error {
		return execAll(tx, migrationStatement{sql: `PRAGMA user_version = 7`}, migrationStatement{sql: `UPDATE context_schema_migrations SET dirty = 0 WHERE version = 7`})
	})
}

func ensureContextSchemaV7Tx(tx *sql.Tx) error {
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS worktree_instances(workspace_id TEXT NOT NULL, worktree TEXT NOT NULL, instance_id TEXT NOT NULL, canonical_path TEXT NOT NULL, state TEXT NOT NULL CHECK(state IN ('creating','active','deleting','deleted')), created_at TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY(workspace_id,worktree,instance_id))`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS worktree_instances_live_name_idx ON worktree_instances(workspace_id,worktree) WHERE state != 'deleted'`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS worktree_instances_id_idx ON worktree_instances(workspace_id,instance_id)`); err != nil {
		return err
	}
	for _, table := range []string{"worktree_routes", "chat_session_dirs", "chat_sessions", "context_sessions", "chat_session_admissions"} {
		present, err := contextTableColumnPresent(tx, table, "instance_id")
		if err != nil {
			return err
		}
		if !present {
			if _, err := tx.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN instance_id TEXT", table)); err != nil {
				return err
			}
		}
	}
	for _, table := range []string{"worktree_routes", "chat_session_dirs"} {
		if _, err := tx.Exec(fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s_worktree_instance_idx ON %s(worktree,instance_id)", table, table)); err != nil {
			return err
		}
	}
	for _, table := range []string{"chat_sessions", "context_sessions", "chat_session_admissions"} {
		if _, err := tx.Exec(fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s_instance_idx ON %s(instance_id)", table, table)); err != nil {
			return err
		}
	}
	return nil
}

func contextTableColumnPresent(tx *sql.Tx, table, column string) (bool, error) {
	rows, err := tx.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}
