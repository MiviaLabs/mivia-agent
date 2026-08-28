package storage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The three failure arms of the hardened open path must each fail closed:
// a chmod refusal at the file step, a chmod refusal at the directory step
// (after the file chmod succeeded), and a MkdirAll refusal while creating
// the hardened directory chain. None may leak an open store handle.

func TestSQLiteHardeningFileChmodFailureFailsClosed(t *testing.T) {
	orig := chmodFile
	t.Cleanup(func() { chmodFile = orig })
	chmodFile = func(_ string, mode os.FileMode) error {
		if mode == 0o600 {
			return errors.New("chmod file refused")
		}
		return nil
	}

	storePath := filepath.Join(t.TempDir(), "sub", "file-fail.db")
	store, err := OpenSQLiteWithOptions(storePath, Options{Harden: true})
	if err == nil {
		if store != nil {
			store.Close()
		}
		t.Fatal("OpenSQLiteWithOptions = nil error, want the file chmod failure")
	}
	if !strings.Contains(err.Error(), "chmod db file") {
		t.Errorf("err = %v, want it to name the chmod db file step", err)
	}
}

func TestSQLiteHardeningDirChmodFailureFailsClosed(t *testing.T) {
	orig := chmodFile
	t.Cleanup(func() { chmodFile = orig })
	// The first 0700 chmod is ensureHardenedDir's, before the file exists;
	// it must pass so the open reaches the post-open directory chmod, whose
	// refusal is the arm under test.
	dirChmods := 0
	chmodFile = func(_ string, mode os.FileMode) error {
		if mode == 0o700 {
			dirChmods++
			if dirChmods == 2 {
				return errors.New("chmod dir refused")
			}
		}
		return nil
	}

	storePath := filepath.Join(t.TempDir(), "sub", "dir-fail.db")
	store, err := OpenSQLiteWithOptions(storePath, Options{Harden: true})
	if err == nil {
		if store != nil {
			store.Close()
		}
		t.Fatal("OpenSQLiteWithOptions = nil error, want the dir chmod failure")
	}
	if !strings.Contains(err.Error(), "chmod db dir") {
		t.Errorf("err = %v, want it to name the chmod db dir step", err)
	}
}

func TestEnsureHardenedDirMkdirAllError(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureHardenedDir(filepath.Join(blocker, "nested")); err == nil {
		t.Fatal("ensureHardenedDir over a regular-file path component = nil error, want the MkdirAll failure")
	}
}
