package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// resetPreservedTables are the tables WipeAllExceptSchema and
// TableRowCounts never touch. context_schema_migrations is bookkeeping, not
// data: deleting its rows would make a subsequent OpenSQLite believe the
// store is unmigrated and re-run the whole ladder against an
// already-current schema (see migrateContextSchema).
var resetPreservedTables = map[string]bool{
	"context_schema_migrations": true,
}

// resettableTableNames returns every user table this store's schema owns,
// excluding resetPreservedTables and SQLite's own internal tables
// (sqlite_% - sqlite_sequence, sqlite_stat*, etc.). Reading sqlite_master at
// call time, rather than hardcoding the table list, keeps this correct
// across schema migrations without a second place to update.
func resettableTableNames(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite\_%' ESCAPE '\' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		if resetPreservedTables[name] {
			continue
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// TableRowCounts reports the row count of every table WipeAllExceptSchema
// would empty - the dry-run half of a destructive reset. It never writes.
func (s *SQLite) TableRowCounts(ctx context.Context) (map[string]int, error) {
	names, err := resettableTableNames(ctx, s.db)
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int, len(names))
	for _, name := range names {
		var n int
		if err := s.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %q`, name)).Scan(&n); err != nil {
			return nil, fmt.Errorf("count %s: %w", name, err)
		}
		counts[name] = n
	}
	return counts, nil
}

// WipeAllExceptSchema deletes every row from every table this store owns,
// except resetPreservedTables, in one transaction. It exists for a full
// reset that keeps only the separate memory store (memory.db / org.db,
// internal/memory - a different file, never opened by this method) and the
// store's own migrated schema.
//
// Several tables carry a foreign key onto context_sessions or
// context_payloads (context_checkpoints, context_payloads,
// context_source_events, context_audits, context_tombstones,
// context_imports - see context_schema_v1_v6.go), and that set has grown
// across migrations. Rather than hand-order every delete against a schema
// that keeps changing, this disables foreign_keys for the duration: SQLite
// only allows toggling that PRAGMA outside a transaction, so it is set on a
// single pinned connection before BEGIN and explicitly restored to ON after
// COMMIT, on that same connection, before the connection returns to the
// pool - a pooled connection some later caller reuses must never be the one
// still carrying foreign_keys=OFF.
func (s *SQLite) WipeAllExceptSchema(ctx context.Context) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("pin connection for reset: %w", err)
	}
	defer conn.Close()

	names, err := resettableTableNames(ctx, conn)
	if err != nil {
		return fmt.Errorf("enumerate tables for reset: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return fmt.Errorf("disable foreign_keys for reset: %w", err)
	}
	// Every return path from here must restore foreign_keys=ON before the
	// connection is released, success or failure - the delete order no
	// longer matters, but leaving enforcement off on a pooled connection
	// would.
	restoreForeignKeys := func() error {
		if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
			return fmt.Errorf("restore foreign_keys after reset: %w", err)
		}
		return nil
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return errors.Join(fmt.Errorf("begin reset transaction: %w", err), restoreForeignKeys())
	}
	for _, name := range names {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %q`, name)); err != nil {
			_ = tx.Rollback()
			return errors.Join(fmt.Errorf("wipe %s: %w", name, err), restoreForeignKeys())
		}
	}
	if err := tx.Commit(); err != nil {
		return errors.Join(fmt.Errorf("commit reset: %w", err), restoreForeignKeys())
	}
	return restoreForeignKeys()
}
