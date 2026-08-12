package miviaauth

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func testToken() Token {
	return Token{
		Bearer:         "test-bearer-token",
		ExpiresAt:      time.Date(2026, 8, 13, 18, 0, 0, 0, time.UTC),
		OrganizationID: "org-123",
		Role:           "admin",
	}
}

func TestSaveThenLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	want := testToken()

	if err := Save(path, want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Errorf("ExpiresAt = %v, want %v", got.ExpiresAt, want.ExpiresAt)
	}
	got.ExpiresAt = want.ExpiresAt
	if got != want {
		t.Errorf("Load() = %+v, want %+v", got, want)
	}
}

func TestSaveFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode bits are not meaningful on windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	if err := Save(path, testToken()); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("file mode = %o, want %o", got, 0o600)
	}
}

func TestSaveParentDirMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode bits are not meaningful on windows")
	}
	dir := t.TempDir()
	nested := filepath.Join(dir, "nested", "auth.json")

	if err := Save(nested, testToken()); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	fi, err := os.Stat(filepath.Dir(nested))
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o700 {
		t.Errorf("parent dir mode = %o, want %o", got, 0o700)
	}
}

func TestLoadMissingFileReturnsErrNotFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist.json")

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want ErrNotFound")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Load() error = %v, want errors.Is(err, ErrNotFound)", err)
	}
}

func TestLoadCorruptFileReturnsNonNotFoundError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(path, []byte("not json{{{"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want a decode error")
	}
	if errors.Is(err, ErrNotFound) {
		t.Errorf("Load() error = %v, want NOT errors.Is(err, ErrNotFound)", err)
	}
}

func TestDeleteMissingFileReturnsNil(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist.json")

	if err := Delete(path); err != nil {
		t.Errorf("Delete() error = %v, want nil", err)
	}
}

func TestDeleteThenLoadReturnsErrNotFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	if err := Save(path, testToken()); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	if err := Delete(path); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	_, err := Load(path)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Load() after Delete() error = %v, want errors.Is(err, ErrNotFound)", err)
	}
}
