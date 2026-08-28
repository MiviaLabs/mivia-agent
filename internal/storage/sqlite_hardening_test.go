package storage

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSQLiteHardeningModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no chmod permission bits")
	}
	dir := t.TempDir()
	storePath := filepath.Join(dir, "sub", "orchestration.db")

	store, err := OpenSQLiteWithOptions(storePath, Options{Harden: true})
	if err != nil {
		t.Fatalf("OpenSQLiteWithOptions hardened: %v", err)
	}
	defer store.Close()

	fi, err := os.Stat(storePath)
	if err != nil {
		t.Fatalf("stat store file: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("store file mode = %o, want 0600", perm)
	}

	dirFi, err := os.Stat(filepath.Dir(storePath))
	if err != nil {
		t.Fatalf("stat store dir: %v", err)
	}
	if perm := dirFi.Mode().Perm(); perm != 0o700 {
		t.Errorf("store dir mode = %o, want 0700", perm)
	}
}

func TestSQLiteNonHardenedUntouched(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no chmod permission bits")
	}
	dir := t.TempDir()
	storePath := filepath.Join(dir, "custom.db")

	store, err := OpenSQLite(storePath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer store.Close()

	fi, err := os.Stat(storePath)
	if err != nil {
		t.Fatalf("stat store file: %v", err)
	}
	// hardening must not touch an operator-managed store: not 0600.
	if perm := fi.Mode().Perm(); perm == 0o600 {
		t.Errorf("non-hardened store file mode = %o, want the default (hardening leaked)", perm)
	}
}

func TestSQLiteHardeningChmodFailureFailsClosed(t *testing.T) {
	errMock := errors.New("chmod mock fail")
	orig := chmodFile
	t.Cleanup(func() { chmodFile = orig })
	chmodFile = func(string, os.FileMode) error { return errMock }

	dir := t.TempDir()
	storePath := filepath.Join(dir, "sub", "fail.db")

	store, err := OpenSQLiteWithOptions(storePath, Options{Harden: true})
	if !errors.Is(err, errMock) {
		if store != nil {
			store.Close()
		}
		t.Fatalf("OpenSQLiteWithOptions = (%v, %v), want error %v", store, err, errMock)
	}
}
