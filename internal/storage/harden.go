package storage

import (
	"database/sql"
	"fmt"
	"os"
)

// Options tunes OpenSQLiteWithOptions.
type Options struct {
	// Harden marks the path as an ad-hoc, OS-temp-dir-backed store (see
	// config.TempStorePath): the directory chain is created 0700 before the
	// sqlite file exists and the file is chmod 0600 after open, failing
	// closed on error. WAL mode still creates transient -wal/-shm sidecars,
	// but only inside the hardened 0700 directory and Close folds the WAL
	// back into the main file, so the directory is the access boundary.
	Harden bool
}

// chmodFile is a test seam: the OS-error branches of the hardened open are
// not reachable with a real filesystem (an owner can always chmod its own
// files).
var chmodFile = os.Chmod

// ensureHardenedDir creates dir at 0700 if it does not already exist (so the
// os.MkdirAll(dir, 0755) inside the open path that runs immediately
// afterward is a documented no-op on an already-existing directory) and then
// chmods it to 0700 unconditionally, correcting a directory left at a looser
// mode by a session that ran before this hardening shipped. This must run
// BEFORE sql.Open ever touches the path: modernc.org/sqlite creates a
// brand-new database file at 0644 regardless of umask, so a
// world-traversable (0755) directory at file-creation time is a real, if
// brief, local privacy exposure even though the file gets chmod'd to 0600
// moments later.
func ensureHardenedDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return chmodFile(dir, 0o700)
}

// hardenOpenedStore applies the post-open half of the Harden contract: the
// database file to 0600 and its directory to 0700. The directory half of the
// contract (create the chain 0700 before the file exists) is ensureHardenedDir
// and runs before sql.Open. Any chmod failure closes both pools and fails
// closed.
func hardenOpenedStore(path, dir string, db, writeDB *sql.DB) error {
	if err := chmodFile(path, 0o600); err != nil {
		writeDB.Close()
		db.Close()
		return fmt.Errorf("chmod db file %s: %w", path, err)
	}
	if err := chmodFile(dir, 0o700); err != nil {
		writeDB.Close()
		db.Close()
		return fmt.Errorf("chmod db dir %s: %w", dir, err)
	}
	return nil
}
