package storage

import (
	"context"
	"database/sql"
	"errors"

	"github.com/MiviaLabs/mivia-agent/internal/context/state"
)

func authorizeUnboundContextSessionTx(ctx context.Context, tx *sql.Tx, principal state.Principal, sessionID string) (contextSessionRow, error) {
	row, err := authorizeContextSessionTx(ctx, tx, principal, sessionID)
	if err != nil && !errors.Is(err, state.ErrSessionTombstoned) {
		return row, err
	}
	if bindingErr := requireWorktreeSessionBinding(row, state.WorktreeInstance{}); bindingErr != nil {
		return row, bindingErr
	}
	return row, err
}
