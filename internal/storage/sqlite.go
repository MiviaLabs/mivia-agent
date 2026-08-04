// Package storage provides the validation seam for durable agent events.
package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

type SQLite struct {
	db                 *sql.DB
	path               string
	writeMu            sync.Mutex
	failureMu          sync.Mutex
	contextFailureStep string
}

func OpenSQLite(path string) (*SQLite, error) {
	dir := path
	if last := strings.LastIndexAny(dir, "/\\"); last >= 0 {
		dir = dir[:last]
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create db directory %s: %w", dir, err)
	}
	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	for _, p := range []string{"PRAGMA journal_mode=WAL", "PRAGMA synchronous=FULL", "PRAGMA foreign_keys=ON", "PRAGMA busy_timeout=5000"} {
		if _, err = db.Exec(p); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", p, err)
		}
	}
	for _, q := range []string{`CREATE TABLE IF NOT EXISTS events (id TEXT PRIMARY KEY, run_id TEXT NOT NULL, sequence INTEGER NOT NULL, kind TEXT NOT NULL, payload BLOB NOT NULL, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, UNIQUE(run_id, sequence))`, `CREATE TABLE IF NOT EXISTS run_claims (run_id TEXT PRIMARY KEY, holder TEXT NOT NULL, acquired_at TEXT NOT NULL)`, `CREATE TABLE IF NOT EXISTS content (ref TEXT PRIMARY KEY, data BLOB NOT NULL, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`, `CREATE TABLE IF NOT EXISTS spool_grants (ref TEXT NOT NULL, principal TEXT NOT NULL, PRIMARY KEY (ref, principal))`} {
		if _, err = db.Exec(q); err != nil {
			db.Close()
			return nil, err
		}
	}
	if err := migrateContextSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	return &SQLite{db: db, path: path}, nil
}
func (s *SQLite) Backup(ctx context.Context, d string) error {
	_, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, d)
	return err
}

// Import copies missing rows from source into this database.
// Existing rows in this database take precedence.
func (s *SQLite) Import(ctx context.Context, source string) error {
	if filepath.Clean(source) == filepath.Clean(s.path) {
		return nil
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("open import connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("disable import foreign keys: %w", err)
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), `PRAGMA foreign_keys = ON`) }()
	if _, err := conn.ExecContext(ctx, `ATTACH DATABASE ? AS legacy`, source); err != nil {
		return fmt.Errorf("attach import database: %w", err)
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), `DETACH DATABASE legacy`) }()

	tables, err := importTableNames(ctx, conn)
	if err != nil {
		return err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin import: %w", err)
	}
	for _, table := range tables {
		columns, err := importColumns(ctx, conn, table)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if len(columns) == 0 {
			continue
		}
		quotedColumns := make([]string, len(columns))
		for i, column := range columns {
			quotedColumns[i] = quoteSQLiteIdentifier(column)
		}
		quotedTable := quoteSQLiteIdentifier(table)
		query := `INSERT OR IGNORE INTO ` + quotedTable + ` (` + strings.Join(quotedColumns, ",") + `) SELECT ` + strings.Join(quotedColumns, ",") + ` FROM legacy.` + quotedTable
		if _, err := tx.ExecContext(ctx, query); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("import table %s: %w", table, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit import: %w", err)
	}
	return nil
}

// ReassignWorkspace moves workspace-scoped rows into destination.
// It returns an error when a key conflicts.
func (s *SQLite) ReassignWorkspace(ctx context.Context, source, destination string) error {
	if source == destination {
		return nil
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("open workspace migration connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("disable workspace migration foreign keys: %w", err)
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), `PRAGMA foreign_keys = ON`) }()
	tables, err := workspaceScopedTables(ctx, conn)
	if err != nil {
		return err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin workspace migration: %w", err)
	}
	for _, table := range tables {
		query := `UPDATE OR IGNORE ` + quoteSQLiteIdentifier(table) + ` SET workspace_id = ? WHERE workspace_id = ?`
		if _, err := tx.ExecContext(ctx, query, destination, source); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("move workspace rows in %s: %w", table, err)
		}
		var remaining int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+quoteSQLiteIdentifier(table)+` WHERE workspace_id = ?`, source).Scan(&remaining); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("check workspace rows in %s: %w", table, err)
		}
		if remaining > 0 {
			_ = tx.Rollback()
			return fmt.Errorf("workspace conflict in %s", table)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit workspace migration: %w", err)
	}
	return nil
}

func workspaceScopedTables(ctx context.Context, conn *sql.Conn) ([]string, error) {
	rows, err := conn.QueryContext(ctx, `SELECT name FROM main.sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name <> 'context_schema_migrations' ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list workspace migration tables: %w", err)
	}
	var names []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("read workspace migration table: %w", err)
		}
		names = append(names, table)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate workspace migration tables: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close workspace migration table list: %w", err)
	}
	tables := make([]string, 0, len(names))
	for _, table := range names {
		columns, err := sqliteTableColumns(ctx, conn, "main", table)
		if err != nil {
			return nil, err
		}
		if columns["workspace_id"] {
			tables = append(tables, table)
		}
	}
	return tables, nil
}

func importTableNames(ctx context.Context, conn *sql.Conn) ([]string, error) {
	rows, err := conn.QueryContext(ctx, `SELECT source.name FROM legacy.sqlite_master source JOIN main.sqlite_master target ON target.type = 'table' AND target.name = source.name WHERE source.type = 'table' AND source.name NOT LIKE 'sqlite_%' AND source.name <> 'context_schema_migrations' ORDER BY source.name`)
	if err != nil {
		return nil, fmt.Errorf("list import tables: %w", err)
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return nil, fmt.Errorf("read import table: %w", err)
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate import tables: %w", err)
	}
	return tables, nil
}

func importColumns(ctx context.Context, conn *sql.Conn, table string) ([]string, error) {
	target, err := sqliteTableColumns(ctx, conn, "main", table)
	if err != nil {
		return nil, err
	}
	source, err := sqliteTableColumns(ctx, conn, "legacy", table)
	if err != nil {
		return nil, err
	}
	columns := make([]string, 0, len(target))
	for column := range target {
		if source[column] {
			columns = append(columns, column)
		}
	}
	return columns, nil
}

func sqliteTableColumns(ctx context.Context, conn *sql.Conn, schema, table string) (map[string]bool, error) {
	rows, err := conn.QueryContext(ctx, `PRAGMA `+schema+`.table_info(`+quoteSQLiteIdentifier(table)+`)`)
	if err != nil {
		return nil, fmt.Errorf("list import columns for %s: %w", table, err)
	}
	defer rows.Close()
	columns := make(map[string]bool)
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, fmt.Errorf("read import column for %s: %w", table, err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate import columns for %s: %w", table, err)
	}
	return columns, nil
}

func quoteSQLiteIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}
func (s *SQLite) Append(ctx context.Context, e Event) error {
	if len(e.Payload) == 0 {
		return fmt.Errorf("empty payload")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO events(id,run_id,sequence,kind,payload) VALUES(?,?,?,?,?)`, e.ID, e.RunID, e.Sequence, e.Kind, e.Payload)
	if err != nil {
		_ = tx.Rollback()
		if isConstraint(err) {
			return ErrDuplicate
		}
		return err
	}
	return tx.Commit()
}
func (s *SQLite) Events(ctx context.Context, id string) ([]Event, error) {
	return s.events(ctx, `SELECT id,run_id,sequence,kind,payload,rowid FROM events WHERE run_id=? ORDER BY sequence`, id)
}
func (s *SQLite) EventsSince(ctx context.Context, id string, after int) ([]Event, error) {
	return s.events(ctx, `SELECT id,run_id,sequence,kind,payload,rowid FROM events WHERE run_id=? AND sequence>? ORDER BY sequence`, id, after)
}
func (s *SQLite) DeleteRun(ctx context.Context, id string, through int) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM events WHERE run_id = ? AND sequence <= ?`, id, through); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM run_claims WHERE run_id = ?`, id); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
func (s *SQLite) events(ctx context.Context, q string, args ...any) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.RunID, &e.Sequence, &e.Kind, &e.Payload, &e.RowID); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
func (s *SQLite) Changes(ctx context.Context, after uint64) (map[string]int, uint64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT run_id, MAX(sequence), MAX(rowid) FROM events WHERE rowid > ? GROUP BY +run_id`, after)
	if err != nil {
		return nil, after, err
	}
	defer rows.Close()
	out := map[string]int{}
	cursor := after
	for rows.Next() {
		var id string
		var seq int
		var row uint64
		if err := rows.Scan(&id, &seq, &row); err != nil {
			return nil, after, err
		}
		out[id] = seq
		if row > cursor {
			cursor = row
		}
	}
	if err := rows.Err(); err != nil {
		return nil, after, err
	}
	return out, cursor, nil
}
func (s *SQLite) Count(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events`).Scan(&n)
	return n, err
}
func (s *SQLite) ListRunIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT run_id FROM events ORDER BY run_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func (s *SQLite) Close() error { return s.db.Close() }
func (s *SQLite) ClaimRun(ctx context.Context, id, h string) error {
	res, err := s.db.ExecContext(ctx, `INSERT INTO run_claims(run_id, holder, acquired_at) VALUES(?, ?, datetime('now')) ON CONFLICT(run_id) DO UPDATE SET acquired_at=excluded.acquired_at WHERE run_claims.holder = excluded.holder`, id, h)
	if err != nil {
		return fmt.Errorf("claim run %q: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrClaimHeld
	}
	return nil
}
func (s *SQLite) ReleaseClaim(ctx context.Context, id, h string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM run_claims WHERE run_id = ? AND holder = ?`, id, h)
	if err != nil {
		return fmt.Errorf("release claim %q: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrClaimNotHeld
	}
	return nil
}
func (s *SQLite) ClearClaim(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM run_claims WHERE run_id = ?`, id)
	return err
}
func (s *SQLite) PutContent(ctx context.Context, ref string, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO content(ref, data) VALUES(?, ?)`, ref, data)
	return err
}
func (s *SQLite) GetContent(ctx context.Context, ref string) ([]byte, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT data FROM content WHERE ref = ?`, ref).Scan(&data)
	if err == sql.ErrNoRows {
		return nil, ErrContentNotFound
	}
	return data, err
}

// GrantSpool durably records that principal holds a read grant on a remainder
// ref. INSERT OR IGNORE keeps the first grant for a (ref, principal) pair, so
// re-spooling the same ref for the same principal is idempotent.
func (s *SQLite) GrantSpool(ctx context.Context, ref, principal string) error {
	if ref == "" || principal == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO spool_grants(ref, principal) VALUES(?, ?)`, ref, principal)
	return err
}

// CheckSpoolGrant reports whether principal holds a durable read grant on ref.
func (s *SQLite) CheckSpoolGrant(ctx context.Context, ref, principal string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM spool_grants WHERE ref = ? AND principal = ?)`, ref, principal).Scan(&exists)
	return exists, err
}
func isConstraint(err error) bool {
	return err != nil && (contains(err.Error(), "constraint") || contains(err.Error(), "UNIQUE"))
}
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
