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
	closeOnce          sync.Once
	closeErr           error
	// usageWriteWG tracks in-flight fire-and-forget usage-event writes
	// (usageWriter.Record dispatches these off its caller's goroutine so a
	// contended write never extends a caller-held lock - see usage_events.go).
	// Close waits for it before checkpointing/closing the underlying db, so a
	// one-shot process (mivia compact, a single non-interactive chat turn)
	// cannot exit - and a test cannot remove its TempDir - while one of these
	// writes is still in flight or hasn't started running yet.
	usageWriteWG sync.WaitGroup
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
	for _, p := range []string{"PRAGMA journal_mode=WAL", "PRAGMA synchronous=NORMAL", "PRAGMA foreign_keys=ON", "PRAGMA busy_timeout=5000"} {
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

// Path returns the database file path this store was opened with - e.g. for
// internal/hub, which places hub.lock/hub.sock beside it.
func (s *SQLite) Path() string { return s.path }

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
	// A run whose only surviving events are deletion tombstones is free for
	// re-admission; any other surviving event means it is still live. The
	// claim probe and empty-payload check below are unchanged, so the whole
	// gate stays one transaction.
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE run_id=? AND kind <> ?)`, runID, KindRunDeleted).Scan(&exists); err != nil {
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

// inTx runs body in one transaction. A body failure and a commit failure are
// the same outcome - nothing landed - so they share one return path rather than
// two that cannot both be exercised.
func (s *SQLite) inTx(ctx context.Context, body func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err = body(tx); err == nil {
		err = tx.Commit()
	} else {
		_ = tx.Rollback()
	}
	return err
}

// Close folds the WAL back into the main database file before closing the
// connection pool. Without the checkpoint, WAL-mode leaves -wal/-shm files
// on disk that a caller's own directory cleanup (e.g. t.TempDir()) can race
// against, since a bare db.Close() gives no guarantee those files are done
// being written to by the time it returns. Idempotent like sql.DB.Close():
// a repeat call is a no-op that returns the first call's result, rather than
// a "database is closed" error from re-running the checkpoint query.
func (s *SQLite) Close() error {
	s.closeOnce.Do(func() {
		// Wait outside the Once body's mutation of closeErr: every write this
		// store dispatched asynchronously must land (or fail) before the
		// checkpoint below, or the checkpoint can run concurrently with (or
		// before) an in-flight INSERT, and the caller's later os.RemoveAll of
		// this store's directory can race a write that hasn't started yet.
		s.usageWriteWG.Wait()
		var busy, log, checkpointed int
		if err := s.db.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &checkpointed); err != nil {
			s.closeErr = err
		}
		if err := s.db.Close(); err != nil && s.closeErr == nil {
			s.closeErr = err
		}
	})
	return s.closeErr
}
