package storage

import (
	"context"
	"database/sql"
	"errors"

	"github.com/MiviaLabs/mivia-agent/internal/context/state"
)

type catalogKeyQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func rejectManagedCatalogKey(ctx context.Context, queryer catalogKeyQueryer, principal state.Principal, name string) error {
	var found int
	err := queryer.QueryRowContext(ctx, `SELECT 1 FROM worktree_catalog_keys WHERE workspace_id=? AND subject_id=? AND storage_key=?`, principal.WorkspaceID, principal.SubjectID, name).Scan(&found)
	if err == nil {
		return state.ErrWorktreeDeleted
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

func requireCatalogMutation(result sql.Result) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return state.ErrWorktreeDeleted
	}
	return nil
}
