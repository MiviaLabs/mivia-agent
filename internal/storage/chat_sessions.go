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
	// restore metadata is missing or points at an older location. Retried
	// while the write lock is busy: the chat process autosaves the catalog
	// after every turn, and a transient cross-process lock collision must
	// not keep the session out of the catalog. The upsert is idempotent, so
	// a retry is safe.
	return retrySQLiteBusy(ctx, func() error {
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
			// sessionID records the live context session this row projects
			// ("id is id, name is name"). A non-empty session_id means the
			// row is a live projection declared by the saving process
			// (opts.SessionID == name) and verified live at write time; NULL
			// means a plain snapshot copy. Worktree rows always stay NULL;
			// legacy projection rows are backfilled at v11.
			sessionID := ""
			if opts.WorktreeInstance.IsZero() && opts.SessionID != "" && opts.SessionID == name {
				var one int
				err := tx.QueryRowContext(ctx, `SELECT 1 FROM context_sessions WHERE workspace_id=? AND subject_id=? AND session_id=? AND tombstoned=0 AND instance_id IS NULL`, principal.WorkspaceID, principal.SubjectID, opts.SessionID).Scan(&one)
				if errors.Is(err, sql.ErrNoRows) {
					sessionID = ""
				} else if err != nil {
					return err
				} else {
					sessionID = opts.SessionID
				}
			}
			result, err := tx.ExecContext(ctx, `INSERT INTO chat_sessions(workspace_id,subject_id,name,model,provider,messages,created_at,updated_at,turn_count,token_count,message_count,instance_id,session_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(workspace_id,subject_id,name) DO UPDATE SET model=excluded.model,provider=excluded.provider,messages=excluded.messages,updated_at=excluded.updated_at,turn_count=excluded.turn_count,token_count=excluded.token_count,message_count=excluded.message_count,session_id=excluded.session_id WHERE chat_sessions.instance_id IS excluded.instance_id`, principal.WorkspaceID, principal.SubjectID, storedName, model, provider, messages, now, now, turns, tokens, messageCount, nullableText(opts.WorktreeInstance.ID), nullableText(sessionID))
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
	// The stored session_id column is the SOLE discriminator between a live
	// projection and a plain named copy ("id is id, name is name"): a
	// user-named copy must no longer be shadowed by a same-named live
	// session, so the catalog row is read first and its session_id decides
	// what the live context row (if any) is for.
	var payload []byte
	var info contextstate.SessionCatalogInfo
	var catalogSessionID string
	err := s.db.QueryRowContext(ctx, `SELECT c.name,c.model,c.provider,c.messages,c.created_at,c.updated_at,c.turn_count,c.token_count,c.message_count,COALESCE(c.session_id,''),COALESCE(d.dir,''),COALESCE(d.worktree,'') FROM chat_sessions c LEFT JOIN chat_session_dirs d ON d.workspace_id=c.workspace_id AND d.subject_id=c.subject_id AND d.name=c.name WHERE c.workspace_id=? AND c.subject_id=? AND c.name=? AND c.instance_id IS NULL`, principal.WorkspaceID, principal.SubjectID, name).Scan(&info.Name, &info.Model, &info.Provider, &payload, &info.CreatedAt, &info.UpdatedAt, &info.TurnCount, &info.TokenCount, &info.MessageCount, &catalogSessionID, &info.Dir, &info.Worktree)
	if errors.Is(err, sql.ErrNoRows) {
		// No snapshot row: the live context session is the only source,
		// exactly as the arm2/--session fallback served it (empty payload
		// included).
		live, livePayload, hasLive, err := s.loadLiveContextSession(ctx, principal, name)
		if err != nil {
			return nil, contextstate.SessionCatalogInfo{}, err
		}
		if hasLive {
			return livePayload, live, nil
		}
		return nil, contextstate.SessionCatalogInfo{}, contextstate.ErrSessionNotFound
	}
	if err != nil {
		return nil, contextstate.SessionCatalogInfo{}, err
	}
	if catalogSessionID == "" {
		// Plain snapshot copy: name is just a name. Even when a live session
		// of the same name exists, the copy is served as-is with no session
		// id - never a shadow, never a takeover.
		return append([]byte(nil), payload...), info, nil
	}
	live, livePayload, hasLive, err := s.loadLiveContextSession(ctx, principal, catalogSessionID)
	if err != nil {
		return nil, contextstate.SessionCatalogInfo{}, err
	}
	// A projection's transcript lives in its completed context checkpoints;
	// the chat_sessions row is only a catalog projection of that state,
	// refreshed by SaveAfterTurn on its own schedule. A projection that lags
	// the checkpoints must not shadow the newer state, so the completed live
	// payload wins when one exists.
	if hasLive && len(livePayload) > len(emptyContextPayload) {
		return livePayload, live, nil
	}
	if hasLive {
		// The live session exists but has no completed checkpoint (a cleared
		// or never-turned session): serve the snapshot, preserving the live
		// identity (id is id) so the caller still recognizes a live session
		// to take over instead of forking a new one.
		info.SessionID = catalogSessionID
		info.Title = live.Title
		return append([]byte(nil), payload...), info, nil
	}
	// The live row is gone (tombstoned or deleted): the projection is now a
	// plain copy.
	info.SessionID = ""
	return append([]byte(nil), payload...), info, nil
}

// emptyContextPayload is the COALESCE default the live-row query serves when
// a session row exists but no completed checkpoint backs it. A payload at or
// below this length carries no messages, so the caller keeps its snapshot.
var emptyContextPayload = []byte("[]")

// liveContextSessionSQL resolves the live context session row behind a
// catalog name. It is the payload source for a live session: the session's
// turns are durable in its completed checkpoints, and the chat_sessions row
// named by the session id is only a listing projection of that state.
const liveContextSessionSQL = `SELECT cs.session_id,COALESCE(cs.title,''),cs.model,cs.provider,COALESCE((SELECT active_context FROM context_checkpoints WHERE checkpoint_id=cs.active_checkpoint_id AND complete=1),?),COALESCE((SELECT MIN(created_at) FROM context_checkpoints WHERE session_id=cs.session_id),CURRENT_TIMESTAMP),COALESCE((SELECT MAX(created_at) FROM context_checkpoints WHERE session_id=cs.session_id),CURRENT_TIMESTAMP),source_sequence,COALESCE(d.dir,''),COALESCE(d.worktree,'') FROM context_sessions cs LEFT JOIN chat_session_dirs d ON d.workspace_id=cs.workspace_id AND d.subject_id=cs.subject_id AND d.name=cs.session_id WHERE cs.workspace_id=? AND cs.subject_id=? AND cs.session_id=? AND cs.tombstoned=0 AND cs.instance_id IS NULL`

// loadLiveContextSession returns the live context session row behind name.
// found is false when no live row exists (a plain named snapshot, or a session
// whose live row is tombstoned); the caller then reads the chat_sessions
// snapshot instead. The payload is emptyContextPayload when the row carries
// no completed checkpoint, and the caller decides whether that is usable.
func (s *SQLite) loadLiveContextSession(ctx context.Context, principal contextstate.Principal, name string) (contextstate.SessionCatalogInfo, []byte, bool, error) {
	var payload []byte
	var info contextstate.SessionCatalogInfo
	var sourceCount int
	err := s.db.QueryRowContext(ctx, liveContextSessionSQL, emptyContextPayload, principal.WorkspaceID, principal.SubjectID, name).Scan(&info.SessionID, &info.Title, &info.Model, &info.Provider, &payload, &info.CreatedAt, &info.UpdatedAt, &sourceCount, &info.Dir, &info.Worktree)
	if errors.Is(err, sql.ErrNoRows) {
		return contextstate.SessionCatalogInfo{}, nil, false, nil
	}
	if err != nil {
		return contextstate.SessionCatalogInfo{}, nil, false, err
	}
	info.Name = info.SessionID
	info.MessageCount = sourceCount
	info.TurnCount = sourceCount
	info.TokenCount = 0
	return info, payload, true, nil
}

func (s *SQLite) ListSessions(ctx context.Context, principal contextstate.Principal) ([]contextstate.SessionCatalogInfo, error) {
	if err := principal.Validate(); err != nil {
		return nil, err
	}
	// Arm 1 (turn snapshots) surfaces the live context row behind a
	// projection when one exists. Identity comes from the stored session_id
	// column, not the name ("id is id, name is name"): joining on
	// c.session_id keeps a user-named copy (NULL session_id) untitled and
	// never re-joins it to a same-named live session, which re-created the
	// alias/duplicate bug. Legacy projection rows are stamped by the v11
	// backfill.
	rows, err := s.db.QueryContext(ctx, `SELECT c.name,COALESCE(s.title,''),c.model,c.provider,COALESCE(s.session_id,''),c.created_at,c.updated_at,c.turn_count,c.token_count,c.message_count,COALESCE(d.dir,''),COALESCE(d.worktree,''),0,'' FROM chat_sessions c LEFT JOIN context_sessions s ON s.workspace_id=c.workspace_id AND s.subject_id=c.subject_id AND s.session_id=c.session_id AND s.tombstoned=0 AND s.instance_id IS NULL LEFT JOIN chat_session_dirs d ON d.workspace_id=c.workspace_id AND d.subject_id=c.subject_id AND d.name=c.name WHERE c.workspace_id=? AND c.subject_id=? AND c.instance_id IS NULL UNION ALL SELECT t.session_id,t.title,t.model,t.provider,t.session_id,t.created,t.updated,t.source_sequence,0,t.source_sequence,COALESCE(d.dir,''),COALESCE(d.worktree,''),0,'' FROM (SELECT cs.workspace_id,cs.subject_id,cs.session_id,cs.title,cs.model,cs.provider,cs.source_sequence,COALESCE(MIN(cc.created_at),CURRENT_TIMESTAMP) AS created,COALESCE(MAX(cc.created_at),CURRENT_TIMESTAMP) AS updated FROM context_sessions cs LEFT JOIN context_checkpoints cc ON cc.session_id=cs.session_id AND cc.workspace_id=cs.workspace_id AND cc.subject_id=cs.subject_id AND cc.complete=1 WHERE cs.workspace_id=? AND cs.subject_id=? AND cs.tombstoned=0 AND cs.source_sequence>0 AND cs.instance_id IS NULL AND NOT EXISTS (SELECT 1 FROM chat_sessions c WHERE c.workspace_id=cs.workspace_id AND c.subject_id=cs.subject_id AND c.name=cs.session_id) GROUP BY cs.workspace_id,cs.subject_id,cs.session_id,cs.title,cs.model,cs.provider,cs.source_sequence) t LEFT JOIN chat_session_dirs d ON d.workspace_id=t.workspace_id AND d.subject_id=t.subject_id AND d.name=t.session_id UNION ALL SELECT 'worktree:' || r.worktree,'','','', '',r.created_at,r.updated_at,0,0,0,r.dir,r.worktree,1,COALESCE(r.instance_id,'') FROM worktree_routes r WHERE r.workspace_id=? AND r.subject_id=? AND (r.instance_id IS NULL OR EXISTS (SELECT 1 FROM worktree_instances wi WHERE wi.workspace_id=r.workspace_id AND wi.worktree=r.worktree AND wi.instance_id=r.instance_id AND wi.state='active')) ORDER BY 7 DESC,1`, principal.WorkspaceID, principal.SubjectID, principal.WorkspaceID, principal.SubjectID, principal.WorkspaceID, principal.SubjectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []contextstate.SessionCatalogInfo
	for rows.Next() {
		var info contextstate.SessionCatalogInfo
		var instanceID string
		var title sql.NullString
		if err := rows.Scan(&info.Name, &title, &info.Model, &info.Provider, &info.SessionID, &info.CreatedAt, &info.UpdatedAt, &info.TurnCount, &info.TokenCount, &info.MessageCount, &info.Dir, &info.Worktree, &info.WorktreeRoute, &instanceID); err != nil {
			return nil, err
		}
		info.Title = title.String
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
	var instanceID sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT session_revision,instance_id FROM context_sessions WHERE workspace_id=? AND subject_id=? AND session_id=? AND tombstoned=0`, principal.WorkspaceID, principal.SubjectID, sessionID).Scan(&revision, &instanceID)
	if err == sql.ErrNoRows {
		_ = tx.Rollback()
		return contextstate.ErrSessionNotFound
	}
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := requireWorktreeSessionBinding(contextSessionRow{InstanceID: instanceID}, contextstate.WorktreeInstance{}); err != nil {
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
