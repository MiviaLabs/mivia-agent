package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
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

func (s *SQLite) ListDeletingWorktreeInstances(ctx context.Context, principal contextstate.Principal) ([]contextstate.WorktreeInstanceInfo, error) {
	return s.listWorktreeInstancesByState(ctx, principal, contextstate.WorktreeDeleting)
}

func (s *SQLite) ListCreatingWorktreeInstances(ctx context.Context, principal contextstate.Principal) ([]contextstate.WorktreeInstanceInfo, error) {
	return s.listWorktreeInstancesByState(ctx, principal, contextstate.WorktreeCreating)
}

// LiveWorktreeInstance returns the one non-deleted instance for a worktree.
func (s *SQLite) LiveWorktreeInstance(ctx context.Context, principal contextstate.Principal, worktree string) (contextstate.WorktreeInstanceInfo, error) {
	if err := principal.Validate(); err != nil {
		return contextstate.WorktreeInstanceInfo{}, err
	}
	var info contextstate.WorktreeInstanceInfo
	info.Instance.Worktree = worktree
	err := s.db.QueryRowContext(ctx, `SELECT instance_id,canonical_path,state FROM worktree_instances WHERE workspace_id=? AND worktree=? AND state<>?`, principal.WorkspaceID, worktree, contextstate.WorktreeDeleted).Scan(&info.Instance.ID, &info.CanonicalPath, &info.State)
	if err == sql.ErrNoRows {
		return contextstate.WorktreeInstanceInfo{}, contextstate.ErrWorktreeDeleted
	}
	if err != nil {
		return contextstate.WorktreeInstanceInfo{}, err
	}
	switch info.State {
	case contextstate.WorktreeCreating, contextstate.WorktreeActive, contextstate.WorktreeDeleting:
	default:
		return contextstate.WorktreeInstanceInfo{}, fmt.Errorf("%w: invalid worktree instance state", contextstate.ErrInvalidDTO)
	}
	return info, nil
}

func (s *SQLite) listWorktreeInstancesByState(ctx context.Context, principal contextstate.Principal, state contextstate.WorktreeInstanceState) ([]contextstate.WorktreeInstanceInfo, error) {
	if err := principal.Validate(); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT worktree,instance_id,canonical_path,state FROM worktree_instances WHERE workspace_id=? AND state=? ORDER BY worktree`, principal.WorkspaceID, state)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []contextstate.WorktreeInstanceInfo
	for rows.Next() {
		var info contextstate.WorktreeInstanceInfo
		if err := rows.Scan(&info.Instance.Worktree, &info.Instance.ID, &info.CanonicalPath, &info.State); err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	return out, rows.Err()
}

// CreatingWorktreeInstance returns the retained creation record for a name.
func (s *SQLite) CreatingWorktreeInstance(ctx context.Context, principal contextstate.Principal, worktree string) (contextstate.WorktreeInstanceInfo, error) {
	if err := principal.Validate(); err != nil {
		return contextstate.WorktreeInstanceInfo{}, err
	}
	var info contextstate.WorktreeInstanceInfo
	info.Instance.Worktree = worktree
	err := s.db.QueryRowContext(ctx, `SELECT instance_id,canonical_path,state FROM worktree_instances WHERE workspace_id=? AND worktree=? AND state=?`, principal.WorkspaceID, worktree, contextstate.WorktreeCreating).Scan(&info.Instance.ID, &info.CanonicalPath, &info.State)
	if err == sql.ErrNoRows {
		return contextstate.WorktreeInstanceInfo{}, contextstate.ErrWorktreeDeleted
	}
	if err != nil {
		return contextstate.WorktreeInstanceInfo{}, err
	}
	return info, nil
}

// ValidateActiveWorktreeInstance verifies the exact active catalog binding.
func (s *SQLite) ValidateActiveWorktreeInstance(ctx context.Context, principal contextstate.Principal, instance contextstate.WorktreeInstance, canonicalPath string) error {
	if err := validateWorktreeInstancePath(principal, instance, canonicalPath); err != nil {
		return err
	}
	var found string
	err := s.db.QueryRowContext(ctx, `SELECT canonical_path FROM worktree_instances WHERE workspace_id=? AND worktree=? AND instance_id=? AND state=?`, principal.WorkspaceID, instance.Worktree, instance.ID, contextstate.WorktreeActive).Scan(&found)
	if err == sql.ErrNoRows {
		return contextstate.ErrWorktreeDeleted
	}
	if err != nil {
		return err
	}
	if filepath.Clean(workspace.LongPath(found)) != filepath.Clean(workspace.LongPath(canonicalPath)) {
		return contextstate.ErrWorktreeDeleted
	}
	return nil
}

// RequireLegacyWorktreeRoute verifies the exact unbound route needed for adoption.
func (s *SQLite) RequireLegacyWorktreeRoute(ctx context.Context, principal contextstate.Principal, worktree, canonicalPath string) error {
	if err := principal.Validate(); err != nil || !filepath.IsAbs(canonicalPath) {
		return contextstate.ErrWorktreeDeleted
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		return requireLegacyWorktreeRouteTx(ctx, tx, principal, worktree, canonicalPath)
	})
}

func requireLegacyWorktreeRouteTx(ctx context.Context, tx *sql.Tx, principal contextstate.Principal, worktree, canonicalPath string) error {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM worktree_routes WHERE workspace_id=? AND subject_id=? AND worktree=? AND dir=? AND instance_id IS NULL`, principal.WorkspaceID, principal.SubjectID, worktree, canonicalPath).Scan(&count)
	if err != nil {
		return err
	}
	if count != 1 {
		return contextstate.ErrWorktreeDeleted
	}
	return nil
}

// LoadWorktree loads a durable session only while its exact instance is active.
func (s *SQLite) LoadWorktree(ctx context.Context, principal contextstate.Principal, sessionID string, instance contextstate.WorktreeInstance) (contextstate.Snapshot, error) {
	if err := instance.Validate(); err != nil || instance.IsZero() {
		return contextstate.Snapshot{}, fmt.Errorf("%w: invalid worktree instance", contextstate.ErrInvalidDTO)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return contextstate.Snapshot{}, err
	}
	defer tx.Rollback()
	if err := requireActiveWorktreeTx(ctx, tx, principal, instance); err != nil {
		return contextstate.Snapshot{}, err
	}
	snapshot, err := loadContextTx(ctx, tx, principal, sessionID, instance)
	if err != nil {
		return contextstate.Snapshot{}, err
	}
	return snapshot, tx.Commit()
}

// BeginWorktreeCreation reserves one worktree name before Git creates its
// directory. A live record with the same name blocks same-name reuse.
func (s *SQLite) BeginWorktreeCreation(ctx context.Context, principal contextstate.Principal, instance contextstate.WorktreeInstance, canonicalPath string) error {
	if err := validateWorktreeInstancePath(principal, instance, canonicalPath); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO worktree_instances(workspace_id,worktree,instance_id,canonical_path,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, principal.WorkspaceID, instance.Worktree, instance.ID, canonicalPath, contextstate.WorktreeCreating, now, now)
	if isConstraint(err) {
		return fmt.Errorf("%w: worktree instance already exists", contextstate.ErrWorktreeDeleted)
	}
	return err
}

// BeginWorktreeAdoption reserves an exact legacy route for adoption.
func (s *SQLite) BeginWorktreeAdoption(ctx context.Context, principal contextstate.Principal, instance contextstate.WorktreeInstance, canonicalPath string) error {
	if err := validateWorktreeInstancePath(principal, instance, canonicalPath); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if err := requireLegacyWorktreeRouteTx(ctx, tx, principal, instance.Worktree, canonicalPath); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO worktree_instances(workspace_id,worktree,instance_id,canonical_path,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, principal.WorkspaceID, instance.Worktree, instance.ID, canonicalPath, contextstate.WorktreeCreating, now, now)
		if isConstraint(err) {
			return contextstate.ErrWorktreeDeleted
		}
		return err
	})
}

// AbandonWorktreeCreation removes a reservation after Git creation fails.
func (s *SQLite) AbandonWorktreeCreation(ctx context.Context, principal contextstate.Principal, instance contextstate.WorktreeInstance) error {
	if err := validateWorktreeInstance(principal, instance); err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	result, err := s.db.ExecContext(ctx, `DELETE FROM worktree_instances WHERE workspace_id=? AND worktree=? AND instance_id=? AND state=?`, principal.WorkspaceID, instance.Worktree, instance.ID, contextstate.WorktreeCreating)
	if err != nil {
		return err
	}
	return requireContextRows(result, contextstate.ErrWorktreeDeleted)
}

// RegisterWorktreeInstance activates a preflighted catalog record. It adds the
// caller route in the same transaction.
func (s *SQLite) RegisterWorktreeInstance(ctx context.Context, principal contextstate.Principal, instance contextstate.WorktreeInstance, canonicalPath string) error {
	if err := validateWorktreeInstancePath(principal, instance, canonicalPath); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE worktree_instances SET state=?,updated_at=? WHERE workspace_id=? AND worktree=? AND instance_id=? AND canonical_path=? AND state=?`, contextstate.WorktreeActive, now, principal.WorkspaceID, instance.Worktree, instance.ID, canonicalPath, contextstate.WorktreeCreating)
		if err != nil {
			return err
		}
		if err := requireContextRows(result, contextstate.ErrWorktreeDeleted); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO worktree_routes(workspace_id,subject_id,worktree,dir,created_at,updated_at,instance_id) VALUES(?,?,?,?,?,?,?)`, principal.WorkspaceID, principal.SubjectID, instance.Worktree, canonicalPath, now, now, instance.ID)
		return err
	})
	return err
}

// RegisterAdoptedWorktreeInstance activates an adoption reservation only if
// the legacy route remains exact and unbound.
func (s *SQLite) RegisterAdoptedWorktreeInstance(ctx context.Context, principal contextstate.Principal, instance contextstate.WorktreeInstance, canonicalPath string) error {
	if err := validateWorktreeInstancePath(principal, instance, canonicalPath); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if err := requireLegacyWorktreeRouteTx(ctx, tx, principal, instance.Worktree, canonicalPath); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE worktree_instances SET state=?,updated_at=? WHERE workspace_id=? AND worktree=? AND instance_id=? AND canonical_path=? AND state=?`, contextstate.WorktreeActive, now, principal.WorkspaceID, instance.Worktree, instance.ID, canonicalPath, contextstate.WorktreeCreating)
		if err != nil {
			return err
		}
		if err := requireContextRows(result, contextstate.ErrWorktreeDeleted); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO worktree_routes(workspace_id,subject_id,worktree,dir,created_at,updated_at,instance_id) VALUES(?,?,?,?,?,?,?)`, principal.WorkspaceID, principal.SubjectID, instance.Worktree, canonicalPath, now, now, instance.ID)
		return err
	})
}

func validateWorktreeInstance(principal contextstate.Principal, instance contextstate.WorktreeInstance) error {
	if err := principal.Validate(); err != nil {
		return err
	}
	if err := instance.Validate(); err != nil || instance.IsZero() {
		return fmt.Errorf("%w: invalid worktree instance", contextstate.ErrInvalidDTO)
	}
	return nil
}

func validateWorktreeInstancePath(principal contextstate.Principal, instance contextstate.WorktreeInstance, canonicalPath string) error {
	if err := validateWorktreeInstance(principal, instance); err != nil {
		return err
	}
	// Expand Windows 8.3 short names before comparing: git and
	// filepath.EvalSymlinks report the long form, so a short-name input
	// would otherwise be rejected as non-canonical for naming the same
	// directory.
	canonicalPath = workspace.LongPath(canonicalPath)
	if !filepath.IsAbs(canonicalPath) || !contextstate.ValidSessionDir(canonicalPath) {
		return fmt.Errorf("%w: invalid worktree path", contextstate.ErrInvalidDTO)
	}
	if filepath.Clean(canonicalPath) != canonicalPath {
		return fmt.Errorf("%w: worktree path is not canonical", contextstate.ErrInvalidDTO)
	}
	resolved, err := canonicalExistingPath(canonicalPath)
	if err != nil || resolved != canonicalPath {
		return fmt.Errorf("%w: worktree path is not canonical", contextstate.ErrInvalidDTO)
	}
	return nil
}

func canonicalExistingPath(path string) (string, error) {
	var suffix []string
	for {
		resolved, err := filepath.EvalSymlinks(path)
		if err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved, nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		info, statErr := os.Lstat(path)
		if statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("path contains a dangling symlink")
		}
		if statErr != nil && !os.IsNotExist(statErr) {
			return "", statErr
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", err
		}
		suffix = append(suffix, filepath.Base(path))
		path = parent
	}
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
		var lifecycleState string
		if err := tx.QueryRowContext(ctx, `SELECT state FROM worktree_instances WHERE workspace_id=? AND worktree=? AND instance_id=?`, principal.WorkspaceID, instance.Worktree, instance.ID).Scan(&lifecycleState); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return contextstate.ErrWorktreeDeleted
			}
			return err
		}
		if lifecycleState != string(contextstate.WorktreeDeleting) && lifecycleState != string(contextstate.WorktreeDeleted) {
			return contextstate.ErrWorktreeDeleted
		}
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
		if _, err := tx.ExecContext(ctx, `DELETE FROM worktree_catalog_keys WHERE workspace_id=? AND subject_id=? AND instance_id=?`, principal.WorkspaceID, principal.SubjectID, instance.ID); err != nil {
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
		if lifecycleState == string(contextstate.WorktreeDeleting) {
			result, err = tx.ExecContext(ctx, `UPDATE worktree_instances SET state=?,updated_at=? WHERE workspace_id=? AND worktree=? AND instance_id=? AND state=?`, contextstate.WorktreeDeleted, time.Now().UTC().Format(time.RFC3339Nano), principal.WorkspaceID, instance.Worktree, instance.ID, contextstate.WorktreeDeleting)
			if err != nil {
				return err
			}
			if err := requireContextRows(result, contextstate.ErrWorktreeDeleted); err != nil {
				return err
			}
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

// pauseAfterWorktreeFenceCheck is a deterministic test hook. It runs after
// requireActiveWorktreeTx confirms the instance is active, before the fenced
// mutation proceeds. Tests use it to interleave a stale in-flight mutation
// with a concurrent deletion (plan 57 test #9). Storage tests are sequential;
// do not arm it from parallel tests. It is a no-op in production.
var pauseAfterWorktreeFenceCheck = func() {}

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
	pauseAfterWorktreeFenceCheck()
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
