package memory

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The org store's open path, after the pre-open directory hardening.
//
// The org tier is user-owned, never operator-managed, so it is hardened
// unconditionally. The existing coverage trips ensureHardenedDir, which runs
// BEFORE the database is opened; what is left is every arm that runs after,
// where a project database is already open. Each of them must fail closed AND
// close that handle: an Open that returns an error while leaking the project
// connection leaves a WAL file the caller believes was never opened.

// openProjectFDs counts the descriptors this process holds on path, waiting
// briefly for the driver to release them. It is the only way to see, from
// outside, that a failed Open let go of the project database it had already
// opened; a handle that was never closed stays counted until the deadline.
func openProjectFDs(t *testing.T, path string) int {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("descriptor accounting needs /proc")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		entries, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Skipf("no /proc/self/fd: %v", err)
		}
		n := 0
		for _, e := range entries {
			target, err := os.Readlink(filepath.Join("/proc/self/fd", e.Name()))
			if err != nil {
				continue
			}
			if strings.HasPrefix(target, path) { // path, path-wal, path-shm
				n++
			}
		}
		if n == 0 || time.Now().After(deadline) {
			return n
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func orgConfig(projectPath, orgPath string) Config {
	return Config{
		Backend:          BackendSQLite,
		ProjectPath:      projectPath,
		OrgPath:          orgPath,
		OrgID:            "github.com/acme",
		MaxEntryBytes:    8192,
		MaxEntries:       5,
		MaxSearchResults: 8,
	}
}

// TestOpenSQLiteStoreOrgDatabaseUnopenable: the org path cannot be opened as
// a database at all. Open must fail rather than fall back to a project-only
// store - an org store that silently is not there loses every org memory the
// session writes.
func TestOpenSQLiteStoreOrgDatabaseUnopenable(t *testing.T) {
	dir := t.TempDir()
	projectPath := filepath.Join(dir, "project.db")
	// A directory where the org database file belongs.
	orgPath := filepath.Join(dir, "org.db")
	if err := os.Mkdir(orgPath, 0o700); err != nil {
		t.Fatal(err)
	}

	store, err := Open(orgConfig(projectPath, orgPath))
	if err == nil {
		_ = store.Close()
		t.Fatal("an unopenable org database was accepted")
	}
	if !strings.Contains(err.Error(), "memory org store") || !strings.Contains(err.Error(), orgPath) {
		t.Errorf("err = %v, want it to name the org store path", err)
	}
	if n := openProjectFDs(t, projectPath); n != 0 {
		t.Errorf("the failed Open left %d descriptor(s) on the project database", n)
	}
}

// TestOpenSQLiteStoreOrgChmodFailsAfterOpen: the org file cannot be made
// private. Returning the store anyway would leave org memories readable by
// every local user, which is the one thing this chmod exists to prevent.
func TestOpenSQLiteStoreOrgChmodFailsAfterOpen(t *testing.T) {
	orig := chmodFile
	t.Cleanup(func() { chmodFile = orig })
	// The pre-open directory hardening (0700) must pass so the failure lands
	// on the file chmod, which is the arm under test.
	chmodFile = func(_ string, mode os.FileMode) error {
		if mode == 0o600 {
			return errGapChmod
		}
		return nil
	}
	dir := t.TempDir()
	projectPath := filepath.Join(dir, "project.db")

	store, err := Open(orgConfig(projectPath, filepath.Join(dir, "org", "org.db")))
	if !errors.Is(err, errGapChmod) {
		if err == nil {
			_ = store.Close()
		}
		t.Fatalf("Open = %v, want the chmod failure", err)
	}
	if n := openProjectFDs(t, projectPath); n != 0 {
		t.Errorf("the failed Open left %d descriptor(s) on the project database", n)
	}
}

// TestOpenSQLiteStoreOrgDirChmodFailsAfterOpen: the second, post-open
// directory chmod is the defence-in-depth one. It fails closed too - a
// world-traversable org directory is a local privacy exposure whether or not
// the file itself is 0600.
func TestOpenSQLiteStoreOrgDirChmodFailsAfterOpen(t *testing.T) {
	orig := chmodFile
	t.Cleanup(func() { chmodFile = orig })
	dirChmods := 0
	chmodFile = func(_ string, mode os.FileMode) error {
		if mode == 0o700 {
			dirChmods++
			if dirChmods == 2 { // 1 is ensureHardenedDir, pre-open
				return errGapChmod
			}
		}
		return nil
	}
	dir := t.TempDir()
	projectPath := filepath.Join(dir, "project.db")

	store, err := Open(orgConfig(projectPath, filepath.Join(dir, "org", "org.db")))
	if !errors.Is(err, errGapChmod) {
		if err == nil {
			_ = store.Close()
		}
		t.Fatalf("Open = %v, want the chmod failure", err)
	}
	if n := openProjectFDs(t, projectPath); n != 0 {
		t.Errorf("the failed Open left %d descriptor(s) on the project database", n)
	}
}

// TestOpenMemoryDBReportsAFailedTierMigration: the tier column is what
// separates core memories (auto-injected into every prompt) from archived
// ones. A database the migration cannot add it to must fail the open, not
// return a store whose every later query names a column that is not there.
//
// The migration is failed with a memories table already at SQLite's column
// limit, so ALTER TABLE ADD COLUMN cannot succeed. CREATE TABLE IF NOT EXISTS
// leaves that table alone, which is exactly the pre-existing-file case the
// migration is for.
func TestOpenMemoryDBReportsAFailedTierMigration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "project.db")

	var cols strings.Builder
	cols.WriteString("CREATE TABLE memories(id TEXT PRIMARY KEY, scope TEXT, org TEXT")
	for i := 0; i < 1997; i++ {
		cols.WriteString(", c" + strconv.Itoa(i) + " TEXT")
	}
	cols.WriteString(")")
	seed, _, err := openMemoryDB(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Exec(`DROP TABLE memories`); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Exec(cols.String()); err != nil {
		t.Fatalf("seed a table at the column limit: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	db, fts, err := openMemoryDB(path, false)
	if err == nil {
		_ = db.Close()
		t.Fatal("a database whose tier migration cannot run was opened")
	}
	if db != nil || fts {
		t.Errorf("failed open returned db=%v fts=%v, want (nil, false)", db != nil, fts)
	}
	if !strings.Contains(err.Error(), "too many columns") {
		t.Errorf("err = %v, want the failed ALTER TABLE", err)
	}
	if n := openProjectFDs(t, path); n != 0 {
		t.Errorf("the failed open left %d descriptor(s) on the database", n)
	}
}
