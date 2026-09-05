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
		// the live checkpoint, as the plain LoadSession does for plain
		// sessions; without this every such session in the /resume picker
		// fails with "session not found" although its instance is active
		// and its history is on disk.
		live, payload, found, lerr := loadLiveWorktreeContextSessionTx(ctx, tx, p, n, i)
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
	err = tx.QueryRowContext(ctx, `SELECT c.name,c.model,c.provider,c.messages,c.created_at,c.updated_at,c.turn_count,c.token_count,c.message_count,COALESCE(c.session_id,''),COALESCE(d.dir,''),COALESCE(d.worktree,'') FROM chat_sessions c LEFT JOIN chat_session_dirs d ON d.workspace_id=c.workspace_id AND d.subject_id=c.subject_id AND d.name=c.name WHERE c.workspace_id=? AND c.subject_id=? AND c.name=? AND c.instance_id=?`, p.WorkspaceID, p.SubjectID, key, i.ID).Scan(&out.Name, &out.Model, &out.Provider, &b, &out.CreatedAt, &out.UpdatedAt, &out.TurnCount, &out.TokenCount, &out.MessageCount, &catalogSessionID, &out.Dir, &out.Worktree)
	if err == sql.ErrNoRows {
		return nil, out, contextstate.ErrSessionNotFound
	}
	if err != nil {
		return nil, out, err
	}
	out.Name = n
	out.WorktreeInstance = i
	if catalogSessionID != "" {
		// "id is id": a projection of the live session keeps its identity so
		// loadContextCatalog reclaims it instead of minting a fresh id and
		// forking the next turn into a second context_sessions row. A live
		// checkpoint, when one exists, outranks the unverified snapshot bytes
		// exactly as resolveProjection decides for plain sessions.
		live, payload, found, lerr := loadLiveWorktreeContextSessionTx(ctx, tx, p, catalogSessionID, i)
		if lerr != nil {
			return nil, out, lerr
		}
		if found && len(payload) > len(emptyContextPayload) {
			return payload, live, tx.Commit()
		}
		out.SessionID = catalogSessionID
	}
	return append([]byte(nil), b...), out, tx.Commit()
}

// liveWorktreeContextSessionSQL is liveContextSessionSQL scoped to ONE
// worktree instance instead of the plain (NULL-instance) namespace. The two
// stay parallel on purpose: the plain loader must keep refusing an
// instance-bound row (fail closed for an unbound reader), and this one must
// refuse every other instance's row.
const liveWorktreeContextSessionSQL = `SELECT cs.session_id,COALESCE(cs.title,''),cs.model,cs.provider,COALESCE((SELECT active_context FROM context_checkpoints WHERE checkpoint_id=cs.active_checkpoint_id AND complete=1),?),COALESCE((SELECT MIN(created_at) FROM context_checkpoints WHERE session_id=cs.session_id),CURRENT_TIMESTAMP),COALESCE((SELECT MAX(created_at) FROM context_checkpoints WHERE session_id=cs.session_id),CURRENT_TIMESTAMP),source_sequence,COALESCE(d.dir,''),COALESCE(d.worktree,'') FROM context_sessions cs LEFT JOIN chat_session_dirs d ON d.workspace_id=cs.workspace_id AND d.subject_id=cs.subject_id AND d.name=cs.session_id WHERE cs.workspace_id=? AND cs.subject_id=? AND cs.session_id=? AND cs.tombstoned=0 AND cs.instance_id=?`

// loadLiveWorktreeContextSessionTx is loadLiveContextSession for a row bound
// to instance, inside the caller's transaction (after requireActiveWorktreeTx,
// so the instance is known active). found is false when no live row is bound
// to that instance under that id.
func loadLiveWorktreeContextSessionTx(ctx context.Context, tx *sql.Tx, p contextstate.Principal, name string, i contextstate.WorktreeInstance) (contextstate.SessionCatalogInfo, []byte, bool, error) {
	var payload []byte
	var info contextstate.SessionCatalogInfo
	var sourceCount int
	err := tx.QueryRowContext(ctx, liveWorktreeContextSessionSQL, emptyContextPayload, p.WorkspaceID, p.SubjectID, name, i.ID).Scan(&info.SessionID, &info.Title, &info.Model, &info.Provider, &payload, &info.CreatedAt, &info.UpdatedAt, &sourceCount, &info.Dir, &info.Worktree)
	if errors.Is(err, sql.ErrNoRows) {
		return contextstate.SessionCatalogInfo{}, nil, false, nil
	}
	if err != nil {
		return contextstate.SessionCatalogInfo{}, nil, false, err
	}
	info.Name = info.SessionID
	info.MessageCount = sourceCount
	info.TurnCount = sourceCount
	info.WorktreeInstance = i
	return info, payload, true, nil
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
		_, err = tx.ExecContext(ctx, `DELETE FROM worktree_catalog_keys WHERE workspace_id=? AND subject_id=? AND instance_id=? AND entity='snapshot' AND name=? AND storage_key=?`, p.WorkspaceID, p.SubjectID, i.ID, n, k)
		return err
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
