// Package storage provides the validation seam for durable agent events.
package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

type SQLite struct {
	db      *sql.DB
	path    string
	writeMu sync.Mutex
}

func OpenSQLite(path string) (*SQLite, error) {
	dir := path
	if last := strings.LastIndexAny(dir, "/\\"); last >= 0 {
		dir = dir[:last]
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create db directory %s: %w", dir, err)
	}
	db, err := sql.Open("sqlite", path)
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
	for _, q := range []string{`CREATE TABLE IF NOT EXISTS events (id TEXT PRIMARY KEY, run_id TEXT NOT NULL, sequence INTEGER NOT NULL, kind TEXT NOT NULL, payload BLOB NOT NULL, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, UNIQUE(run_id, sequence))`, `CREATE TABLE IF NOT EXISTS run_claims (run_id TEXT PRIMARY KEY, holder TEXT NOT NULL, acquired_at TEXT NOT NULL)`, `CREATE TABLE IF NOT EXISTS content (ref TEXT PRIMARY KEY, data BLOB NOT NULL, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`} {
		if _, err = db.Exec(q); err != nil {
			db.Close()
			return nil, err
		}
	}
	return &SQLite{db: db, path: path}, nil
}
func (s *SQLite) Backup(ctx context.Context, d string) error {
	_, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, d)
	return err
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
	return s.events(ctx, `SELECT id,run_id,sequence,kind,payload FROM events WHERE run_id=? ORDER BY sequence`, id)
}
func (s *SQLite) EventsSince(ctx context.Context, id string, after int) ([]Event, error) {
	return s.events(ctx, `SELECT id,run_id,sequence,kind,payload FROM events WHERE run_id=? AND sequence>? ORDER BY sequence`, id, after)
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
		if err := rows.Scan(&e.ID, &e.RunID, &e.Sequence, &e.Kind, &e.Payload); err != nil {
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
