package storage

import (
	"context"
	"database/sql"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

var _ contextstate.SessionTitleCatalog = (*SQLite)(nil)

// SetSessionTitle updates display metadata for the principal's bound session.
func (s *SQLite) SetSessionTitle(ctx context.Context, principal contextstate.Principal, title string, instance contextstate.WorktreeInstance) error {
	var err error
	title, err = contextstate.NormalizeSessionTitle(title)
	if err != nil {
		return err
	}
	if err := instance.Validate(); err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, func(tx *sql.Tx) error {
		row, err := authorizeContextSessionTx(ctx, tx, principal, principal.SessionID)
		if err != nil {
			return err
		}
		if err := requireWorktreeSessionBinding(row, instance); err != nil {
			return err
		}
		if err := requireActiveWorktreeTx(ctx, tx, principal, instance); err != nil {
			return err
		}
		var value any
		if title != "" {
			value = title
		}
		result, err := tx.ExecContext(ctx, `UPDATE context_sessions SET title=? WHERE workspace_id=? AND subject_id=? AND session_id=? AND capability_digest=? AND tombstoned=0 AND instance_id IS ?`, value, principal.WorkspaceID, principal.SubjectID, principal.SessionID, principal.CapabilityDigest(), nullableText(instance.ID))
		if err != nil {
			return err
		}
		return requireCatalogMutation(result)
	})
}
