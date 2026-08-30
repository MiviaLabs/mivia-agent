package miviaauth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrNotFound reports that no auth token is stored at the requested path.
var ErrNotFound = errors.New("no stored auth token")

// syncFile is the fsync used by Save. It is a package-level variable so tests
// can substitute a tracking or failing wrapper, mirroring
// internal/chat/persistence_io.go's seam for the same purpose.
var syncFile = (*os.File).Sync

// Load reads the token stored at path. A missing file reports an error that
// satisfies errors.Is(err, ErrNotFound). A malformed file reports a wrapped
// decode error that does not satisfy that check.
func Load(path string) (Token, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Token{}, fmt.Errorf("miviaauth: load %s: %w", path, ErrNotFound)
		}
		return Token{}, fmt.Errorf("miviaauth: read %s: %w", path, err)
	}
	var tok Token
	if err := json.Unmarshal(data, &tok); err != nil {
		return Token{}, fmt.Errorf("miviaauth: decode %s: %w", path, err)
	}
	return tok, nil
}

// Save writes t to path as JSON, atomically and durably. The parent directory
// is created with 0o700 if missing, and the file is written with 0o600, fsynced,
// and only then renamed into place, so a partial write never lands at the
// final path.
//
// The fsync is not belt-and-braces here. What this file holds is a one-time-use
// refresh token whose predecessor the server has already destroyed, so a token
// that reaches only the page cache and dies in a crash is a lost session, not a
// retryable write. The rename itself is atomic on every platform Go supports for
// same-filesystem renames; the parent directory is deliberately NOT fsynced,
// because there is no portable way to do it on Windows and the failure it would
// close leaves the previous file intact, which is the same end state as a failed
// save.
func Save(path string, t Token) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("miviaauth: create %s: %w", dir, err)
	}
	data, err := json.Marshal(t)
	if err != nil {
		return fmt.Errorf("miviaauth: encode token: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".mivia-auth-*")
	if err != nil {
		return fmt.Errorf("miviaauth: create a temp auth file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("miviaauth: set auth file permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("miviaauth: write the auth file: %w", err)
	}
	if err := syncFile(tmp); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("miviaauth: flush the auth file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("miviaauth: close the auth file: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("miviaauth: install the auth file: %w", err)
	}
	return nil
}

// Delete removes the token stored at path. A missing file is not an error:
// Delete is idempotent.
func Delete(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("miviaauth: delete %s: %w", path, err)
	}
	return nil
}
