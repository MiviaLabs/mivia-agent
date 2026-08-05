package storage

import (
	"context"
	"database/sql"
	"errors"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

func authorizeUnboundContextSessionTx(ctx context.Context, tx *sql.Tx, principal contextstate.Principal, sessionID string) (contextSessionRow, error) {
	row, err := authorizeContextSessionTx(ctx, tx, principal, sessionID)
	if err != nil && !errors.Is(err, contextstate.ErrSessionTombstoned) {
		return row, err
	}
	if bindingErr := requireWorktreeSessionBinding(row, contextstate.WorktreeInstance{}); bindingErr != nil {
		return row, bindingErr
	}
	return row, err
}
