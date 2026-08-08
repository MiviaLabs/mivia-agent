package storage

import (
	"database/sql"
	"fmt"
)

func applyContextSchemaV10(db *sql.DB) error {
	if err := inMigrationTx(db, 10, "apply", func(tx *sql.Tx) error {
		if err := ensureContextSchemaV10Tx(tx); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT OR REPLACE INTO context_schema_migrations(version, dirty) VALUES(10, 1)`)
		return err
	}); err != nil {
		return err
	}
	return inMigrationTx(db, 10, "finalize", func(tx *sql.Tx) error {
		return execAll(tx, migrationStatement{sql: `PRAGMA user_version = 10`}, migrationStatement{sql: `UPDATE context_schema_migrations SET dirty = 0 WHERE version = 10`})
	})
}

func applyContextSchemaV9AndV10(db *sql.DB) error {
	if err := applyContextSchemaV9(db); err != nil {
		return err
	}
	return applyContextSchemaV10(db)
}

func ensureContextSchemaV10(db *sql.DB) error {
	if err := ensureContextSchemaV9(db); err != nil {
		return err
	}
	return inMigrationTx(db, 10, "repair-contract", ensureContextSchemaV10Tx)
}

func ensureContextSchemaV10Tx(tx *sql.Tx) error {
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
		if name == "title" {
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
		if err := tx.QueryRow(`SELECT count(*) FROM pragma_table_info('context_sessions') WHERE name='title'`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("unsupported context session title schema")
		}
		if _, err := tx.Exec(`ALTER TABLE context_sessions ADD COLUMN title TEXT`); err != nil {
			return fmt.Errorf("add context session title: %w", err)
		}
	}
	_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS context_sessions_v10_contract(ready INTEGER NOT NULL CHECK(ready = 1))`)
	return err
}
