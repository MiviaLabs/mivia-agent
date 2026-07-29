// Package storage provides the validation seam for durable agent events.
package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrDuplicate       = errors.New("duplicate event")
	ErrClaimHeld       = errors.New("run claim held by another holder")
	ErrClaimNotHeld    = errors.New("run claim not held by this holder")
	ErrContentNotFound = errors.New("content not found")
)

// Claim represents an exclusive execution claim on a run.
type Claim struct {
	RunID      string
	Holder     string
	AcquiredAt string
}

type Event struct {
	ID       string
	RunID    string
	Sequence int
	Kind     string
	Payload  []byte
}

type Store interface {
	Append(context.Context, Event) error
	Events(context.Context, string) ([]Event, error)
	// EventsSince returns the events of a run whose sequence is strictly
	// greater than afterSequence, ordered by ascending sequence. It is the
	// bounded tail read that lets a reader catch up on another writer's
	// appends without replaying the whole history.
	EventsSince(ctx context.Context, runID string, afterSequence int) ([]Event, error)
	// Changes is the freshness probe for incremental catch-up. Given a cursor
	// previously returned by Changes (0 to start from the beginning), it
	// reports the highest sequence of every run appended to since that cursor,
	// together with the new cursor. Cost is proportional to the number of runs
	// that moved, not to the size of the history, so a caller that is already
	// up to date pays a constant-time probe.
	Changes(ctx context.Context, afterCursor uint64) (maxSequences map[string]int, cursor uint64, err error)
	// ClaimRun acquires an exclusive claim on a run for holder. Returns nil
	// if the claim was acquired. Returns ErrClaimHeld if another holder
	// already holds the claim. The same holder calling ClaimRun again
	// refreshes the claim successfully.
	ClaimRun(ctx context.Context, runID, holder string) error
	// ReleaseClaim releases the claim on a run. Only the current holder may
	// release. Returns ErrClaimNotHeld if the caller does not hold the claim.
	ReleaseClaim(ctx context.Context, runID, holder string) error
	// ClearClaim force-releases any claim on a run, regardless of holder.
	// Returns nil if no claim existed. Used during crash recovery to clear
	// stale claims on terminal runs.
	ClearClaim(ctx context.Context, runID string) error
	// PutContent stores raw bytes keyed by a content-addressed reference
	// (e.g. "ref:output:xxxx"). Idempotent for the same ref.
	PutContent(ctx context.Context, ref string, data []byte) error
	// GetContent retrieves bytes previously stored by PutContent.
	// Returns ErrContentNotFound if the ref is unknown.
	GetContent(ctx context.Context, ref string) ([]byte, error)
	Count(context.Context) (int, error)
	ListRunIDs(context.Context) ([]string, error)
	Close() error
}

type Memory struct {
	mu     sync.RWMutex
	events map[string][]Event
	ids    map[string]struct{}
	// order records the run ID of each append in order, so Changes can report
	// what moved since a cursor without scanning the whole history. The cursor
	// is an index into this slice.
	order  []string
	maxSeq map[string]int
	// claims tracks exclusive run execution claims.
	claims map[string]Claim // runID → claim
	// content maps content-addressed references to raw bytes.
	content map[string][]byte
}

func NewMemory() *Memory {
	return &Memory{events: map[string][]Event{}, ids: map[string]struct{}{}, maxSeq: map[string]int{}, claims: map[string]Claim{}, content: map[string][]byte{}}
}

func (m *Memory) Append(_ context.Context, e Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.ids[e.ID]; ok {
		return ErrDuplicate
	}
	if len(e.Payload) == 0 {
		return fmt.Errorf("empty payload")
	}
	m.events[e.RunID] = append(m.events[e.RunID], cloneEvent(e))
	m.ids[e.ID] = struct{}{}
	m.order = append(m.order, e.RunID)
	if e.Sequence > m.maxSeq[e.RunID] {
		m.maxSeq[e.RunID] = e.Sequence
	}
	return nil
}

func (m *Memory) Events(_ context.Context, runID string) ([]Event, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Event, len(m.events[runID]))
	for i, e := range m.events[runID] {
		out[i] = cloneEvent(e)
	}
	return out, nil
}

func (m *Memory) EventsSince(_ context.Context, runID string, afterSequence int) ([]Event, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Event
	for _, e := range m.events[runID] {
		if e.Sequence > afterSequence {
			out = append(out, cloneEvent(e))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Sequence < out[j].Sequence })
	return out, nil
}

func (m *Memory) Changes(_ context.Context, afterCursor uint64) (map[string]int, uint64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cursor := uint64(len(m.order))
	if afterCursor >= cursor {
		return nil, cursor, nil
	}
	out := map[string]int{}
	for _, runID := range m.order[afterCursor:] {
		if _, seen := out[runID]; seen {
			continue
		}
		out[runID] = m.maxSeq[runID]
	}
	return out, cursor, nil
}

func (m *Memory) Count(_ context.Context) (int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.ids), nil
}

func (m *Memory) ListRunIDs(_ context.Context) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]string, 0, len(m.events))
	for id := range m.events {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func (m *Memory) Close() error { return nil }

func (m *Memory) ClaimRun(_ context.Context, runID, holder string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.claims[runID]
	if ok && existing.Holder != holder {
		return ErrClaimHeld
	}
	m.claims[runID] = Claim{RunID: runID, Holder: holder, AcquiredAt: time.Now().UTC().Format(time.RFC3339)}
	return nil
}

func (m *Memory) ReleaseClaim(_ context.Context, runID, holder string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.claims[runID]
	if !ok {
		return ErrClaimNotHeld
	}
	if existing.Holder != holder {
		return ErrClaimNotHeld
	}
	delete(m.claims, runID)
	return nil
}

func (m *Memory) ClearClaim(_ context.Context, runID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.claims, runID)
	return nil
}

func (m *Memory) PutContent(_ context.Context, ref string, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.content[ref] = cloneBytes(data)
	return nil
}

func (m *Memory) GetContent(_ context.Context, ref string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	data, ok := m.content[ref]
	if !ok {
		return nil, ErrContentNotFound
	}
	return cloneBytes(data), nil
}

type SQLite struct {
	db      *sql.DB
	path    string
	writeMu sync.Mutex
}

func OpenSQLite(path string) (*SQLite, error) {
	// Ensure parent directory exists — sql.Open won't create it.
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
	for _, pragma := range []string{"PRAGMA journal_mode=WAL", "PRAGMA synchronous=FULL", "PRAGMA foreign_keys=ON", "PRAGMA busy_timeout=5000"} {
		if _, err = db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS events (
	 id TEXT PRIMARY KEY, run_id TEXT NOT NULL, sequence INTEGER NOT NULL,
	 kind TEXT NOT NULL, payload BLOB NOT NULL, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	 UNIQUE(run_id, sequence)
 )`)
	if err != nil {
		db.Close()
		return nil, err
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS run_claims (
	 run_id TEXT PRIMARY KEY, holder TEXT NOT NULL, acquired_at TEXT NOT NULL
 )`)
	if err != nil {
		db.Close()
		return nil, err
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS content (
	 ref TEXT PRIMARY KEY, data BLOB NOT NULL, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
 )`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &SQLite{db: db, path: path}, nil
}

// Backup writes a consistent SQLite snapshot to destination.
func (s *SQLite) Backup(ctx context.Context, destination string) error {
	_, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, destination)
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

func (s *SQLite) Events(ctx context.Context, runID string) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,run_id,sequence,kind,payload FROM events WHERE run_id=? ORDER BY sequence`, runID)
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

func (s *SQLite) EventsSince(ctx context.Context, runID string, afterSequence int) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,run_id,sequence,kind,payload FROM events WHERE run_id=? AND sequence>? ORDER BY sequence`, runID, afterSequence)
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

// Changes uses the table's rowid as the cursor. Rows are only ever inserted
// (never deleted or rewritten), and SQLite serialises writers, so rowid is a
// monotonic append position: everything appended after cursor N has a rowid
// greater than N.
func (s *SQLite) Changes(ctx context.Context, afterCursor uint64) (map[string]int, uint64, error) {
	// `GROUP BY +run_id` is deliberate: plain `GROUP BY run_id` makes SQLite
	// scan the whole UNIQUE(run_id, sequence) covering index, turning the probe
	// into O(history). The unary plus keeps that index out of the grouping so
	// the planner uses the rowid range instead — SEARCH events USING INTEGER
	// PRIMARY KEY (rowid>?) — which is O(rows appended since the cursor).
	rows, err := s.db.QueryContext(ctx,
		`SELECT run_id, MAX(sequence), MAX(rowid) FROM events WHERE rowid > ? GROUP BY +run_id`, afterCursor)
	if err != nil {
		return nil, afterCursor, err
	}
	defer rows.Close()
	out := map[string]int{}
	cursor := afterCursor
	for rows.Next() {
		var (
			runID    string
			maxSeq   int
			maxRowID uint64
		)
		if err := rows.Scan(&runID, &maxSeq, &maxRowID); err != nil {
			return nil, afterCursor, err
		}
		out[runID] = maxSeq
		if maxRowID > cursor {
			cursor = maxRowID
		}
	}
	if err := rows.Err(); err != nil {
		return nil, afterCursor, err
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

func (s *SQLite) ClaimRun(ctx context.Context, runID, holder string) error {
	// UPSERT semantics: if the same holder already holds the claim, refresh it.
	// If a different holder holds it, refuse.
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO run_claims(run_id, holder, acquired_at) VALUES(?, ?, datetime('now'))
		 ON CONFLICT(run_id) DO UPDATE SET acquired_at=excluded.acquired_at
		 WHERE run_claims.holder = excluded.holder`,
		runID, holder)
	if err != nil {
		return fmt.Errorf("claim run %q: %w", runID, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// The ON CONFLICT WHERE clause did not match — someone else holds it.
		return ErrClaimHeld
	}
	return nil
}

func (s *SQLite) ReleaseClaim(ctx context.Context, runID, holder string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM run_claims WHERE run_id = ? AND holder = ?`, runID, holder)
	if err != nil {
		return fmt.Errorf("release claim %q: %w", runID, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrClaimNotHeld
	}
	return nil
}

func (s *SQLite) ClearClaim(ctx context.Context, runID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM run_claims WHERE run_id = ?`, runID)
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
func cloneEvent(e Event) Event   { e.Payload = append([]byte(nil), e.Payload...); return e }
func cloneBytes(b []byte) []byte { out := make([]byte, len(b)); copy(out, b); return out }
