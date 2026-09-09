package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenSQLiteReadOnlyDoesNotCreateOrModifyStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "context.db")
	store, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ro, err := OpenSQLiteReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ro.db.ExecContext(context.Background(), `CREATE TABLE forbidden(id INTEGER)`); err == nil {
		t.Fatal("read-only store accepted a write")
	}
	if err := ro.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("read-only open/close changed database bytes")
	}
}

func TestOpenSQLiteReadOnlyRejectsEmptyPath(t *testing.T) {
	if _, err := OpenSQLiteReadOnly(""); err == nil {
		t.Fatal("OpenSQLiteReadOnly accepted an empty path")
	}
}

func TestOpenSQLiteReadOnlyRejectsMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.db")
	if _, err := OpenSQLiteReadOnly(missing); err == nil {
		t.Fatal("OpenSQLiteReadOnly accepted a path with no file")
	}
}

func TestOpenSQLiteReadOnlyRejectsNonDatabaseFile(t *testing.T) {
	notDB := filepath.Join(t.TempDir(), "notadb.db")
	if err := os.WriteFile(notDB, []byte("not a sqlite file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenSQLiteReadOnly(notDB); err == nil {
		t.Fatal("OpenSQLiteReadOnly accepted a file that is not a valid sqlite database")
	}
}

func TestOpenSQLiteReadOnlyRejectsNewerSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "context.db")
	store, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`PRAGMA user_version = 999999`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenSQLiteReadOnly(path); err == nil {
		t.Fatal("OpenSQLiteReadOnly accepted a database with a newer-than-supported schema version")
	}
}
