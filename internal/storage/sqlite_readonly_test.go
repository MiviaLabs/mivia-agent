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
