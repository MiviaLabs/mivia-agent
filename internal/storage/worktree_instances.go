package storage

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

var _ contextstate.WorktreeSessionCatalog = (*SQLite)(nil)
var _ contextstate.WorktreeStore = (*SQLite)(nil)

// DeletingWorktreeInstance returns the retained deletion record for a name.
func (s *SQLite) DeletingWorktreeInstance(ctx context.Context, principal contextstate.Principal, worktree string) (contextstate.WorktreeInstance, error) {
	if err := principal.Validate(); err != nil {
		return contextstate.WorktreeInstance{}, err
	}
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT instance_id FROM worktree_instances WHERE workspace_id=? AND worktree=? AND state=?`, principal.WorkspaceID, worktree, contextstate.WorktreeDeleting).Scan(&id)
	if err == sql.ErrNoRows {
		return contextstate.WorktreeInstance{}, contextstate.ErrWorktreeDeleted
	}
	if err != nil {
		return contextstate.WorktreeInstance{}, err
	}
	return contextstate.WorktreeInstance{Worktree: worktree, ID: id}, nil
}

// LoadWorktree loads a durable session only while its exact instance is active.
func (s *SQLite) LoadWorktree(ctx context.Context, principal contextstate.Principal, sessionID string, instance contextstate.WorktreeInstance) (contextstate.Snapshot, error) {
	if err := instance.Validate(); err != nil || instance.IsZero() {
		return contextstate.Snapshot{}, fmt.Errorf("%w: invalid worktree instance", contextstate.ErrInvalidDTO)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return contextstate.Snapshot{}, err
	}
	if err := requireActiveWorktreeTx(ctx, tx, principal, instance); err != nil {
		_ = tx.Rollback()
		return contextstate.Snapshot{}, err
	}
	_ = tx.Rollback()
	return s.Load(ctx, principal, sessionID)
}

// RegisterWorktreeInstance creates an active catalog record for a managed
// worktree. A live record with the same name blocks same-name reuse.
func (s *SQLite) RegisterWorktreeInstance(ctx context.Context, principal contextstate.Principal, instance contextstate.WorktreeInstance, canonicalPath string) error {
	if err := principal.Validate(); err != nil {
		return err
	}
	if err := instance.Validate(); err != nil || instance.IsZero() {
		return fmt.Errorf("%w: invalid worktree instance", contextstate.ErrInvalidDTO)
	}
	if !filepath.IsAbs(canonicalPath) || !contextstate.ValidSessionDir(canonicalPath) {
		return fmt.Errorf("%w: invalid worktree path", contextstate.ErrInvalidDTO)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO worktree_instances(workspace_id,worktree,instance_id,canonical_path,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, principal.WorkspaceID, instance.Worktree, instance.ID, canonicalPath, contextstate.WorktreeActive, now, now); err != nil {
			if isConstraint(err) {
				return fmt.Errorf("%w: worktree instance already exists", contextstate.ErrWorktreeDeleted)
			}
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO worktree_routes(workspace_id,subject_id,worktree,dir,created_at,updated_at,instance_id) VALUES(?,?,?,?,?,?,?) ON CONFLICT(workspace_id,subject_id,worktree) DO UPDATE SET dir=excluded.dir,updated_at=excluded.updated_at,instance_id=excluded.instance_id`, principal.WorkspaceID, principal.SubjectID, instance.Worktree, canonicalPath, now, now, instance.ID)
		return err
	})
	return err
}

// BeginWorktreeDeletion fences the exact active physical worktree instance.
func (s *SQLite) BeginWorktreeDeletion(ctx context.Context, principal contextstate.Principal, instance contextstate.WorktreeInstance) error {
	if err := principal.Validate(); err != nil {
		return err
	}
	if err := instance.Validate(); err != nil || instance.IsZero() {
		return fmt.Errorf("%w: invalid worktree instance", contextstate.ErrInvalidDTO)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	result, err := s.db.ExecContext(ctx, `UPDATE worktree_instances SET state=?,updated_at=? WHERE workspace_id=? AND worktree=? AND instance_id=? AND state=?`, contextstate.WorktreeDeleting, time.Now().UTC().Format(time.RFC3339Nano), principal.WorkspaceID, instance.Worktree, instance.ID, contextstate.WorktreeActive)
	if err != nil {
		return err
	}
	return requireContextRows(result, contextstate.ErrWorktreeDeleted)
}

// ReactivateWorktreeInstance restores an instance only when deletion did not
// remove its Git worktree. Callers use it after a failed external removal.
func (s *SQLite) ReactivateWorktreeInstance(ctx context.Context, principal contextstate.Principal, instance contextstate.WorktreeInstance) error {
	if err := principal.Validate(); err != nil {
		return err
	}
	if err := instance.Validate(); err != nil || instance.IsZero() {
		return fmt.Errorf("%w: invalid worktree instance", contextstate.ErrInvalidDTO)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	result, err := s.db.ExecContext(ctx, `UPDATE worktree_instances SET state=?,updated_at=? WHERE workspace_id=? AND worktree=? AND instance_id=? AND state=?`, contextstate.WorktreeActive, time.Now().UTC().Format(time.RFC3339Nano), principal.WorkspaceID, instance.Worktree, instance.ID, contextstate.WorktreeDeleting)
	if err != nil {
		return err
	}
	return requireContextRows(result, contextstate.ErrWorktreeDeleted)
}

// DeleteWorktreeSessions completes a fenced deletion. The lifecycle row is
// exact-scoped so a retry for an old instance cannot change a replacement.
func (s *SQLite) DeleteWorktreeSessions(ctx context.Context, principal contextstate.Principal, instance contextstate.WorktreeInstance) (int, error) {
	if err := principal.Validate(); err != nil {
		return 0, err
	}
	if err := instance.Validate(); err != nil || instance.IsZero() {
		return 0, fmt.Errorf("%w: invalid worktree instance", contextstate.ErrInvalidDTO)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var count int
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var sessionIDs []string
		rows, err := tx.QueryContext(ctx, `SELECT session_id FROM context_sessions WHERE workspace_id=? AND subject_id=? AND instance_id=? AND tombstoned=0`, principal.WorkspaceID, principal.SubjectID, instance.ID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var sessionID string
			if err := rows.Scan(&sessionID); err != nil {
				rows.Close()
				return err
			}
			sessionIDs = append(sessionIDs, sessionID)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM chat_sessions WHERE workspace_id=? AND subject_id=? AND instance_id=?`, principal.WorkspaceID, principal.SubjectID, instance.ID)
		if err != nil {
			return err
		}
		deletedSnapshots, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM chat_session_admissions WHERE workspace_id=? AND subject_id=? AND instance_id=?`, principal.WorkspaceID, principal.SubjectID, instance.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM chat_session_dirs WHERE workspace_id=? AND subject_id=? AND instance_id=?`, principal.WorkspaceID, principal.SubjectID, instance.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM worktree_routes WHERE workspace_id=? AND subject_id=? AND worktree=? AND instance_id=?`, principal.WorkspaceID, principal.SubjectID, instance.Worktree, instance.ID); err != nil {
			return err
		}
		for _, sessionID := range sessionIDs {
			if err := tombstoneWorktreeSessionTx(ctx, tx, principal, sessionID); err != nil {
				return err
			}
		}
		count = int(deletedSnapshots) + len(sessionIDs)
		result, err = tx.ExecContext(ctx, `UPDATE worktree_instances SET state=?,updated_at=? WHERE workspace_id=? AND worktree=? AND instance_id=? AND state=?`, contextstate.WorktreeDeleted, time.Now().UTC().Format(time.RFC3339Nano), principal.WorkspaceID, instance.Worktree, instance.ID, contextstate.WorktreeDeleting)
		if err != nil {
			return err
		}
		if err := requireContextRows(result, contextstate.ErrWorktreeDeleted); err != nil {
			return err
		}
		return nil
	})
	return count, err
}

func tombstoneWorktreeSessionTx(ctx context.Context, tx *sql.Tx, principal contextstate.Principal, sessionID string) error {
	var revision uint64
	if err := tx.QueryRowContext(ctx, `SELECT session_revision FROM context_sessions WHERE workspace_id=? AND subject_id=? AND session_id=? AND tombstoned=0`, principal.WorkspaceID, principal.SubjectID, sessionID).Scan(&revision); err != nil {
		return err
	}
	created, expires := retentionWindow()
	auditID, err := newContextID("ctxaudit_")
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE context_sessions SET tombstoned=1,session_revision=? WHERE workspace_id=? AND subject_id=? AND session_id=? AND tombstoned=0`, revision+1, principal.WorkspaceID, principal.SubjectID, sessionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE context_payloads SET revoked=1,expires_at=? WHERE workspace_id=? AND subject_id=? AND session_id=? AND revoked=0`, expires, principal.WorkspaceID, principal.SubjectID, sessionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO context_audits(audit_id,action,workspace_id,session_id,subject_id,revision,size,retention_class,expires_at,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, auditID, string(contextstate.AuditDelete), principal.WorkspaceID, sessionID, principal.SubjectID, revision+1, 0, string(contextstate.RetentionCompliance), expires, created); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO context_tombstones(session_id,workspace_id,subject_id,revision,retention_class,expires_at,audit_id,created_at) VALUES(?,?,?,?,?,?,?,?)`, sessionID, principal.WorkspaceID, principal.SubjectID, revision+1, string(contextstate.RetentionCompliance), expires, auditID, created)
	return err
}

func requireActiveWorktreeTx(ctx context.Context, tx *sql.Tx, principal contextstate.Principal, instance contextstate.WorktreeInstance) error {
	if instance.IsZero() {
		return nil
	}
	var state contextstate.WorktreeInstanceState
	err := tx.QueryRowContext(ctx, `SELECT state FROM worktree_instances WHERE workspace_id=? AND worktree=? AND instance_id=?`, principal.WorkspaceID, instance.Worktree, instance.ID).Scan(&state)
	if err == sql.ErrNoRows {
		return contextstate.ErrWorktreeDeleted
	}
	if err != nil {
		return err
	}
	if state != contextstate.WorktreeActive {
		return contextstate.ErrWorktreeDeleted
	}
	return nil
}

func requireWorktreeSessionBinding(row contextSessionRow, instance contextstate.WorktreeInstance) error {
	if instance.IsZero() {
		if row.InstanceID.Valid {
			return contextstate.ErrWorktreeDeleted
		}
		return nil
	}
	if !row.InstanceID.Valid || row.InstanceID.String != instance.ID {
		return contextstate.ErrWorktreeDeleted
	}
	return nil
}
