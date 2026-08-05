package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

func validateSessionCatalogName(name string) error {
	if strings.TrimSpace(name) == "" || len(name) > contextstate.MaxIdentifierBytes || strings.ContainsAny(name, "/\\\x00") {
		return fmt.Errorf("%w: invalid session name", contextstate.ErrInvalidDTO)
	}
	return nil
}

func worktreeCatalogName(name string, instance contextstate.WorktreeInstance) string {
	if instance.IsZero() {
		return name
	}
	return "worktree-" + instance.ID + "-" + name
}

var _ contextstate.SessionCatalog = (*SQLite)(nil)

func (s *SQLite) SaveSession(ctx context.Context, principal contextstate.Principal, name string, messages []byte, model, provider string, turns, tokens, messageCount int, opts contextstate.SessionSaveOptions) error {
	if err := principal.Validate(); err != nil {
		return err
	}
	if err := validateSessionCatalogName(name); err != nil {
		return err
	}
	if len(messages) == 0 || contextstate.Exceeds(len(messages), contextstate.CurrentLimits().SessionStateBytes) {
		return fmt.Errorf("%w: invalid session message payload", contextstate.ErrInvalidDTO)
	}
	if !contextstate.ValidSessionDir(opts.Dir) || !contextstate.ValidSessionDir(opts.Worktree) {
		return fmt.Errorf("%w: invalid session directory metadata", contextstate.ErrInvalidDTO)
	}
	if err := opts.WorktreeInstance.Validate(); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	// One transaction: the snapshot row and its directory record either both
	// land or neither does, so a torn write cannot leave a snapshot whose
	// restore metadata is missing or points at an older location.
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if err := requireActiveWorktreeTx(ctx, tx, principal, opts.WorktreeInstance); err != nil {
			return err
		}
		if opts.WorktreeInstance.IsZero() {
			if err := rejectManagedCatalogKey(ctx, tx, principal, name); err != nil {
				return err
			}
		}
		storedName := name
		if !opts.WorktreeInstance.IsZero() {
			var err error
			storedName, err = worktreeCatalogKeyTx(ctx, tx, principal, opts.WorktreeInstance, "snapshot", name)
			if err != nil {
				return err
			}
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO chat_sessions(workspace_id,subject_id,name,model,provider,messages,created_at,updated_at,turn_count,token_count,message_count,instance_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(workspace_id,subject_id,name) DO UPDATE SET model=excluded.model,provider=excluded.provider,messages=excluded.messages,updated_at=excluded.updated_at,turn_count=excluded.turn_count,token_count=excluded.token_count,message_count=excluded.message_count WHERE chat_sessions.instance_id IS excluded.instance_id`, principal.WorkspaceID, principal.SubjectID, storedName, model, provider, messages, now, now, turns, tokens, messageCount, nullableText(opts.WorktreeInstance.ID))
		if err != nil {
			return err
		}
		if err := requireCatalogMutation(result); err != nil {
			return err
		}
		result, err = tx.ExecContext(ctx, upsertSessionDirSQL, principal.WorkspaceID, principal.SubjectID, storedName, opts.Dir, opts.Worktree, nullableText(opts.WorktreeInstance.ID))
		if err != nil {
			return err
		}
		return requireCatalogMutation(result)
	})
}

func (s *SQLite) LoadSession(ctx context.Context, principal contextstate.Principal, name string) ([]byte, contextstate.SessionCatalogInfo, error) {
	if err := principal.Validate(); err != nil {
		return nil, contextstate.SessionCatalogInfo{}, err
	}
	if err := validateSessionCatalogName(name); err != nil {
		return nil, contextstate.SessionCatalogInfo{}, err
	}
	if err := rejectManagedCatalogKey(ctx, s.db, principal, name); err != nil {
		return nil, contextstate.SessionCatalogInfo{}, err
	}
	var payload []byte
	var info contextstate.SessionCatalogInfo
	err := s.db.QueryRowContext(ctx, `SELECT c.name,c.model,c.provider,c.messages,c.created_at,c.updated_at,c.turn_count,c.token_count,c.message_count,COALESCE(d.dir,''),COALESCE(d.worktree,'') FROM chat_sessions c LEFT JOIN chat_session_dirs d ON d.workspace_id=c.workspace_id AND d.subject_id=c.subject_id AND d.name=c.name WHERE c.workspace_id=? AND c.subject_id=? AND c.name=? AND c.instance_id IS NULL`, principal.WorkspaceID, principal.SubjectID, name).Scan(&info.Name, &info.Model, &info.Provider, &payload, &info.CreatedAt, &info.UpdatedAt, &info.TurnCount, &info.TokenCount, &info.MessageCount, &info.Dir, &info.Worktree)
	if err == sql.ErrNoRows {
		var sourceCount int
		err = s.db.QueryRowContext(ctx, `SELECT cs.session_id,cs.model,cs.provider,COALESCE((SELECT active_context FROM context_checkpoints WHERE checkpoint_id=cs.active_checkpoint_id AND complete=1),?),COALESCE((SELECT MIN(created_at) FROM context_checkpoints WHERE session_id=cs.session_id),CURRENT_TIMESTAMP),COALESCE((SELECT MAX(created_at) FROM context_checkpoints WHERE session_id=cs.session_id),CURRENT_TIMESTAMP),source_sequence,COALESCE(d.dir,''),COALESCE(d.worktree,'') FROM context_sessions cs LEFT JOIN chat_session_dirs d ON d.workspace_id=cs.workspace_id AND d.subject_id=cs.subject_id AND d.name=cs.session_id WHERE cs.workspace_id=? AND cs.subject_id=? AND cs.session_id=? AND cs.tombstoned=0 AND cs.instance_id IS NULL`, []byte("[]"), principal.WorkspaceID, principal.SubjectID, name).Scan(&info.SessionID, &info.Model, &info.Provider, &payload, &info.CreatedAt, &info.UpdatedAt, &sourceCount, &info.Dir, &info.Worktree)
		if err == sql.ErrNoRows {
			return nil, contextstate.SessionCatalogInfo{}, contextstate.ErrSessionNotFound
		}
		if err != nil {
			return nil, contextstate.SessionCatalogInfo{}, err
		}
		info.Name = info.SessionID
		info.MessageCount = sourceCount
		info.TurnCount = sourceCount
		info.TokenCount = 0
		return append([]byte(nil), payload...), info, nil
	}
	if err != nil {
		return nil, contextstate.SessionCatalogInfo{}, err
	}
	return append([]byte(nil), payload...), info, nil
}

func (s *SQLite) ListSessions(ctx context.Context, principal contextstate.Principal) ([]contextstate.SessionCatalogInfo, error) {
	if err := principal.Validate(); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT c.name,c.model,c.provider,'',c.created_at,c.updated_at,c.turn_count,c.token_count,c.message_count,COALESCE(d.dir,''),COALESCE(d.worktree,''),0,'' FROM chat_sessions c LEFT JOIN chat_session_dirs d ON d.workspace_id=c.workspace_id AND d.subject_id=c.subject_id AND d.name=c.name WHERE c.workspace_id=? AND c.subject_id=? AND c.instance_id IS NULL UNION ALL SELECT t.session_id,t.model,t.provider,t.session_id,t.created,t.updated,t.source_sequence,0,t.source_sequence,COALESCE(d.dir,''),COALESCE(d.worktree,''),0,'' FROM (SELECT cs.workspace_id,cs.subject_id,cs.session_id,cs.model,cs.provider,cs.source_sequence,COALESCE(MIN(cc.created_at),CURRENT_TIMESTAMP) AS created,COALESCE(MAX(cc.created_at),CURRENT_TIMESTAMP) AS updated FROM context_sessions cs LEFT JOIN context_checkpoints cc ON cc.session_id=cs.session_id AND cc.workspace_id=cs.workspace_id AND cc.subject_id=cs.subject_id AND cc.complete=1 WHERE cs.workspace_id=? AND cs.subject_id=? AND cs.tombstoned=0 AND cs.source_sequence>0 AND cs.instance_id IS NULL AND NOT EXISTS (SELECT 1 FROM chat_sessions c WHERE c.workspace_id=cs.workspace_id AND c.subject_id=cs.subject_id AND c.name=cs.session_id) GROUP BY cs.workspace_id,cs.subject_id,cs.session_id,cs.model,cs.provider,cs.source_sequence) t LEFT JOIN chat_session_dirs d ON d.workspace_id=t.workspace_id AND d.subject_id=t.subject_id AND d.name=t.session_id UNION ALL SELECT 'worktree:' || r.worktree,'','', '',r.created_at,r.updated_at,0,0,0,r.dir,r.worktree,1,COALESCE(r.instance_id,'') FROM worktree_routes r WHERE r.workspace_id=? AND r.subject_id=? AND (r.instance_id IS NULL OR EXISTS (SELECT 1 FROM worktree_instances wi WHERE wi.workspace_id=r.workspace_id AND wi.worktree=r.worktree AND wi.instance_id=r.instance_id AND wi.state='active')) ORDER BY 6 DESC,1`, principal.WorkspaceID, principal.SubjectID, principal.WorkspaceID, principal.SubjectID, principal.WorkspaceID, principal.SubjectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []contextstate.SessionCatalogInfo
	for rows.Next() {
		var info contextstate.SessionCatalogInfo
		var instanceID string
		if err := rows.Scan(&info.Name, &info.Model, &info.Provider, &info.SessionID, &info.CreatedAt, &info.UpdatedAt, &info.TurnCount, &info.TokenCount, &info.MessageCount, &info.Dir, &info.Worktree, &info.WorktreeRoute, &instanceID); err != nil {
			return nil, err
		}
		if instanceID != "" {
			info.WorktreeInstance = contextstate.WorktreeInstance{Worktree: info.Worktree, ID: instanceID}
		}
		out = append(out, info)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return hideCoveredWorktreeRoutes(out), nil
}

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
	rows, err := tx.QueryContext(ctx, `SELECT c.name,c.model,c.provider,c.created_at,c.updated_at,c.turn_count,c.token_count,c.message_count,COALESCE(d.dir,''),COALESCE(d.worktree,'') FROM chat_sessions c LEFT JOIN chat_session_dirs d ON d.workspace_id=c.workspace_id AND d.subject_id=c.subject_id AND d.name=c.name WHERE c.workspace_id=? AND c.subject_id=? AND c.instance_id=? ORDER BY c.updated_at DESC`, principal.WorkspaceID, principal.SubjectID, instance.ID)
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
	liveRows, err := tx.QueryContext(ctx, `SELECT cs.session_id,cs.model,cs.provider,COALESCE(MIN(cc.created_at),CURRENT_TIMESTAMP),COALESCE(MAX(cc.created_at),CURRENT_TIMESTAMP),cs.source_sequence,COALESCE(d.dir,''),COALESCE(d.worktree,'') FROM context_sessions cs LEFT JOIN context_checkpoints cc ON cc.workspace_id=cs.workspace_id AND cc.subject_id=cs.subject_id AND cc.session_id=cs.session_id AND cc.complete=1 LEFT JOIN chat_session_dirs d ON d.workspace_id=cs.workspace_id AND d.subject_id=cs.subject_id AND d.name=cs.session_id WHERE cs.workspace_id=? AND cs.subject_id=? AND cs.instance_id=? AND cs.tombstoned=0 AND cs.source_sequence>0 GROUP BY cs.workspace_id,cs.subject_id,cs.session_id,cs.model,cs.provider,cs.source_sequence,d.dir,d.worktree`, principal.WorkspaceID, principal.SubjectID, instance.ID)
	if err != nil {
		return nil, err
	}
	defer liveRows.Close()
	for liveRows.Next() {
		var info contextstate.SessionCatalogInfo
		if err := liveRows.Scan(&info.Name, &info.Model, &info.Provider, &info.CreatedAt, &info.UpdatedAt, &info.MessageCount, &info.Dir, &info.Worktree); err != nil {
			return nil, err
		}
		info.SessionID = info.Name
		info.TurnCount = info.MessageCount
		info.WorktreeInstance = instance
		out = append(out, info)
	}
	if err := liveRows.Err(); err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

func hideCoveredWorktreeRoutes(infos []contextstate.SessionCatalogInfo) []contextstate.SessionCatalogInfo {
	type routeKey struct {
		worktree string
		dir      string
	}
	covered := make(map[routeKey]bool)
	for _, info := range infos {
		if !info.WorktreeRoute && info.Worktree != "" && info.Dir != "" {
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
func (s *SQLite) DeleteWorktreeRoute(ctx context.Context, principal contextstate.Principal, worktree string) error {
	if err := principal.Validate(); err != nil {
		return err
	}
	if err := validateSessionCatalogName(worktree); err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM worktree_routes WHERE workspace_id=? AND subject_id=? AND worktree=? AND instance_id IS NULL`, principal.WorkspaceID, principal.SubjectID, worktree)
	return err
}

func (s *SQLite) DeleteSessionSnapshot(ctx context.Context, principal contextstate.Principal, name string) error {
	if err := principal.Validate(); err != nil {
		return err
	}
	if err := validateSessionCatalogName(name); err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	count, err := s.deleteSessionSnapshotRow(ctx, principal, name)
	if err != nil {
		return err
	}
	if count == 0 {
		return s.deleteContextSessionOrOrphanedAdmission(ctx, principal, name)
	}
	return nil
}

// deleteContextSessionOrOrphanedAdmission handles the case the snapshot delete
// matched nothing. Either the name is context-backed - and the retention
// transaction owns reclaiming its admission record atomically with the
// tombstone - or nothing owns the name at all, in which case any admission row
// left behind is an orphan that no other path will ever reclaim.
func (s *SQLite) deleteContextSessionOrOrphanedAdmission(ctx context.Context, principal contextstate.Principal, name string) error {
	err := s.deleteCatalogContextSession(ctx, principal, name)
	if !errors.Is(err, contextstate.ErrSessionNotFound) {
		return err
	}
	if _, sweepErr := s.db.ExecContext(ctx, deleteSessionAdmissionSQL, principal.WorkspaceID, principal.SubjectID, name); sweepErr != nil {
		return sweepErr
	}
	if _, sweepErr := s.db.ExecContext(ctx, deleteSessionDirSQL, principal.WorkspaceID, principal.SubjectID, name); sweepErr != nil {
		return sweepErr
	}
	return err
}

// deleteSessionAdmissionSQL reclaims a named session's admission record.
// chat_session_admissions carries no foreign key to the catalog, so every
// delete path has to name it explicitly or the row - and the agent and tool
// names in it - outlives the session forever.
const deleteSessionAdmissionSQL = `DELETE FROM chat_session_admissions WHERE workspace_id=? AND subject_id=? AND name=? AND instance_id IS NULL`

// deleteSessionDirSQL reclaims a named session's directory record. It is the
// same shape as the admission record: a side table with no foreign key, so
// every delete path names it explicitly or the row outlives the session.
const deleteSessionDirSQL = `DELETE FROM chat_session_dirs WHERE workspace_id=? AND subject_id=? AND name=? AND instance_id IS NULL`

// upsertSessionDirSQL records (or refreshes) a session's directory metadata.
// The name key is a chat_sessions snapshot name for named saves and a
// context_sessions session_id for live sessions.
const upsertSessionDirSQL = `INSERT INTO chat_session_dirs(workspace_id,subject_id,name,dir,worktree,instance_id) VALUES(?,?,?,?,?,?) ON CONFLICT(workspace_id,subject_id,name) DO UPDATE SET dir=excluded.dir,worktree=excluded.worktree WHERE chat_session_dirs.instance_id IS excluded.instance_id`

// deleteSessionSnapshotRow removes a snapshot and its admission record in one
// transaction and reports how many snapshot rows it removed, so the caller can
// fall back to the context-backed delete path.
//
// The admission record is only reclaimed when the snapshot delete actually
// matched. A name this transaction did not own may still be owned by a
// context-backed session, and that session's retention transaction is a
// separate commit: reclaiming the record here would durably destroy it before
// the retention work is known to have landed, leaving a live session with no
// admitted tool set.
func (s *SQLite) deleteSessionSnapshotRow(ctx context.Context, principal contextstate.Principal, name string) (int64, error) {
	var count int64
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := rejectManagedCatalogKey(ctx, tx, principal, name); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM chat_sessions WHERE workspace_id=? AND subject_id=? AND name=? AND instance_id IS NULL`, principal.WorkspaceID, principal.SubjectID, name)
		if err != nil {
			return err
		}
		if count, err = result.RowsAffected(); err != nil || count == 0 {
			return err
		}
		if _, err = tx.ExecContext(ctx, deleteSessionAdmissionSQL, principal.WorkspaceID, principal.SubjectID, name); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, deleteSessionDirSQL, principal.WorkspaceID, principal.SubjectID, name)
		return err
	})
	return count, err
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

// deleteCatalogContextSession applies the full retention lifecycle to a
// context-backed session exposed through the catalog. Catalog callers may
// select another session owned by the same subject, so the operation is
// scoped by the owner tuple rather than the current principal's session ID.
func (s *SQLite) deleteCatalogContextSession(ctx context.Context, principal contextstate.Principal, sessionID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	var revision int
	err = tx.QueryRowContext(ctx, `SELECT session_revision FROM context_sessions WHERE workspace_id=? AND subject_id=? AND session_id=? AND tombstoned=0`, principal.WorkspaceID, principal.SubjectID, sessionID).Scan(&revision)
	if err == sql.ErrNoRows {
		_ = tx.Rollback()
		return contextstate.ErrSessionNotFound
	}
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	auditID, err := newContextID("ctxaudit_")
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	created, expires := retentionWindow()
	if _, err = tx.ExecContext(ctx, `UPDATE context_sessions SET tombstoned=1,session_revision=? WHERE workspace_id=? AND subject_id=? AND session_id=? AND tombstoned=0`, revision+1, principal.WorkspaceID, principal.SubjectID, sessionID); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE context_payloads SET revoked=1,expires_at=? WHERE workspace_id=? AND subject_id=? AND session_id=? AND revoked=0`, expires, principal.WorkspaceID, principal.SubjectID, sessionID); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO context_audits(audit_id,action,workspace_id,session_id,subject_id,revision,size,retention_class,expires_at,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, auditID, string(contextstate.AuditDelete), principal.WorkspaceID, sessionID, principal.SubjectID, revision+1, 0, string(contextstate.RetentionCompliance), expires, created); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO context_tombstones(session_id,workspace_id,subject_id,revision,retention_class,expires_at,audit_id,created_at) VALUES(?,?,?,?,?,?,?,?)`, sessionID, principal.WorkspaceID, principal.SubjectID, revision+1, string(contextstate.RetentionCompliance), expires, auditID, created); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err = tx.ExecContext(ctx, deleteSessionAdmissionSQL, principal.WorkspaceID, principal.SubjectID, sessionID); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err = tx.ExecContext(ctx, deleteSessionDirSQL, principal.WorkspaceID, principal.SubjectID, sessionID); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

func (s *SQLite) PruneSessionSnapshots(ctx context.Context, principal contextstate.Principal, names []string) error {
	if err := principal.Validate(); err != nil {
		return err
	}
	if len(names) == 0 {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := validateSessionCatalogName(name); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := rejectManagedCatalogKey(ctx, tx, principal, name); err != nil {
			_ = tx.Rollback()
			return err
		}
		// Reclaim the admission record only for a snapshot this prune actually
		// removed. A name that matched nothing may still be a live
		// context-backed session, and stripping its admitted tool set while it
		// keeps running is the failure mode the delete path already had to fix.
		var pruned int64
		result, err := tx.ExecContext(ctx, `DELETE FROM chat_sessions WHERE workspace_id=? AND subject_id=? AND name=? AND instance_id IS NULL`, principal.WorkspaceID, principal.SubjectID, name)
		if err == nil {
			pruned, err = result.RowsAffected()
		}
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if pruned == 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, deleteSessionAdmissionSQL, principal.WorkspaceID, principal.SubjectID, name); err != nil {
			_ = tx.Rollback()
			return err
		}
		if _, err := tx.ExecContext(ctx, deleteSessionDirSQL, principal.WorkspaceID, principal.SubjectID, name); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}
