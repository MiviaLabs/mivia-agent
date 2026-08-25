package storage

import (
	"context"
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

// Compact reclaims the free pages a prune leaves behind. SQLite never shrinks
// a database file on DELETE alone: the pages move to the freelist and the file
// keeps its size until a VACUUM rewrites it. Retention without this is only a
// bound on growth, not a reduction.
//
// VACUUM cannot run inside a transaction and rewrites the whole file, so it
// belongs in an explicit maintenance command, never on the open path.
func (s *SQLite) Compact(ctx context.Context) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.ExecContext(ctx, `VACUUM`); err != nil {
		return fmt.Errorf("vacuum store: %w", err)
	}
	return nil
}
