package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

var _ contextstate.SessionReclaimer = (*SQLite)(nil)

// ReclaimSession transfers write ownership of an existing, non-tombstoned
// live context session to principal's own freshly minted capability, then
// returns its current snapshot. It exists because Principal.capability is
// minted fresh and random per process and never persisted anywhere it could
// be recovered - a later process resuming a session by id (an id it learned
// through LoadSession/ListSessions, both scoped only to workspace+subject
// with no capability check) has no way to reconstruct the capability the
// original process held, so authorizing every other durable-write path on an
// exact capability match would make cross-process resume impossible by
// construction. Reclaiming is scoped the same way those reads already are:
// knowing the session's id, workspace and subject is what LoadSession and
// DeleteSessionSnapshot already treat as sufficient authority for the same
// session, so extending that authority to "take over its capability" adds no
// new trust boundary.
//
// A managed worktree session is rejected: those are addressed by name
// through the chat_sessions catalog (worktree_catalog_keys), never through
// this capability-gated context_sessions row, so reclaiming one here would
// be meaningless.
func (s *SQLite) ReclaimSession(ctx context.Context, principal contextstate.Principal, sessionID string) (contextstate.Snapshot, error) {
	if err := principal.Validate(); err != nil {
		return contextstate.Snapshot{}, err
	}
	if !principal.IsBound() || sessionID != principal.SessionID {
		return contextstate.Snapshot{}, contextstate.ErrPrincipalMismatch
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return contextstate.Snapshot{}, err
	}
	var subjectID string
	var tombstoned int
	var instanceID sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT subject_id,tombstoned,instance_id FROM context_sessions WHERE workspace_id=? AND session_id=?`, principal.WorkspaceID, sessionID).Scan(&subjectID, &tombstoned, &instanceID)
	if errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		return contextstate.Snapshot{}, contextstate.ErrSessionNotFound
	}
	if err != nil {
		_ = tx.Rollback()
		return contextstate.Snapshot{}, err
	}
	if subjectID != principal.SubjectID {
		_ = tx.Rollback()
		return contextstate.Snapshot{}, contextstate.ErrPrincipalMismatch
	}
	if tombstoned != 0 {
		_ = tx.Rollback()
		return contextstate.Snapshot{}, contextstate.ErrSessionTombstoned
	}
	if instanceID.Valid {
		_ = tx.Rollback()
		return contextstate.Snapshot{}, fmt.Errorf("%w: managed worktree sessions cannot be reclaimed", contextstate.ErrInvalidDTO)
	}
	result, err := tx.ExecContext(ctx, `UPDATE context_sessions SET capability_digest=? WHERE workspace_id=? AND session_id=? AND subject_id=? AND tombstoned=0 AND instance_id IS NULL`, principal.CapabilityDigest(), principal.WorkspaceID, sessionID, principal.SubjectID)
	if err != nil {
		_ = tx.Rollback()
		return contextstate.Snapshot{}, err
	}
	if err := requireCatalogMutation(result); err != nil {
		_ = tx.Rollback()
		return contextstate.Snapshot{}, err
	}
	snapshot, err := loadContextTx(ctx, tx, principal, sessionID, contextstate.WorktreeInstance{})
	if err != nil {
		_ = tx.Rollback()
		return contextstate.Snapshot{}, err
	}
	return snapshot, tx.Commit()
}
