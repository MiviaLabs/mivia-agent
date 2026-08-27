package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// reapableWorktreeStates are the two lifecycle states a sweep may remove.
//
//   - 'deleted' is terminal: DeleteWorktreeInstance has already torn the
//     instance down and nothing routes to it again.
//   - 'creating' is an abandoned create. BeginWorktreeCreation writes it and
//     RegisterWorktreeInstance promotes it within one call, so a row still in
//     'creating' after the retention window is the residue of a crash.
//
// 'active' and 'deleting' are never candidates. An 'active' row may describe a
// live worktree the user still works in, and a 'deleting' row is mid-teardown:
// removing either would strand the routes that resolve to it.
var reapableWorktreeStates = []contextstate.WorktreeInstanceState{
	contextstate.WorktreeDeleted,
	contextstate.WorktreeCreating,
}

// worktreeGCCandidateSQL selects the aged instances one sweep may remove.
// These tables carry RFC3339Nano timestamps written by Go, not the
// CURRENT_TIMESTAMP layout the context tables use - see sqliteTimestampLayout.
const worktreeGCCandidateSQL = `SELECT workspace_id, worktree, instance_id FROM worktree_instances
    WHERE state IN (?, ?) AND updated_at <= ? ORDER BY workspace_id, worktree, instance_id LIMIT ?`

// PruneWorktreeInstances removes worktree instances in a reapable state whose
// last update is older than retention, the routes bound to them, and any route
// whose instance_id no longer resolves to an instance row.
//
// Nothing bounded these tables before. Each managed worktree leaves rows behind
// permanently: on one real store they held 70,841 instances across 57,077
// distinct workspace ids, against a handful of live worktrees, for 34 MB of
// rows plus 22 MB of indexes.
//
// Returns the instance and route counts removed. Bounded by limit and
// idempotent, so a caller loops until a sweep comes back short.
func (s *SQLite) PruneWorktreeInstances(ctx context.Context, now time.Time, retention time.Duration, limit int) (int, int, error) {
	if retention < 0 {
		return 0, 0, fmt.Errorf("%w: negative worktree retention", contextstate.ErrInvalidDTO)
	}
	if limit <= 0 || limit > maxCheckpointGCLimit {
		return 0, 0, fmt.Errorf("%w: invalid worktree GC limit", contextstate.ErrInvalidDTO)
	}
	cutoff := now.UTC().Add(-retention).Format(time.RFC3339Nano)
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return 0, 0, err
	}
	rows, err := tx.QueryContext(ctx, worktreeGCCandidateSQL, string(reapableWorktreeStates[0]), string(reapableWorktreeStates[1]), cutoff, limit)
	if err != nil {
		_ = tx.Rollback()
		return 0, 0, err
	}
	type candidate struct{ workspace, worktree, instance string }
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.workspace, &c.worktree, &c.instance); err != nil {
			rows.Close()
			_ = tx.Rollback()
			return 0, 0, err
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		_ = tx.Rollback()
		return 0, 0, err
	}
	rows.Close()

	// Routes go before their instance, so a failure mid-sweep can never leave
	// a route pointing at an instance row that is already gone. The whole
	// sweep is one transaction, so it is all-or-nothing either way.
	removedRoutes := 0
	for _, c := range candidates {
		result, err := tx.ExecContext(ctx, `DELETE FROM worktree_routes WHERE workspace_id=? AND worktree=? AND instance_id=?`, c.workspace, c.worktree, c.instance)
		if err != nil {
			_ = tx.Rollback()
			return 0, 0, err
		}
		n, _ := result.RowsAffected()
		removedRoutes += int(n)
		if _, err := tx.ExecContext(ctx, `DELETE FROM worktree_instances WHERE workspace_id=? AND worktree=? AND instance_id=?`, c.workspace, c.worktree, c.instance); err != nil {
			_ = tx.Rollback()
			return 0, 0, err
		}
	}

	// Orphaned routes: instance_id is set but resolves to nothing, so no
	// lookup can ever succeed through them. A NULL instance_id is the legacy
	// pre-instance binding, which is still valid and must not be touched.
	result, err := tx.ExecContext(ctx, `DELETE FROM worktree_routes WHERE instance_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM worktree_instances i
        WHERE i.workspace_id = worktree_routes.workspace_id AND i.worktree = worktree_routes.worktree
          AND i.instance_id = worktree_routes.instance_id)`)
	if err != nil {
		_ = tx.Rollback()
		return 0, 0, err
	}
	orphans, _ := result.RowsAffected()
	removedRoutes += int(orphans)

	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return len(candidates), removedRoutes, nil
}
