package storage

import (
	"context"
	"database/sql"
)

// PutContent stores raw bytes keyed by a content-addressed reference
// (e.g. "ref:output:xxxx"). Idempotent for the same ref.
func (s *SQLite) PutContent(ctx context.Context, ref string, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO content(ref, data) VALUES(?, ?)`, ref, data)
	return err
}

// GetContent retrieves bytes previously stored by PutContent.
// Returns ErrContentNotFound if the ref is unknown.
func (s *SQLite) GetContent(ctx context.Context, ref string) ([]byte, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT data FROM content WHERE ref = ?`, ref).Scan(&data)
	if err == sql.ErrNoRows {
		return nil, ErrContentNotFound
	}
	return data, err
}

// GrantSpool durably records that principal holds a read grant on a remainder
// ref. INSERT OR IGNORE keeps the first grant for a (ref, principal) pair, so
// re-spooling the same ref for the same principal is idempotent.
func (s *SQLite) GrantSpool(ctx context.Context, ref, principal string) error {
	if ref == "" || principal == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO spool_grants(ref, principal) VALUES(?, ?)`, ref, principal)
	return err
}

// CheckSpoolGrant reports whether principal holds a durable read grant on ref.
func (s *SQLite) CheckSpoolGrant(ctx context.Context, ref, principal string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM spool_grants WHERE ref = ? AND principal = ?)`, ref, principal).Scan(&exists)
	return exists, err
}
