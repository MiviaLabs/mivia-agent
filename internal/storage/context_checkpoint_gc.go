package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// sqliteTimestampLayout is the shape CURRENT_TIMESTAMP writes into every
// created_at column in this schema ("2026-08-15 01:56:49", UTC, no zone).
// A cutoff compared against those columns must use the same layout: the
// comparison is lexicographic, and RFC3339's 'T' sorts above ' ', so an
// RFC3339 cutoff would silently select every row.
const sqliteTimestampLayout = "2006-01-02 15:04:05"

// maxCheckpointGCLimit bounds one sweep, so a maintenance command cannot hold
// the write lock for an unbounded delete. Callers loop until the sweep returns
// fewer rows than the limit.
const maxCheckpointGCLimit = 10_000

// pruneSessionCheckpointsSQL deletes aged checkpoint rows that no reachable
// read can still reach. Three exemptions keep it safe, all evaluated per
// session (workspace_id, session_id, subject_id):
//
//   - the session's active_checkpoint_id, which chat_sessions.go reads for the
//     live context and which the active-checkpoint trigger requires to exist;
//   - the earliest complete checkpoint, which context_first_message.go reads;
//   - the newest :keep complete checkpoints, the resume floor.
//
// Incomplete checkpoints are never candidates: context_store_recovery.go reads
// them to resume an interrupted commit.
//
// Nothing holds a foreign key onto context_checkpoints, so a delete here needs
// no child sweep - unlike PruneContextPayloads, whose chunk rows must go first.
const pruneSessionCheckpointsSQL = `DELETE FROM context_checkpoints WHERE checkpoint_id IN (
    SELECT c.checkpoint_id FROM context_checkpoints c
    WHERE c.complete = 1
      AND c.created_at <= ?
      AND NOT EXISTS (
          SELECT 1 FROM context_sessions s
          WHERE s.workspace_id = c.workspace_id AND s.session_id = c.session_id
            AND s.subject_id = c.subject_id AND s.active_checkpoint_id = c.checkpoint_id)
      AND c.checkpoint_id <> COALESCE((
          SELECT e.checkpoint_id FROM context_checkpoints e
          WHERE e.workspace_id = c.workspace_id AND e.session_id = c.session_id
            AND e.subject_id = c.subject_id AND e.complete = 1
          ORDER BY e.source_start ASC, e.checkpoint_id ASC LIMIT 1), '')
      AND c.checkpoint_id NOT IN (
          SELECT n.checkpoint_id FROM context_checkpoints n
          WHERE n.workspace_id = c.workspace_id AND n.session_id = c.session_id
            AND n.subject_id = c.subject_id AND n.complete = 1
          ORDER BY n.source_end DESC, n.checkpoint_id DESC LIMIT ?)
    ORDER BY c.checkpoint_id
    LIMIT ?)`

// PruneSessionCheckpoints deletes complete checkpoint rows older than
// retention, keeping per session the active checkpoint, the earliest complete
// checkpoint, and the newest keep rows. It returns the number of rows removed.
//
// This is the only bound on context_checkpoints. Each row carries a full
// active_context blob, so without a sweep the table grows with every committed
// turn and never shrinks; on one real store it reached 144 MB of a 311 MB
// database in ten days.
//
// The sweep is idempotent and bounded by limit. A caller that wants the table
// fully swept loops until the result is below limit.
func (s *SQLite) PruneSessionCheckpoints(ctx context.Context, now time.Time, retention time.Duration, keep, limit int) (int, error) {
	if retention < 0 {
		return 0, fmt.Errorf("%w: negative checkpoint retention", contextstate.ErrInvalidDTO)
	}
	if keep < 0 {
		return 0, fmt.Errorf("%w: negative checkpoint keep floor", contextstate.ErrInvalidDTO)
	}
	if limit <= 0 || limit > maxCheckpointGCLimit {
		return 0, fmt.Errorf("%w: invalid checkpoint GC limit", contextstate.ErrInvalidDTO)
	}
	cutoff := now.UTC().Add(-retention).Format(sqliteTimestampLayout)
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, pruneSessionCheckpointsSQL, cutoff, keep, limit)
	if err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	count, _ := result.RowsAffected()
	return int(count), nil
}

// compactPageSize is the page size the rewrite adopts. The default 4096 is a
// poor fit for this schema: checkpoint and payload blobs average several KiB,
// and every byte past one page spills onto a chain of overflow pages. 8 KiB
// keeps far more of a row on its own page while staying well inside the range
// SQLite reads efficiently.
const compactPageSize = 8192

// Compact rewrites the database file. It does three things a running store
// cannot do for itself:
//
//   - reclaims the pages a prune freed. SQLite moves deleted pages to the
//     freelist and never shrinks the file on DELETE alone, so retention
//     without this bounds growth without reducing size;
//   - adopts compactPageSize, which is fixed when the file is created and can
//     only change across a VACUUM;
//   - switches auto_vacuum to INCREMENTAL, which needs the same full rebuild
//     because it adds a pointer map to every page.
//
// All of it runs on one pinned connection: PRAGMA page_size and auto_vacuum
// apply per connection, and a pooled VACUUM could otherwise land on a
// different connection that never saw them. The store leaves WAL mode for the
// rewrite, because SQLite refuses a page_size change in WAL, and returns to it
// afterwards.
//
// VACUUM cannot run inside a transaction and rewrites the whole file, so this
// belongs in an explicit maintenance command, never on the open path.
func (s *SQLite) Compact(ctx context.Context) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	// Leaving WAL mode needs every other connection to this file gone: each
	// one holds a read mark, and SQLite answers PRAGMA journal_mode=DELETE
	// with SQLITE_BUSY while any survives. Dropping both pools' idle limit to
	// zero closes their cached connections; the limits are restored below.
	s.db.SetMaxIdleConns(0)
	if s.writeDB != nil {
		s.writeDB.SetMaxIdleConns(0)
	}
	defer func() {
		s.db.SetMaxIdleConns(8)
		if s.writeDB != nil {
			s.writeDB.SetMaxIdleConns(4)
		}
	}()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("pin connection for compact: %w", err)
	}
	defer conn.Close()
	var mode string
	if err := conn.QueryRowContext(ctx, `PRAGMA journal_mode=DELETE`).Scan(&mode); err != nil {
		return fmt.Errorf("leave WAL for compact: %w", err)
	}
	// From here the store is out of WAL mode, so every return path must put it
	// back before reporting - a store left in DELETE mode would lose the
	// concurrency the rest of this package assumes.
	restoreWAL := func() error {
		var back string
		if err := conn.QueryRowContext(ctx, `PRAGMA journal_mode=WAL`).Scan(&back); err != nil {
			return fmt.Errorf("restore WAL after compact: %w", err)
		}
		return nil
	}
	for _, pragma := range []string{
		fmt.Sprintf(`PRAGMA page_size=%d`, compactPageSize),
		`PRAGMA auto_vacuum=INCREMENTAL`,
	} {
		if _, err := conn.ExecContext(ctx, pragma); err != nil {
			return errors.Join(fmt.Errorf("%s: %w", pragma, err), restoreWAL())
		}
	}
	if _, err := conn.ExecContext(ctx, `VACUUM`); err != nil {
		return errors.Join(fmt.Errorf("vacuum store: %w", err), restoreWAL())
	}
	return restoreWAL()
}
