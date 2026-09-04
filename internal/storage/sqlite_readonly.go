package storage

import (
	"database/sql"
	"fmt"
	"os"
)

// OpenSQLiteReadOnly opens an existing SQLite store without schema migration,
// WAL setup, writes, or checkpointing. The returned store owns its handle.
func OpenSQLiteReadOnly(path string) (*SQLite, error) {
	if path == "" {
		return nil, fmt.Errorf("open sqlite read-only store: empty path")
	}
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("open sqlite read-only store: %w", err)
	}
	db, err := sql.Open("sqlite", sqliteReadOnlyDSN(path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	if err := rejectNewerContextSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	return &SQLite{db: db, path: path}, nil
}
