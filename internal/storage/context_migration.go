package storage

import (
	"database/sql"
	"fmt"
)

// applyContextMigration runs one DDL statement as a migration: schema in the
// first transaction, version bump and dirty clear in the second, matching the
// crash-recovery contract repairContextSchema depends on. A crash between the
// two leaves the schema committed and the dirty flag set, which is exactly what
// repairContextSchema knows how to recover.
func applyContextMigration(db *sql.DB, version int, ddl string) error {
	if err := inMigrationTx(db, version, "apply", func(tx *sql.Tx) error {
		return execAll(tx,
			migrationStatement{sql: ddl},
			migrationStatement{sql: `INSERT OR REPLACE INTO context_schema_migrations(version, dirty) VALUES(?, 1)`, args: []any{version}},
		)
	}); err != nil {
		return err
	}
	return inMigrationTx(db, version, "finalize", func(tx *sql.Tx) error {
		return execAll(tx,
			// PRAGMA takes no bind parameters; version is a compiled-in int.
			migrationStatement{sql: fmt.Sprintf(`PRAGMA user_version = %d`, version)},
			migrationStatement{sql: `UPDATE context_schema_migrations SET dirty = 0 WHERE version = ?`, args: []any{version}},
		)
	})
}

// migrationStatement is one statement of a migration phase.
type migrationStatement struct {
	sql  string
	args []any
}

// execAll runs statements in order and stops at the first failure. The caller's
// transaction wrapper decides what a failure means.
func execAll(tx *sql.Tx, statements ...migrationStatement) error {
	for _, statement := range statements {
		if _, err := tx.Exec(statement.sql, statement.args...); err != nil {
			return err
		}
	}
	return nil
}

// inMigrationTx runs one migration phase in its own transaction. A body failure
// and a commit failure are the same outcome - the phase did not land - so they
// share one rollback-and-report path rather than two that cannot both be
// exercised.
func inMigrationTx(db *sql.DB, version int, phase string, body func(*sql.Tx) error) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin context migration v%d %s: %w", version, phase, err)
	}
	if err = body(tx); err == nil {
		err = tx.Commit()
	} else {
		_ = tx.Rollback()
	}
	if err != nil {
		return fmt.Errorf("context migration v%d %s: %w", version, phase, err)
	}
	return nil
}
