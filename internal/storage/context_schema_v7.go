package storage

import (
	"database/sql"
	"fmt"
	"strings"
	"unicode"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

const liveWorktreeIndexSQL = `CREATE UNIQUE INDEX worktree_instances_live_name_idx ON worktree_instances(workspace_id,worktree) WHERE state != 'deleted'`
const worktreeInstanceIDIndexSQL = `CREATE UNIQUE INDEX worktree_instances_id_idx ON worktree_instances(workspace_id,instance_id)`

var contextV7SecondaryIndexes = map[string]string{
	"worktree_routes_worktree_instance_idx":   `CREATE INDEX worktree_routes_worktree_instance_idx ON worktree_routes(worktree,instance_id)`,
	"chat_session_dirs_worktree_instance_idx": `CREATE INDEX chat_session_dirs_worktree_instance_idx ON chat_session_dirs(worktree,instance_id)`,
	"chat_sessions_instance_idx":              `CREATE INDEX chat_sessions_instance_idx ON chat_sessions(instance_id)`,
	"context_sessions_instance_idx":           `CREATE INDEX context_sessions_instance_idx ON context_sessions(instance_id)`,
	"chat_session_admissions_instance_idx":    `CREATE INDEX chat_session_admissions_instance_idx ON chat_session_admissions(instance_id)`,
}

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
	if err := ensureWorktreeInstancesTable(tx); err != nil {
		return err
	}
	if err := ensureLiveWorktreeIndex(tx); err != nil {
		return err
	}
	if err := ensureExactContextIndex(tx, "worktree_instances_id_idx", worktreeInstanceIDIndexSQL); err != nil {
		return err
	}
	for _, table := range []string{"worktree_routes", "chat_session_dirs", "chat_sessions", "context_sessions", "chat_session_admissions"} {
		present, valid, err := contextInstanceColumnContract(tx, table)
		if err != nil {
			return err
		}
		if !present {
			if _, err := tx.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN instance_id TEXT", table)); err != nil {
				return err
			}
		} else if table != "worktree_routes" && !valid {
			return fmt.Errorf("%w: malformed %s.instance_id column", contextstate.ErrInvalidDTO, table)
		}
	}
	for name, definition := range contextV7SecondaryIndexes {
		if err := ensureExactContextIndex(tx, name, definition); err != nil {
			return err
		}
	}
	return nil
}

func contextInstanceColumnContract(tx *sql.Tx, table string) (bool, bool, error) {
	rows, err := tx.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, false, err
		}
		if name == "instance_id" {
			valid := strings.EqualFold(typ, "TEXT") && notNull == 0 && primaryKey == 0 && defaultValue == nil
			return true, valid, nil
		}
	}
	return false, false, rows.Err()
}

func ensureWorktreeInstancesTable(tx *sql.Tx) error {
	expected := worktreeInstancesTableSQL("worktree_instances")
	var definition string
	err := tx.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='worktree_instances'`).Scan(&definition)
	if err == sql.ErrNoRows {
		_, err = tx.Exec(expected)
		return err
	}
	if err != nil {
		return err
	}
	if normalizeSchemaDefinition(definition) == normalizeSchemaDefinition(expected) {
		return nil
	}
	if _, err := tx.Exec(worktreeInstancesTableSQL("worktree_instances_v7_repair")); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO worktree_instances_v7_repair(workspace_id,worktree,instance_id,canonical_path,state,created_at,updated_at) SELECT workspace_id,worktree,instance_id,canonical_path,state,created_at,updated_at FROM worktree_instances`); err != nil {
		return fmt.Errorf("repair worktree instances table: %w", err)
	}
	if _, err := tx.Exec(`DROP TABLE worktree_instances`); err != nil {
		return err
	}
	_, err = tx.Exec(`ALTER TABLE worktree_instances_v7_repair RENAME TO worktree_instances`)
	return err
}

func worktreeInstancesTableSQL(name string) string {
	return fmt.Sprintf(`CREATE TABLE %s(workspace_id TEXT NOT NULL, worktree TEXT NOT NULL, instance_id TEXT NOT NULL, canonical_path TEXT NOT NULL, state TEXT NOT NULL CHECK(state IN ('creating','active','deleting','deleted')), created_at TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY(workspace_id,worktree,instance_id))`, name)
}

func ensureLiveWorktreeIndex(tx *sql.Tx) error {
	return ensureExactContextIndex(tx, "worktree_instances_live_name_idx", liveWorktreeIndexSQL)
}

func ensureExactContextIndex(tx *sql.Tx, name, expectedDefinition string) error {
	var definition string
	err := tx.QueryRow(`SELECT sql FROM sqlite_master WHERE type='index' AND name=?`, name).Scan(&definition)
	if err == sql.ErrNoRows {
		_, err = tx.Exec(expectedDefinition)
		return err
	}
	if err != nil {
		return err
	}
	if normalizeSchemaDefinition(definition) == normalizeSchemaDefinition(expectedDefinition) {
		return nil
	}
	if _, err := tx.Exec(`DROP INDEX ` + name); err != nil {
		return err
	}
	if _, err := tx.Exec(expectedDefinition); err != nil {
		return fmt.Errorf("repair context index %s: %w", name, err)
	}
	return nil
}

func normalizeSchemaDefinition(definition string) string {
	var normalized strings.Builder
	inLiteral := false
	for _, r := range definition {
		if r == '\'' {
			inLiteral = !inLiteral
			normalized.WriteRune(r)
			continue
		}
		if inLiteral {
			normalized.WriteRune(r)
			continue
		}
		if unicode.IsSpace(r) || r == ';' {
			continue
		}
		normalized.WriteRune(unicode.ToLower(r))
	}
	return normalized.String()
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
