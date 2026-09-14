package storage

import (
	"context"
	"database/sql"

	"github.com/MiviaLabs/mivia-agent/internal/context/state"
)

var _ state.SessionTitleCatalog = (*SQLite)(nil)

// SetSessionTitle updates display metadata for an authorized context session.
func (s *SQLite) SetSessionTitle(ctx context.Context, principal state.Principal, sessionID, title string, instance state.WorktreeInstance) error {
	var err error
	title, err = state.NormalizeSessionTitle(title)
	if err != nil {
		return err
	}
	if err := instance.Validate(); err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, func(tx *sql.Tx) error {
		row, err := authorizeContextSessionTitleTx(ctx, tx, principal, sessionID)
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
		result, err := tx.ExecContext(ctx, `UPDATE context_sessions SET title=? WHERE workspace_id=? AND subject_id=? AND session_id=? AND tombstoned=0 AND instance_id IS ?`, value, principal.WorkspaceID, principal.SubjectID, sessionID, nullableText(instance.ID))
		if err != nil {
			return err
		}
		return requireCatalogMutation(result)
	})
}

func authorizeContextSessionTitleTx(ctx context.Context, tx *sql.Tx, principal state.Principal, sessionID string) (contextSessionRow, error) {
	if err := principal.Validate(); err != nil {
		return contextSessionRow{}, err
	}
	if !principal.IsBound() || sessionID == "" {
		return contextSessionRow{}, state.ErrPrincipalMismatch
	}
	var row contextSessionRow
	var subjectID string
	var capability string
	var tombstoned int
	err := tx.QueryRowContext(ctx, `SELECT subject_id,capability_digest,session_revision,durable_revision,source_sequence,provider,model,binding_generation,tombstoned,instance_id FROM context_sessions WHERE workspace_id=? AND session_id=?`, principal.WorkspaceID, sessionID).Scan(&subjectID, &capability, &row.SessionRevision, &row.DurableRevision, &row.SourceSequence, &row.Provider, &row.Model, &row.BindingGeneration, &tombstoned, &row.InstanceID)
	if err == sql.ErrNoRows {
		return contextSessionRow{}, state.ErrSessionNotFound
	}
	if err != nil {
		return contextSessionRow{}, err
	}
	if subjectID != principal.SubjectID {
		return contextSessionRow{}, state.ErrPrincipalMismatch
	}
	row.Tombstoned = tombstoned != 0
	if row.Tombstoned {
		return row, state.ErrSessionTombstoned
	}
	return row, nil
}
