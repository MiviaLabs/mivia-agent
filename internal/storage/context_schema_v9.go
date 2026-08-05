package storage

import "database/sql"

const worktreeRoutesInstanceIndexSQL = `CREATE UNIQUE INDEX worktree_routes_instance_idx ON worktree_routes(workspace_id,subject_id,worktree,instance_id)`

func applyContextSchemaV9(db *sql.DB) error {
	if err := inMigrationTx(db, 9, "apply", func(tx *sql.Tx) error {
		if err := ensureContextSchemaV9Tx(tx); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT OR REPLACE INTO context_schema_migrations(version, dirty) VALUES(9, 1)`)
		return err
	}); err != nil {
		return err
	}
	return inMigrationTx(db, 9, "finalize", func(tx *sql.Tx) error {
		return execAll(tx, migrationStatement{sql: `PRAGMA user_version = 9`}, migrationStatement{sql: `UPDATE context_schema_migrations SET dirty = 0 WHERE version = 9`})
	})
}

func ensureContextSchemaV9(db *sql.DB) error {
	if err := ensureContextSchemaV8(db); err != nil {
		return err
	}
	return inMigrationTx(db, 9, "repair-contract", ensureContextSchemaV9Tx)
}

func ensureContextSchemaV9Tx(tx *sql.Tx) error {
	ready, err := worktreeRoutesV9Ready(tx)
	if err != nil || ready {
		if err != nil {
			return err
		}
		return ensureWorktreeRoutesV9Witness(tx)
	}
	if _, err := tx.Exec(`CREATE TABLE worktree_routes_v9(
		workspace_id TEXT NOT NULL, subject_id TEXT NOT NULL, worktree TEXT NOT NULL,
		dir TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
		instance_id TEXT, PRIMARY KEY(workspace_id,subject_id,worktree,instance_id))`); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO worktree_routes_v9(workspace_id,subject_id,worktree,dir,created_at,updated_at,instance_id)
		SELECT workspace_id,subject_id,worktree,dir,created_at,updated_at,instance_id FROM worktree_routes`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE worktree_routes`); err != nil {
		return err
	}
	if _, err := tx.Exec(`ALTER TABLE worktree_routes_v9 RENAME TO worktree_routes`); err != nil {
		return err
	}
	return ensureWorktreeRoutesV9Witness(tx)
}

func ensureWorktreeRoutesV9Witness(tx *sql.Tx) error {
	if err := ensureExactContextIndex(tx, "worktree_routes_worktree_instance_idx", contextV7SecondaryIndexes["worktree_routes_worktree_instance_idx"]); err != nil {
		return err
	}
	if err := ensureExactContextIndex(tx, "worktree_routes_instance_idx", worktreeRoutesInstanceIndexSQL); err != nil {
		return err
	}
	_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS worktree_routes_v9_contract(ready INTEGER NOT NULL CHECK(ready = 1))`)
	return err
}

func worktreeRoutesV9Ready(tx *sql.Tx) (bool, error) {
	rows, err := tx.Query(`PRAGMA table_info(worktree_routes)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	type columnContract struct {
		name    string
		typ     string
		notNull int
		key     int
	}
	want := []columnContract{
		{name: "workspace_id", typ: "TEXT", notNull: 1, key: 1},
		{name: "subject_id", typ: "TEXT", notNull: 1, key: 2},
		{name: "worktree", typ: "TEXT", notNull: 1, key: 3},
		{name: "dir", typ: "TEXT", notNull: 1},
		{name: "created_at", typ: "TEXT", notNull: 1},
		{name: "updated_at", typ: "TEXT", notNull: 1},
		{name: "instance_id", typ: "TEXT", key: 4},
	}
	index := 0
	for rows.Next() {
		var cid, notNull, key int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &key); err != nil {
			return false, err
		}
		if index >= len(want) {
			return false, nil
		}
		expected := want[index]
		if cid != index || name != expected.name || typ != expected.typ || notNull != expected.notNull || key != expected.key || defaultValue != nil {
			return false, nil
		}
		index++
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return index == len(want), nil
}
