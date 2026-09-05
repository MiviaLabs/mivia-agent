package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

func (s *SQLite) LoadWorktreeSession(ctx context.Context, p contextstate.Principal, n string, i contextstate.WorktreeInstance) ([]byte, contextstate.SessionCatalogInfo, error) {
	if err := i.Validate(); err != nil || i.IsZero() {
		return nil, contextstate.SessionCatalogInfo{}, fmt.Errorf("%w: invalid worktree instance", contextstate.ErrInvalidDTO)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, contextstate.SessionCatalogInfo{}, err
	}
	defer tx.Rollback()
	if err = requireActiveWorktreeTx(ctx, tx, p, i); err != nil {
		return nil, contextstate.SessionCatalogInfo{}, err
	}
	key, err := loadWorktreeCatalogKeyTx(ctx, tx, p, i, "snapshot", n)
	if errors.Is(err, contextstate.ErrSessionNotFound) {
		// No snapshot row under this instance. A worktree session that only
		// ever completed turns (never /save, never /clear - the normal TUI
		// case) has exactly that shape: a live context_sessions row bound to
		// the instance, plus checkpoints, and nothing in chat_sessions. Serve
		// the live checkpoint, exactly as the plain LoadSession does for a
		// plain session; without this every such session in the /resume
		// picker failed with "session not found" although its instance was
		// active and its history was on disk.
		live, payload, found, _, lerr := s.loadLiveContextSession(ctx, tx, p, n, i)
		if lerr != nil {
			return nil, contextstate.SessionCatalogInfo{}, lerr
		}
		if !found {
			return nil, contextstate.SessionCatalogInfo{}, contextstate.ErrSessionNotFound
		}
		return payload, live, tx.Commit()
	}
	if err != nil {
		return nil, contextstate.SessionCatalogInfo{}, err
	}
	var b []byte
	var out contextstate.SessionCatalogInfo
	var catalogSessionID string
	var snapshotRevision sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT c.name,c.model,c.provider,c.messages,c.created_at,c.updated_at,c.turn_count,c.token_count,c.message_count,COALESCE(c.session_id,''),COALESCE(d.dir,''),COALESCE(d.worktree,''),c.session_revision FROM chat_sessions c LEFT JOIN chat_session_dirs d ON d.workspace_id=c.workspace_id AND d.subject_id=c.subject_id AND d.name=c.name WHERE c.workspace_id=? AND c.subject_id=? AND c.name=? AND c.instance_id=?`, p.WorkspaceID, p.SubjectID, key, i.ID).Scan(&out.Name, &out.Model, &out.Provider, &b, &out.CreatedAt, &out.UpdatedAt, &out.TurnCount, &out.TokenCount, &out.MessageCount, &catalogSessionID, &out.Dir, &out.Worktree, &snapshotRevision)
	if err == sql.ErrNoRows {
		return nil, out, contextstate.ErrSessionNotFound
	}
	if err != nil {
		return nil, out, err
	}
	out.Name = n
	out.WorktreeInstance = i
	if catalogSessionID == "" {
		// Plain snapshot copy inside the worktree: name is just a name.
		return append([]byte(nil), b...), out, tx.Commit()
	}
	// "id is id": one decision for both namespaces, so a worktree snapshot
	// gets the SAME staleness rule the plain path has - a snapshot older
	// than a /clear must not be served, and the live identity must survive
	// so the caller reclaims instead of forking.
	payload, info, err := s.resolveProjection(ctx, tx, p, catalogSessionID, b, out, snapshotRevision, i)
	if err != nil {
		return nil, out, err
	}
	info.Name = n
	info.WorktreeInstance = i
	return payload, info, tx.Commit()
}

func (s *SQLite) DeleteWorktreeSessionSnapshot(ctx context.Context, p contextstate.Principal, n string, i contextstate.WorktreeInstance) error {
	if err := i.Validate(); err != nil || i.IsZero() {
		return contextstate.ErrWorktreeDeleted
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if err := requireActiveWorktreeTx(ctx, tx, p, i); err != nil {
			return err
		}
		k, err := loadWorktreeCatalogKeyTx(ctx, tx, p, i, "snapshot", n)
		if err != nil {
			return err
		}
		r, err := tx.ExecContext(ctx, `DELETE FROM chat_sessions WHERE workspace_id=? AND subject_id=? AND name=? AND instance_id=?`, p.WorkspaceID, p.SubjectID, k, i.ID)
		if err != nil {
			return err
		}
		if err = requireContextRows(r, contextstate.ErrSessionNotFound); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM chat_session_admissions WHERE workspace_id=? AND subject_id=? AND name=? AND instance_id=?`, p.WorkspaceID, p.SubjectID, k, i.ID); err != nil {
			return err
		}
		// The dir and catalog-key records are side tables with no foreign key,
		// so every delete path names them explicitly or the rows outlive the
		// snapshot forever (see chat_sessions.go).
		if _, err := tx.ExecContext(ctx, `DELETE FROM chat_session_dirs WHERE workspace_id=? AND subject_id=? AND name=? AND instance_id=?`, p.WorkspaceID, p.SubjectID, k, i.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM worktree_catalog_keys WHERE workspace_id=? AND subject_id=? AND instance_id=? AND entity='snapshot' AND name=? AND storage_key=?`, p.WorkspaceID, p.SubjectID, i.ID, n, k); err != nil {
			return err
		}
		// The live row is what LoadWorktreeSession now serves when no
		// snapshot remains, so removing only the snapshot would hand the
		// whole "deleted" conversation back on the next resume. Same
		// retention lifecycle the plain delete applies.
		if err := tombstoneContextSessionTx(ctx, tx, p, n, i); err != nil && !errors.Is(err, contextstate.ErrSessionNotFound) {
			return err
		}
		return nil
	})
}

func (s *SQLite) PruneWorktreeSessionSnapshots(ctx context.Context, p contextstate.Principal, names []string, i contextstate.WorktreeInstance) error {
	if err := i.Validate(); err != nil || i.IsZero() {
		return contextstate.ErrWorktreeDeleted
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if err := requireActiveWorktreeTx(ctx, tx, p, i); err != nil {
			return err
		}
		for _, name := range names {
			if err := validateSessionCatalogName(name); err != nil {
				return err
			}
			key, err := loadWorktreeCatalogKeyTx(ctx, tx, p, i, "snapshot", name)
			if err == contextstate.ErrSessionNotFound {
				continue
			}
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM chat_sessions WHERE workspace_id=? AND subject_id=? AND name=? AND instance_id=?`, p.WorkspaceID, p.SubjectID, key, i.ID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM chat_session_admissions WHERE workspace_id=? AND subject_id=? AND name=? AND instance_id=?`, p.WorkspaceID, p.SubjectID, key, i.ID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM chat_session_dirs WHERE workspace_id=? AND subject_id=? AND name=? AND instance_id=?`, p.WorkspaceID, p.SubjectID, key, i.ID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM worktree_catalog_keys WHERE workspace_id=? AND subject_id=? AND instance_id=? AND entity='snapshot' AND name=? AND storage_key=?`, p.WorkspaceID, p.SubjectID, i.ID, name, key); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *SQLite) SaveWorktreeSessionAdmission(ctx context.Context, p contextstate.Principal, n string, r contextstate.SessionAdmission, i contextstate.WorktreeInstance) error {
	if err := i.Validate(); err != nil || i.IsZero() {
		return contextstate.ErrWorktreeDeleted
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if err := requireActiveWorktreeTx(ctx, tx, p, i); err != nil {
			return err
		}
		k, err := worktreeCatalogKeyTx(ctx, tx, p, i, "snapshot", n)
		if err != nil {
			return err
		}
		if len(r.Names) == 0 {
			_, err = tx.ExecContext(ctx, `DELETE FROM chat_session_admissions WHERE workspace_id=? AND subject_id=? AND name=? AND instance_id=?`, p.WorkspaceID, p.SubjectID, k, i.ID)
			return err
		}
		b, _ := json.Marshal(r.Names)
		result, err := tx.ExecContext(ctx, `INSERT INTO chat_session_admissions(workspace_id,subject_id,name,agent,digest,names,updated_at,instance_id) VALUES(?,?,?,?,?,?,CURRENT_TIMESTAMP,?) ON CONFLICT(workspace_id,subject_id,name) DO UPDATE SET agent=excluded.agent,digest=excluded.digest,names=excluded.names,updated_at=excluded.updated_at WHERE chat_session_admissions.instance_id IS excluded.instance_id`, p.WorkspaceID, p.SubjectID, k, r.Agent, r.Digest, string(b), i.ID)
		if err != nil {
			return err
		}
		return requireCatalogMutation(result)
	})
}

func (s *SQLite) LoadWorktreeSessionAdmission(ctx context.Context, p contextstate.Principal, n string, i contextstate.WorktreeInstance) (contextstate.SessionAdmission, error) {
	var r contextstate.SessionAdmission
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return r, err
	}
	defer tx.Rollback()
	if err = requireActiveWorktreeTx(ctx, tx, p, i); err != nil {
		return r, err
	}
	k, err := loadWorktreeCatalogKeyTx(ctx, tx, p, i, "snapshot", n)
	if err == contextstate.ErrSessionNotFound {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	var b string
	err = tx.QueryRowContext(ctx, `SELECT agent,digest,names FROM chat_session_admissions WHERE workspace_id=? AND subject_id=? AND name=? AND instance_id=?`, p.WorkspaceID, p.SubjectID, k, i.ID).Scan(&r.Agent, &r.Digest, &b)
	if err == sql.ErrNoRows {
		return contextstate.SessionAdmission{}, nil
	}
	if err != nil {
		return r, err
	}
	return r, json.Unmarshal([]byte(b), &r.Names)
}

// WorktreeSessionBinding reports the managed worktree a live session id is
// bound to. found is false for a plain session, so a caller can resolve any
// bare id through one path: the binding is a property of the SESSION,
// recorded here, not something only a /resume listing row knows.
//
// A row bound to an instance the catalog no longer has (or one being
// deleted) is refused rather than reported unbound: silently degrading to
// the plain namespace is what let a worktree session resume detached from
// the worktree it belongs to.
func (s *SQLite) WorktreeSessionBinding(ctx context.Context, p contextstate.Principal, sessionID string) (contextstate.WorktreeInstanceInfo, bool, error) {
	if err := p.Validate(); err != nil {
		return contextstate.WorktreeInstanceInfo{}, false, err
	}
	if err := validateSessionCatalogName(sessionID); err != nil {
		return contextstate.WorktreeInstanceInfo{}, false, err
	}
	var instanceID sql.NullString
	var info contextstate.WorktreeInstanceInfo
	var worktree, canonical, state sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT cs.instance_id,wi.worktree,wi.canonical_path,wi.state FROM context_sessions cs LEFT JOIN worktree_instances wi ON wi.workspace_id=cs.workspace_id AND wi.instance_id=cs.instance_id WHERE cs.workspace_id=? AND cs.subject_id=? AND cs.session_id=? AND cs.tombstoned=0`, p.WorkspaceID, p.SubjectID, sessionID).Scan(&instanceID, &worktree, &canonical, &state)
	if errors.Is(err, sql.ErrNoRows) {
		// No live row at all: a plain named snapshot, or an unknown id. The
		// plain loader owns that decision.
		return contextstate.WorktreeInstanceInfo{}, false, nil
	}
	if err != nil {
		return contextstate.WorktreeInstanceInfo{}, false, err
	}
	if !instanceID.Valid || instanceID.String == "" {
		return contextstate.WorktreeInstanceInfo{}, false, nil
	}
	if !worktree.Valid || !canonical.Valid || contextstate.WorktreeInstanceState(state.String) == contextstate.WorktreeDeleted {
		return contextstate.WorktreeInstanceInfo{}, false, contextstate.ErrWorktreeDeleted
	}
	info.Instance = contextstate.WorktreeInstance{Worktree: worktree.String, ID: instanceID.String}
	info.CanonicalPath = canonical.String
	info.State = contextstate.WorktreeInstanceState(state.String)
	return info, true, nil
}
