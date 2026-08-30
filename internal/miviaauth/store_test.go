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

func TestSaveLoadRoundTripPersistsRefreshToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	want := Token{
		Bearer:         "bearer-1",
		RefreshToken:   "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
		ExpiresAt:      time.Now().Add(time.Hour).UTC().Truncate(time.Second),
		OrganizationID: "org-1",
		Role:           "member",
	}
	if err := Save(path, want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.RefreshToken != want.RefreshToken {
		t.Errorf("RefreshToken = %q, want %q", got.RefreshToken, want.RefreshToken)
	}
	if !got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Errorf("ExpiresAt = %v, want %v", got.ExpiresAt, want.ExpiresAt)
	}
}

// TestLoadLegacyAuthJSONWithoutRefreshTokenDoesNotFail covers upgrading over
// a session file written before the /v1 contract. Load must be permissive:
// the file is not corrupt, it is old, and deciding what to do about that
// belongs to Service, not to the decoder. Never a crash, never a parse error.
func TestLoadLegacyAuthJSONWithoutRefreshTokenDoesNotFail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	legacy := `{"Bearer":"go-mivia-era-bearer","ExpiresAt":"2030-01-01T00:00:00Z","OrganizationID":"org-1","Role":"admin"}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v, want a legacy file to load cleanly", err)
	}
	if got.RefreshToken != "" {
		t.Errorf("RefreshToken = %q, want empty -- that emptiness is the legacy marker", got.RefreshToken)
	}
	if got.Bearer != "go-mivia-era-bearer" {
		t.Errorf("Bearer = %q, want the legacy value preserved", got.Bearer)
	}
}

// TestLoadIgnoresUnknownFieldsFromAFutureRelease: a newer mivia may add a
// field; an older one must not treat the file as corrupt and delete a working
// session.
func TestLoadIgnoresUnknownFieldsFromAFutureRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	future := `{"Bearer":"b","RefreshToken":"r","ExpiresAt":"2030-01-01T00:00:00Z","SomethingNew":42}`
	if err := os.WriteFile(path, []byte(future), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.RefreshToken != "r" {
		t.Errorf("RefreshToken = %q", got.RefreshToken)
	}
}

// TestSaveSyncsBeforeRename pins the durability ordering, not merely that a
// sync happened. What this file holds is a one-time-use refresh token whose
// predecessor the server has already destroyed, so a write that reaches only
// the page cache is a lost session.
func TestSaveSyncsBeforeRename(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")

	var (
		called            bool
		destExistedAtSync bool
	)
	original := syncFile
	syncFile = func(f *os.File) error {
		called = true
		_, err := os.Stat(path)
		destExistedAtSync = err == nil
		return f.Sync()
	}
	t.Cleanup(func() { syncFile = original })

	if err := Save(path, Token{Bearer: "b", RefreshToken: "r"}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if !called {
		t.Fatal("Save() never fsynced the temp file")
	}
	if destExistedAtSync {
		t.Error("the destination already existed when Save fsynced; the sync must happen before the rename installs it")
	}
}

func TestSaveSyncFailureIsReportedAndInstallsNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	original := syncFile
	syncFile = func(*os.File) error { return errors.New("no space left on device") }
	t.Cleanup(func() { syncFile = original })

	if err := Save(path, Token{Bearer: "b", RefreshToken: "r"}); err == nil {
		t.Fatal("Save() returned nil when the fsync failed")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("Save() installed a file it could not flush")
	}
}
