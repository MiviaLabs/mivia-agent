package storage

import (
	"context"
	"database/sql"
	"encoding/json"
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
	if err != nil {
		return nil, contextstate.SessionCatalogInfo{}, err
	}
	var b []byte
	var out contextstate.SessionCatalogInfo
	err = tx.QueryRowContext(ctx, `SELECT c.name,c.model,c.provider,c.messages,c.created_at,c.updated_at,c.turn_count,c.token_count,c.message_count,COALESCE(d.dir,''),COALESCE(d.worktree,'') FROM chat_sessions c LEFT JOIN chat_session_dirs d ON d.workspace_id=c.workspace_id AND d.subject_id=c.subject_id AND d.name=c.name WHERE c.workspace_id=? AND c.subject_id=? AND c.name=? AND c.instance_id=?`, p.WorkspaceID, p.SubjectID, key, i.ID).Scan(&out.Name, &out.Model, &out.Provider, &b, &out.CreatedAt, &out.UpdatedAt, &out.TurnCount, &out.TokenCount, &out.MessageCount, &out.Dir, &out.Worktree)
	if err == sql.ErrNoRows {
		return nil, out, contextstate.ErrSessionNotFound
	}
	if err != nil {
		return nil, out, err
	}
	out.Name = n
	return append([]byte(nil), b...), out, tx.Commit()
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
		_, err = tx.ExecContext(ctx, `DELETE FROM chat_session_admissions WHERE workspace_id=? AND subject_id=? AND name=? AND instance_id=?`, p.WorkspaceID, p.SubjectID, k, i.ID)
		return err
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
		_, err = tx.ExecContext(ctx, `INSERT INTO chat_session_admissions(workspace_id,subject_id,name,agent,digest,names,updated_at,instance_id) VALUES(?,?,?,?,?,?,CURRENT_TIMESTAMP,?) ON CONFLICT(workspace_id,subject_id,name) DO UPDATE SET agent=excluded.agent,digest=excluded.digest,names=excluded.names,updated_at=excluded.updated_at,instance_id=excluded.instance_id`, p.WorkspaceID, p.SubjectID, k, r.Agent, r.Digest, string(b), i.ID)
		return err
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
