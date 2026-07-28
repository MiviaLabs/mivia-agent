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

	_ "modernc.org/sqlite"
)

var ErrDuplicate = errors.New("duplicate event")

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
	Count(context.Context) (int, error)
	ListRunIDs(context.Context) ([]string, error)
	Close() error
}

type Memory struct {
	mu     sync.RWMutex
	events map[string][]Event
	ids    map[string]struct{}
}

func NewMemory() *Memory { return &Memory{events: map[string][]Event{}, ids: map[string]struct{}{}} }

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
func cloneEvent(e Event) Event { e.Payload = append([]byte(nil), e.Payload...); return e }
