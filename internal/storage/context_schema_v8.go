package storage

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

func applyContextSchemaV8(db *sql.DB) error {
	return applyContextMigration(db, 8, `CREATE TABLE IF NOT EXISTS worktree_catalog_keys(
		workspace_id TEXT NOT NULL, subject_id TEXT NOT NULL, instance_id TEXT NOT NULL,
		entity TEXT NOT NULL, name TEXT NOT NULL, storage_key TEXT NOT NULL,
		PRIMARY KEY(workspace_id,subject_id,instance_id,entity,name),
		UNIQUE(workspace_id,subject_id,storage_key))`)
}

func ensureContextSchemaV8(db *sql.DB) error {
	if err := ensureContextSchemaV7(db); err != nil {
		return err
	}
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS worktree_catalog_keys(
		workspace_id TEXT NOT NULL, subject_id TEXT NOT NULL, instance_id TEXT NOT NULL,
		entity TEXT NOT NULL, name TEXT NOT NULL, storage_key TEXT NOT NULL,
		PRIMARY KEY(workspace_id,subject_id,instance_id,entity,name),
		UNIQUE(workspace_id,subject_id,storage_key))`)
	return err
}

func worktreeCatalogKeyTx(ctx context.Context, tx *sql.Tx, principal contextstate.Principal, instance contextstate.WorktreeInstance, entity, name string) (string, error) {
	var key string
	err := tx.QueryRowContext(ctx, `SELECT storage_key FROM worktree_catalog_keys WHERE workspace_id=? AND subject_id=? AND instance_id=? AND entity=? AND name=?`, principal.WorkspaceID, principal.SubjectID, instance.ID, entity, name).Scan(&key)
	if err == nil {
		return key, nil
	}
	if err != sql.ErrNoRows {
		return "", err
	}
	key, err = newContextID("wtcatalog_")
	if err != nil {
		return "", fmt.Errorf("create worktree catalog key: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO worktree_catalog_keys(workspace_id,subject_id,instance_id,entity,name,storage_key) VALUES(?,?,?,?,?,?)`, principal.WorkspaceID, principal.SubjectID, instance.ID, entity, name, key)
	return key, err
}

func loadWorktreeCatalogKeyTx(ctx context.Context, tx *sql.Tx, principal contextstate.Principal, instance contextstate.WorktreeInstance, entity, name string) (string, error) {
	var key string
	err := tx.QueryRowContext(ctx, `SELECT storage_key FROM worktree_catalog_keys WHERE workspace_id=? AND subject_id=? AND instance_id=? AND entity=? AND name=?`, principal.WorkspaceID, principal.SubjectID, instance.ID, entity, name).Scan(&key)
	if err == sql.ErrNoRows {
		return "", contextstate.ErrSessionNotFound
	}
	return key, err
}
