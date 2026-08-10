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
	"time"

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
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("open sqlite store: empty path")
	}
	// filepath.Dir yields "." for a bare filename, so the parent of a
	// separator-less relative path is the current directory and the database
	// opens as a regular file there - never as a directory named like the
	// file, which is what MkdirAll over the filename itself used to create
	// (DC-10). Separator-containing and absolute paths are unchanged.
	dir := filepath.Dir(path)
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
	if err := rejectNewerContextSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	for _, q := range []string{`CREATE TABLE IF NOT EXISTS events (id TEXT PRIMARY KEY, run_id TEXT NOT NULL, sequence INTEGER NOT NULL, kind TEXT NOT NULL, payload BLOB NOT NULL, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, UNIQUE(run_id, sequence))`, `CREATE TABLE IF NOT EXISTS run_claims (run_id TEXT PRIMARY KEY, holder TEXT NOT NULL, acquired_at TEXT NOT NULL, fence INTEGER NOT NULL DEFAULT 1)`, `CREATE TABLE IF NOT EXISTS content (ref TEXT PRIMARY KEY, data BLOB NOT NULL, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`, `CREATE TABLE IF NOT EXISTS spool_grants (ref TEXT NOT NULL, principal TEXT NOT NULL, PRIMARY KEY (ref, principal))`} {
		if _, err = db.Exec(q); err != nil {
			db.Close()
			return nil, err
		}
	}
	if _, err = db.Exec(`ALTER TABLE run_claims ADD COLUMN fence INTEGER NOT NULL DEFAULT 1`); err != nil && !strings.Contains(err.Error(), "duplicate column") {
		db.Close()
		return nil, fmt.Errorf("migrate run claim fence: %w", err)
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

func (s *SQLite) Append(ctx context.Context, e Event) error {
	return s.append(ctx, e, "", false)
}

func (s *SQLite) AppendBatch(ctx context.Context, events []Event) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for _, e := range events {
		if len(e.Payload) == 0 {
			_ = tx.Rollback()
			return fmt.Errorf("empty payload")
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO events(id,run_id,sequence,kind,payload) VALUES(?,?,?,?,?)`, e.ID, e.RunID, e.Sequence, e.Kind, e.Payload); err != nil {
			_ = tx.Rollback()
			if isConstraint(err) {
				return ErrDuplicate
			}
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLite) AppendBatchForNewRun(ctx context.Context, runID string, events []Event) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	for attempt := 0; ; attempt++ {
		err := s.appendBatchForNewRun(ctx, runID, events)
		if !isSQLiteBusy(err) || attempt == 7 {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt+1) * time.Millisecond):
		}
	}
}

func (s *SQLite) appendBatchForNewRun(ctx context.Context, runID string, events []Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	var exists int
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE run_id=?)`, runID).Scan(&exists); err != nil {
		_ = tx.Rollback()
		return err
	}
	if exists != 0 {
		_ = tx.Rollback()
		return ErrDuplicate
	}
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM run_claims WHERE run_id=?)`, runID).Scan(&exists); err != nil {
		_ = tx.Rollback()
		return err
	}
	if exists != 0 {
		_ = tx.Rollback()
		return ErrClaimHeld
	}
	for _, e := range events {
		if len(e.Payload) == 0 {
			_ = tx.Rollback()
			return fmt.Errorf("empty payload")
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO events(id,run_id,sequence,kind,payload) VALUES(?,?,?,?,?)`, e.ID, e.RunID, e.Sequence, e.Kind, e.Payload); err != nil {
			_ = tx.Rollback()
			if isConstraint(err) {
				return ErrDuplicate
			}
			return err
		}
	}
	return tx.Commit()
}

func isSQLiteBusy(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "SQLITE_BUSY") || strings.Contains(err.Error(), "database is locked"))
}

// AppendClaimed atomically checks the run claim and appends the event.
func (s *SQLite) AppendClaimed(ctx context.Context, e Event, holder string) error {
	return s.append(ctx, e, holder, true)
}

func (s *SQLite) AppendWithExistingClaim(ctx context.Context, e Event, holder string) error {
	if len(e.Payload) == 0 {
		return fmt.Errorf("empty payload")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO events(id,run_id,sequence,kind,payload) SELECT ?,?,?,?,? WHERE EXISTS (SELECT 1 FROM run_claims WHERE run_id=? AND holder=?)`, e.ID, e.RunID, e.Sequence, e.Kind, e.Payload, e.RunID, holder)
	if err == nil {
		var n int64
		n, err = res.RowsAffected()
		if err == nil && n == 0 {
			_ = tx.Rollback()
			return ErrClaimHeld
		}
	}
	if err != nil {
		_ = tx.Rollback()
		if isConstraint(err) {
			return ErrDuplicate
		}
		return err
	}
	return tx.Commit()
}

func (s *SQLite) append(ctx context.Context, e Event, holder string, claimed bool) error {
	if len(e.Payload) == 0 {
		return fmt.Errorf("empty payload")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if claimed {
		var res sql.Result
		res, err = tx.ExecContext(ctx, `INSERT INTO events(id,run_id,sequence,kind,payload) SELECT ?,?,?,?,? WHERE NOT EXISTS (SELECT 1 FROM run_claims WHERE run_id = ? AND (holder <> ? OR ? = ''))`, e.ID, e.RunID, e.Sequence, e.Kind, e.Payload, e.RunID, holder, holder)
		if err == nil {
			var n int64
			n, err = res.RowsAffected()
			if err == nil && n == 0 {
				_ = tx.Rollback()
				return ErrClaimHeld
			}
		}
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO events(id,run_id,sequence,kind,payload) VALUES(?,?,?,?,?)`, e.ID, e.RunID, e.Sequence, e.Kind, e.Payload)
	}
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

// AppendAndDeleteRun appends a deletion tombstone and deletes earlier events
// and the claim in one SQLite transaction.
func (s *SQLite) AppendAndDeleteRun(ctx context.Context, tombstone Event, claim Claim) error {
	if len(tombstone.Payload) == 0 {
		return fmt.Errorf("empty payload")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	var holder string
	var fence uint64
	err = tx.QueryRowContext(ctx, `SELECT holder, fence FROM run_claims WHERE run_id=?`, tombstone.RunID).Scan(&holder, &fence)
	if err != nil && err != sql.ErrNoRows {
		_ = tx.Rollback()
		return err
	}
	if err == nil && (claim.Holder == "" || holder != claim.Holder || (claim.Fence != 0 && fence != claim.Fence)) {
		_ = tx.Rollback()
		return ErrClaimHeld
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO events(id,run_id,sequence,kind,payload) VALUES(?,?,?,?,?)`, tombstone.ID, tombstone.RunID, tombstone.Sequence, tombstone.Kind, tombstone.Payload); err != nil {
		_ = tx.Rollback()
		if isConstraint(err) {
			return ErrDuplicate
		}
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM events WHERE run_id = ? AND sequence < ?`, tombstone.RunID, tombstone.Sequence); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM run_claims WHERE run_id = ?`, tombstone.RunID); err != nil {
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
	_, err := s.ClaimRunFenced(ctx, id, h)
	return err
}
func (s *SQLite) ClaimRunFenced(ctx context.Context, id, h string) (Claim, error) {
	if h == "" {
		return Claim{}, ErrClaimNotHeld
	}
	var claim Claim
	err := s.db.QueryRowContext(ctx, `INSERT INTO run_claims(run_id, holder, acquired_at, fence) VALUES(?, ?, strftime('%Y-%m-%dT%H:%M:%fZ','now'), 1) ON CONFLICT(run_id) DO UPDATE SET acquired_at=excluded.acquired_at WHERE run_claims.holder = excluded.holder RETURNING run_id, holder, acquired_at, fence`, id, h).Scan(&claim.RunID, &claim.Holder, &claim.AcquiredAt, &claim.Fence)
	if err == sql.ErrNoRows {
		return Claim{}, ErrClaimHeld
	}
	if err != nil {
		return Claim{}, fmt.Errorf("claim run %q: %w", id, err)
	}
	return claim, nil
}
func (s *SQLite) TakeoverClaim(ctx context.Context, id, h string) error {
	if h == "" {
		return ErrClaimNotHeld
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO run_claims(run_id, holder, acquired_at) VALUES(?, ?, datetime('now')) ON CONFLICT(run_id) DO UPDATE SET holder=excluded.holder, acquired_at=excluded.acquired_at`, id, h)
	if err != nil {
		return fmt.Errorf("take over claim %q: %w", id, err)
	}
	return nil
}

func (s *SQLite) TakeoverExpiredClaim(ctx context.Context, id, h string, maxAge time.Duration) error {
	_, err := s.TakeoverExpiredClaimFenced(ctx, id, h, maxAge)
	return err
}
func (s *SQLite) TakeoverExpiredClaimFenced(ctx context.Context, id, h string, maxAge time.Duration) (Claim, error) {
	if h == "" {
		return Claim{}, ErrClaimNotHeld
	}
	millis := maxAge.Milliseconds()
	var claim Claim
	err := s.db.QueryRowContext(ctx, `UPDATE run_claims SET holder = ?, acquired_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'), fence = fence + 1 WHERE run_id = ? AND julianday(acquired_at) <= julianday('now') - (? / 86400000.0) RETURNING run_id, holder, acquired_at, fence`, h, id, millis).Scan(&claim.RunID, &claim.Holder, &claim.AcquiredAt, &claim.Fence)
	if err == sql.ErrNoRows {
		var found int
		err := s.db.QueryRowContext(ctx, `SELECT 1 FROM run_claims WHERE run_id = ?`, id).Scan(&found)
		if err == sql.ErrNoRows {
			return Claim{}, ErrClaimNotHeld
		}
		if err != nil {
			return Claim{}, fmt.Errorf("read expired claim %q: %w", id, err)
		}
		return Claim{}, ErrClaimHeld
	}
	if err != nil {
		return Claim{}, fmt.Errorf("take over expired claim %q: %w", id, err)
	}
	return claim, nil
}

func (s *SQLite) AppendClaimedFenced(ctx context.Context, e Event, claim Claim) error {
	if len(e.Payload) == 0 {
		return fmt.Errorf("empty payload")
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO events(id,run_id,sequence,kind,payload) SELECT ?,?,?,?,? WHERE EXISTS (SELECT 1 FROM run_claims WHERE run_id=? AND holder=? AND fence=?)`, e.ID, e.RunID, e.Sequence, e.Kind, e.Payload, e.RunID, claim.Holder, claim.Fence)
	if err != nil {
		if isConstraint(err) {
			return ErrDuplicate
		}
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrClaimHeld
	}
	return nil
}

func (s *SQLite) ReleaseClaimFenced(ctx context.Context, claim Claim) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM run_claims WHERE run_id=? AND holder=? AND fence=?`, claim.RunID, claim.Holder, claim.Fence)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrClaimNotHeld
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
