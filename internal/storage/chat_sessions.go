package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
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

// resolveSnapshotProjectionIdentity computes the session_id and
// session_revision a SaveSession write should stamp. sessionID records the
// live context session this row projects ("id is id, name is name"): a
// non-empty session_id means the row is a live projection declared by the
// saving process (opts.SessionID == name) and verified live at write time;
// empty means a plain snapshot copy. Worktree rows always stay empty; legacy
// projection rows are backfilled at v11. sessionRevision is only meaningful
// alongside a stamped sessionID - a plain named copy never reaches
// resolveProjection, so its revision would never be read anyway.
func resolveSnapshotProjectionIdentity(ctx context.Context, tx *sql.Tx, principal contextstate.Principal, name string, opts contextstate.SessionSaveOptions) (sessionID string, sessionRevision any, err error) {
	if opts.SessionID != "" && opts.SessionID == name {
		// The projection keeps its live identity only when a live row exists
		// in the SAME namespace: the plain (NULL-instance) one for a plain
		// save, the bound instance's for a worktree save. A worktree snapshot
		// that dropped its id here came back from LoadWorktreeSession with
		// no SessionID, so the resumed session never reclaimed and its next
		// turn forked a second context_sessions row.
		var one int
		var err error
		if opts.WorktreeInstance.IsZero() {
			err = tx.QueryRowContext(ctx, `SELECT 1 FROM context_sessions WHERE workspace_id=? AND subject_id=? AND session_id=? AND tombstoned=0 AND instance_id IS NULL`, principal.WorkspaceID, principal.SubjectID, opts.SessionID).Scan(&one)
		} else {
			err = tx.QueryRowContext(ctx, `SELECT 1 FROM context_sessions WHERE workspace_id=? AND subject_id=? AND session_id=? AND tombstoned=0 AND instance_id=?`, principal.WorkspaceID, principal.SubjectID, opts.SessionID, opts.WorktreeInstance.ID).Scan(&one)
		}
		if errors.Is(err, sql.ErrNoRows) {
			sessionID = ""
		} else if err != nil {
			return "", nil, err
		} else {
			sessionID = opts.SessionID
		}
	}
	if sessionID != "" && opts.SessionRevision != nil {
		sessionRevision = *opts.SessionRevision
	}
	return sessionID, sessionRevision, nil
}

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
			sessionID, sessionRevision, err := resolveSnapshotProjectionIdentity(ctx, tx, principal, name, opts)
			if err != nil {
				return err
			}
			result, err := tx.ExecContext(ctx, `INSERT INTO chat_sessions(workspace_id,subject_id,name,model,provider,messages,created_at,updated_at,turn_count,token_count,message_count,instance_id,session_id,session_revision) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(workspace_id,subject_id,name) DO UPDATE SET model=excluded.model,provider=excluded.provider,messages=excluded.messages,updated_at=excluded.updated_at,turn_count=excluded.turn_count,token_count=excluded.token_count,message_count=excluded.message_count,session_id=excluded.session_id,session_revision=excluded.session_revision WHERE chat_sessions.instance_id IS excluded.instance_id`, principal.WorkspaceID, principal.SubjectID, storedName, model, provider, messages, now, now, turns, tokens, messageCount, nullableText(opts.WorktreeInstance.ID), nullableText(sessionID), sessionRevision)
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
	var snapshotRevision sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT c.name,c.model,c.provider,c.messages,c.created_at,c.updated_at,c.turn_count,c.token_count,c.message_count,COALESCE(c.session_id,''),COALESCE(d.dir,''),COALESCE(d.worktree,''),c.session_revision FROM chat_sessions c LEFT JOIN chat_session_dirs d ON d.workspace_id=c.workspace_id AND d.subject_id=c.subject_id AND d.name=c.name WHERE c.workspace_id=? AND c.subject_id=? AND c.name=? AND c.instance_id IS NULL`, principal.WorkspaceID, principal.SubjectID, name).Scan(&info.Name, &info.Model, &info.Provider, &payload, &info.CreatedAt, &info.UpdatedAt, &info.TurnCount, &info.TokenCount, &info.MessageCount, &catalogSessionID, &info.Dir, &info.Worktree, &snapshotRevision)
	if errors.Is(err, sql.ErrNoRows) {
		// No snapshot row: the live context session is the only source,
		// exactly as the arm2/--session fallback served it (empty payload
		// included).
		live, livePayload, hasLive, _, err := s.loadLiveContextSession(ctx, principal, name)
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
	return s.resolveProjection(ctx, principal, catalogSessionID, payload, info, snapshotRevision)
}

// resolveProjection decides whether a chat_sessions projection row (name is a
// live context session's own id) serves its live checkpoint or its snapshot.
//
// A completed checkpoint is always preferred when one exists: it is
// fingerprint-validated (validateStoredCheckpoint), unlike chat_sessions'
// messages column, which carries no integrity check at all.
//
// When there is NO completed checkpoint, the choice is not that simple: it
// covers both "this session was never turned" (empty is correct) and "the
// only content this session ever had lives in the snapshot, because its one
// and only turn died before any preparation existed to checkpoint against"
// (adoptFailedTurnSnapshot, internal/chat/turn_finish.go) - unconditionally
// preferring empty here silently destroys the second case's only copy.
// snapshotRevision (stamped at save time) resolves the ambiguity: if the live
// session's head has not advanced past it, nothing (no /clear, no commit) has
// happened since the snapshot was taken, so it is not stale and must be
// served; if the head has advanced past it, a clear or a commit superseded it
// and the live (possibly empty) state wins. A NULL snapshotRevision (a row
// saved before this column existed) can't be reasoned about this way, so it
// keeps today's conservative default: prefer the live state.
func (s *SQLite) resolveProjection(ctx context.Context, principal contextstate.Principal, catalogSessionID string, payload []byte, info contextstate.SessionCatalogInfo, snapshotRevision sql.NullInt64) ([]byte, contextstate.SessionCatalogInfo, error) {
	live, livePayload, hasLive, liveRevision, err := s.loadLiveContextSession(ctx, principal, catalogSessionID)
	if err != nil {
		return nil, contextstate.SessionCatalogInfo{}, err
	}
	if !hasLive {
		// The live row is gone (tombstoned or deleted): the projection is now
		// a plain copy.
		info.SessionID = ""
		return append([]byte(nil), payload...), info, nil
	}
	if len(livePayload) > len(emptyContextPayload) {
		return livePayload, live, nil
	}
	if snapshotRevision.Valid && uint64(snapshotRevision.Int64) >= liveRevision {
		// Nothing has advanced the head since this snapshot was saved - it is
		// the only content this session ever had. Preserve the live identity
		// (id is id) so the caller still recognizes a live session to take
		// over instead of forking a new one.
		info.SessionID = catalogSessionID
		info.Title = live.Title
		return append([]byte(nil), payload...), info, nil
	}
	return livePayload, live, nil
}

// emptyContextPayload is the COALESCE default the live-row query serves when
// a session row exists but no completed checkpoint backs it. A payload at or
// below this length carries no messages, so the caller keeps its snapshot.
var emptyContextPayload = []byte("[]")

// liveContextSessionSQL resolves the live context session row behind a
// catalog name. It is the payload source for a live session: the session's
// turns are durable in its completed checkpoints, and the chat_sessions row
// named by the session id is only a listing projection of that state.
//
// This intentionally does NOT fall back past active_checkpoint_id when it is
// NULL. An earlier version of this query did, on the premise that a
// NULL/stale pointer could leave a still-valid, un-superseded checkpoint
// orphaned behind it. That premise does not hold: every write that touches
// active_checkpoint_id (Commit's publishContextHead, Advance's
// advanceActiveCheckpoint in context_store.go) sets the pointer and
// session_revision/durable_revision together, atomically, in the same UPDATE.
// The only way to observe NULL here is either a session that has never
// committed (nothing to fall back to) or /clear's ClearActive path, which
// sets active_checkpoint_id=NULL *deliberately* while bumping the revision
// past whatever checkpoint used to be active - a fallback that ignored NULL
// would resurrect a conversation the user explicitly cleared on the very next
// resume. Bug audit caught this as a real regression before it shipped; see
// git history for the reverted fallback and its test coverage.
const liveContextSessionSQL = `SELECT cs.session_id,COALESCE(cs.title,''),cs.model,cs.provider,COALESCE((SELECT active_context FROM context_checkpoints WHERE checkpoint_id=cs.active_checkpoint_id AND complete=1),?),COALESCE((SELECT MIN(created_at) FROM context_checkpoints WHERE session_id=cs.session_id),CURRENT_TIMESTAMP),COALESCE((SELECT MAX(created_at) FROM context_checkpoints WHERE session_id=cs.session_id),CURRENT_TIMESTAMP),source_sequence,COALESCE(d.dir,''),COALESCE(d.worktree,''),cs.session_revision FROM context_sessions cs LEFT JOIN chat_session_dirs d ON d.workspace_id=cs.workspace_id AND d.subject_id=cs.subject_id AND d.name=cs.session_id WHERE cs.workspace_id=? AND cs.subject_id=? AND cs.session_id=? AND cs.tombstoned=0 AND cs.instance_id IS NULL`

// loadLiveContextSession returns the live context session row behind name.
// found is false when no live row exists (a plain named snapshot, or a session
// whose live row is tombstoned); the caller then reads the chat_sessions
// snapshot instead. The payload is emptyContextPayload when the row carries
// no completed checkpoint, and the caller decides whether that is usable.
// revision is the live session's current session_revision, for
// resolveProjection's staleness comparison.
func (s *SQLite) loadLiveContextSession(ctx context.Context, principal contextstate.Principal, name string) (contextstate.SessionCatalogInfo, []byte, bool, uint64, error) {
	var payload []byte
	var info contextstate.SessionCatalogInfo
	var sourceCount int
	var revision uint64
	err := s.db.QueryRowContext(ctx, liveContextSessionSQL, emptyContextPayload, principal.WorkspaceID, principal.SubjectID, name).Scan(&info.SessionID, &info.Title, &info.Model, &info.Provider, &payload, &info.CreatedAt, &info.UpdatedAt, &sourceCount, &info.Dir, &info.Worktree, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return contextstate.SessionCatalogInfo{}, nil, false, 0, nil
	}
	if err != nil {
		return contextstate.SessionCatalogInfo{}, nil, false, 0, err
	}
	info.Name = info.SessionID
	info.MessageCount = sourceCount
	info.TurnCount = sourceCount
	info.TokenCount = 0
	return info, payload, true, revision, nil
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
	rows, err := s.db.QueryContext(ctx, `SELECT c.name,COALESCE(s.title,''),c.model,c.provider,COALESCE(s.session_id,''),c.created_at,c.updated_at,c.turn_count,c.token_count,c.message_count,COALESCE(d.dir,''),COALESCE(d.worktree,''),0,COALESCE(c.instance_id,'') FROM chat_sessions c LEFT JOIN context_sessions s ON s.workspace_id=c.workspace_id AND s.subject_id=c.subject_id AND s.session_id=c.session_id AND s.tombstoned=0 AND s.instance_id IS c.instance_id LEFT JOIN chat_session_dirs d ON d.workspace_id=c.workspace_id AND d.subject_id=c.subject_id AND d.name=c.name AND d.instance_id IS c.instance_id WHERE c.workspace_id=? AND c.subject_id=? UNION ALL SELECT t.session_id,t.title,t.model,t.provider,t.session_id,t.created,t.updated,t.source_sequence,0,t.source_sequence,COALESCE(d.dir,''),COALESCE(d.worktree,''),0,COALESCE(t.instance_id,'') FROM (SELECT cs.workspace_id,cs.subject_id,cs.session_id,cs.title,cs.model,cs.provider,cs.source_sequence,cs.instance_id,COALESCE(MIN(cc.created_at),CURRENT_TIMESTAMP) AS created,COALESCE(MAX(cc.created_at),CURRENT_TIMESTAMP) AS updated FROM context_sessions cs LEFT JOIN context_checkpoints cc ON cc.session_id=cs.session_id AND cc.workspace_id=cs.workspace_id AND cc.subject_id=cs.subject_id AND cc.complete=1 WHERE cs.workspace_id=? AND cs.subject_id=? AND cs.tombstoned=0 AND (cs.source_sequence>0 OR cs.title IS NOT NULL) AND NOT EXISTS (SELECT 1 FROM chat_sessions c WHERE c.workspace_id=cs.workspace_id AND c.subject_id=cs.subject_id AND c.name=cs.session_id AND c.instance_id IS cs.instance_id) GROUP BY cs.workspace_id,cs.subject_id,cs.session_id,cs.title,cs.model,cs.provider,cs.source_sequence,cs.instance_id) t LEFT JOIN chat_session_dirs d ON d.workspace_id=t.workspace_id AND d.subject_id=t.subject_id AND d.name=t.session_id AND d.instance_id IS t.instance_id UNION ALL SELECT 'worktree:' || r.worktree,'','','', '',r.created_at,r.updated_at,0,0,0,r.dir,r.worktree,1,COALESCE(r.instance_id,'') FROM worktree_routes r WHERE r.workspace_id=? AND r.subject_id=? AND (r.instance_id IS NULL OR EXISTS (SELECT 1 FROM worktree_instances wi WHERE wi.workspace_id=r.workspace_id AND wi.worktree=r.worktree AND wi.instance_id=r.instance_id AND wi.state='active'))`, principal.WorkspaceID, principal.SubjectID, principal.WorkspaceID, principal.SubjectID, principal.WorkspaceID, principal.SubjectID)
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
	sortSessionCatalogInfos(out)
	return hideCoveredWorktreeRoutes(out), nil
}

// sortSessionCatalogInfos orders rows most-recently-updated first, tie-broken
// by name ascending - the ordering the removed `ORDER BY 7 DESC,1` SQL clause
// used to provide. That clause compared UpdatedAt as a raw string across a
// UNION whose arms write two different timestamp layouts (RFC3339Nano for
// chat_sessions/worktree_routes, SQLite's CURRENT_TIMESTAMP layout for the
// context_checkpoints-derived arm - see contextstate.ParseCatalogTimestamp),
// so a live session with no completed checkpoint yet could rank below a
// stale RFC3339-timestamped row from months ago, or a genuinely newer live
// session could sort below an older named snapshot, regardless of actual
// recency. Sorting parsed times in Go avoids comparing across layouts.
func sortSessionCatalogInfos(out []contextstate.SessionCatalogInfo) {
	sort.SliceStable(out, func(i, j int) bool {
		ti := contextstate.ParseCatalogTimestamp(out[i].UpdatedAt)
		tj := contextstate.ParseCatalogTimestamp(out[j].UpdatedAt)
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return out[i].Name < out[j].Name
	})
}

// upsertSessionDirSQL records (or refreshes) a session's directory metadata.
// The name key is a chat_sessions snapshot name for named saves and a
// context_sessions session_id for live sessions.
const upsertSessionDirSQL = `INSERT INTO chat_session_dirs(workspace_id,subject_id,name,dir,worktree,instance_id) VALUES(?,?,?,?,?,?) ON CONFLICT(workspace_id,subject_id,name) DO UPDATE SET dir=excluded.dir,worktree=excluded.worktree WHERE chat_session_dirs.instance_id IS excluded.instance_id`
