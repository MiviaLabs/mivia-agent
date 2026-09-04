package storage

// Worktree-instance and launch-route halves of the session catalog, split
// from chat_sessions.go to keep that file under the go-structure policy's
// file-size limit. Same package, so chat_sessions.go's ListSessions still
// calls hideCoveredWorktreeRoutes directly.

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// ListWorktreeSessions lists snapshots for one active worktree instance.
func (s *SQLite) ListWorktreeSessions(ctx context.Context, principal contextstate.Principal, instance contextstate.WorktreeInstance) ([]contextstate.SessionCatalogInfo, error) {
	if err := instance.Validate(); err != nil || instance.IsZero() {
		return nil, fmt.Errorf("%w: invalid worktree instance", contextstate.ErrInvalidDTO)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := requireActiveWorktreeTx(ctx, tx, principal, instance); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT c.name,c.model,c.provider,c.created_at,c.updated_at,c.turn_count,c.token_count,c.message_count,COALESCE(d.dir,''),COALESCE(d.worktree,'') FROM chat_sessions c LEFT JOIN chat_session_dirs d ON d.workspace_id=c.workspace_id AND d.subject_id=c.subject_id AND d.name=c.name WHERE c.workspace_id=? AND c.subject_id=? AND c.instance_id=?`, principal.WorkspaceID, principal.SubjectID, instance.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []contextstate.SessionCatalogInfo
	for rows.Next() {
		var info contextstate.SessionCatalogInfo
		if err := rows.Scan(&info.Name, &info.Model, &info.Provider, &info.CreatedAt, &info.UpdatedAt, &info.TurnCount, &info.TokenCount, &info.MessageCount, &info.Dir, &info.Worktree); err != nil {
			return nil, err
		}
		storageKey := info.Name
		if err := tx.QueryRowContext(ctx, `SELECT name FROM worktree_catalog_keys WHERE workspace_id=? AND subject_id=? AND instance_id=? AND entity='snapshot' AND storage_key=?`, principal.WorkspaceID, principal.SubjectID, instance.ID, storageKey).Scan(&info.Name); err != nil {
			return nil, err
		}
		info.WorktreeInstance = instance
		out = append(out, info)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	liveRows, err := tx.QueryContext(ctx, `SELECT cs.session_id,cs.title,cs.model,cs.provider,COALESCE(MIN(cc.created_at),CURRENT_TIMESTAMP),COALESCE(MAX(cc.created_at),CURRENT_TIMESTAMP),cs.source_sequence,COALESCE(d.dir,''),COALESCE(d.worktree,'') FROM context_sessions cs LEFT JOIN context_checkpoints cc ON cc.workspace_id=cs.workspace_id AND cc.subject_id=cs.subject_id AND cc.session_id=cs.session_id AND cc.complete=1 LEFT JOIN chat_session_dirs d ON d.workspace_id=cs.workspace_id AND d.subject_id=cs.subject_id AND d.name=cs.session_id WHERE cs.workspace_id=? AND cs.subject_id=? AND cs.instance_id=? AND cs.tombstoned=0 AND (cs.source_sequence>0 OR cs.title IS NOT NULL) GROUP BY cs.workspace_id,cs.subject_id,cs.session_id,cs.title,cs.model,cs.provider,cs.source_sequence,d.dir,d.worktree`, principal.WorkspaceID, principal.SubjectID, instance.ID)
	if err != nil {
		return nil, err
	}
	defer liveRows.Close()
	for liveRows.Next() {
		var info contextstate.SessionCatalogInfo
		var title sql.NullString
		if err := liveRows.Scan(&info.Name, &title, &info.Model, &info.Provider, &info.CreatedAt, &info.UpdatedAt, &info.MessageCount, &info.Dir, &info.Worktree); err != nil {
			return nil, err
		}
		info.Title = title.String
		info.SessionID = info.Name
		info.TurnCount = info.MessageCount
		info.WorktreeInstance = instance
		out = append(out, info)
	}
	if err := liveRows.Err(); err != nil {
		return nil, err
	}
	// Both arms above used to rely on the first arm's own SQL `ORDER BY
	// c.updated_at DESC`, leaving the second (live-session) arm unordered
	// and appended after it - and that first arm's ordering itself compared
	// only its own RFC3339Nano timestamps, never accounting for the second
	// arm's SQLite-CURRENT_TIMESTAMP-layout rows once merged. Sort the
	// combined slice the same tolerant way ListSessions does.
	sortSessionCatalogInfos(out)
	return out, tx.Commit()
}

func hideCoveredWorktreeRoutes(infos []contextstate.SessionCatalogInfo) []contextstate.SessionCatalogInfo {
	type routeKey struct {
		worktree string
		dir      string
	}
	covered := make(map[routeKey]bool)
	for _, info := range infos {
		// Only a row carrying the managed instance covers the route: it
		// resumes through the instance-scoped path, so the route row is
		// redundant next to it. A row with bare worktree metadata (legacy
		// pre-instance session) resumes PLAIN and must not hide the route
		// pseudo-row - that row is the only scoped-start affordance for
		// the worktree.
		if !info.WorktreeRoute && info.Worktree != "" && info.Dir != "" && !info.WorktreeInstance.IsZero() {
			covered[routeKey{worktree: info.Worktree, dir: info.Dir}] = true
		}
	}
	filtered := make([]contextstate.SessionCatalogInfo, 0, len(infos))
	for _, info := range infos {
		if info.WorktreeRoute && covered[routeKey{worktree: info.Worktree, dir: info.Dir}] {
			continue
		}
		filtered = append(filtered, info)
	}
	return filtered
}

var _ contextstate.WorktreeRouteCatalog = (*SQLite)(nil)

// SaveWorktreeRoute upserts the launch route for one mivia-managed worktree.
func (s *SQLite) SaveWorktreeRoute(ctx context.Context, principal contextstate.Principal, worktree, dir string) error {
	if err := principal.Validate(); err != nil {
		return err
	}
	if err := validateSessionCatalogName(worktree); err != nil || dir == "" || !contextstate.ValidSessionDir(dir) {
		return fmt.Errorf("%w: invalid worktree route", contextstate.ErrInvalidDTO)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE worktree_routes SET dir=?,updated_at=? WHERE workspace_id=? AND subject_id=? AND worktree=? AND instance_id IS NULL`, dir, now, principal.WorkspaceID, principal.SubjectID, worktree)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil || count > 0 {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO worktree_routes(workspace_id,subject_id,worktree,dir,created_at,updated_at,instance_id) VALUES(?,?,?,?,?,?,NULL)`, principal.WorkspaceID, principal.SubjectID, worktree, dir, now, now)
		return err
	})
}

// DeleteWorktreeRoute removes a launch route after its Git worktree is gone.
// It reports how many rows it removed.
func (s *SQLite) DeleteWorktreeRoute(ctx context.Context, principal contextstate.Principal, worktree string) (int64, error) {
	if err := principal.Validate(); err != nil {
		return 0, err
	}
	if err := validateSessionCatalogName(worktree); err != nil {
		return 0, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	result, err := s.db.ExecContext(ctx, `DELETE FROM worktree_routes WHERE workspace_id=? AND subject_id=? AND worktree=? AND instance_id IS NULL`, principal.WorkspaceID, principal.SubjectID, worktree)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// DeleteWorktreeRoutesByName removes every launch route for one worktree
// name, whether bound to an instance or legacy. It reports how many rows it
// removed. Call it only when no live instance owns the name, so no active
// route can be affected.
func (s *SQLite) DeleteWorktreeRoutesByName(ctx context.Context, principal contextstate.Principal, worktree string) (int64, error) {
	if err := principal.Validate(); err != nil {
		return 0, err
	}
	if err := validateSessionCatalogName(worktree); err != nil {
		return 0, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	result, err := s.db.ExecContext(ctx, `DELETE FROM worktree_routes WHERE workspace_id=? AND subject_id=? AND worktree=?`, principal.WorkspaceID, principal.SubjectID, worktree)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
