package memory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Open() hardening & chmod error branches
// ---------------------------------------------------------------------------

func TestGapOpenSQLiteStoreOrgChmodError(t *testing.T) {
	// The org store must fail closed (not silently stay world-readable) when
	// the privacy chmod cannot be applied.
	orig := chmodFile
	t.Cleanup(func() { chmodFile = orig })
	chmodFile = func(string, os.FileMode) error { return errGapChmod }
	cfg := Config{
		Backend:          BackendSQLite,
		ProjectPath:      filepath.Join(t.TempDir(), "project.db"),
		OrgPath:          filepath.Join(t.TempDir(), "org.db"),
		OrgID:            "github.com/acme",
		MaxEntryBytes:    8192,
		MaxEntries:       5,
		MaxSearchResults: 8,
	}
	if _, err := Open(cfg); !errors.Is(err, errGapChmod) {
		t.Fatalf("Open = %v, want chmod error %v", err, errGapChmod)
	}
}

func TestGapOpenSQLiteStoreOrgDirChmodError(t *testing.T) {
	// Same for the org directory: the file chmod succeeds, the directory
	// chmod fails, and Open must still fail closed.
	orig := chmodFile
	t.Cleanup(func() { chmodFile = orig })
	dir := t.TempDir()
	orgPath := filepath.Join(dir, "org.db")
	chmodFile = func(p string, _ os.FileMode) error {
		if p == orgPath {
			return nil // file chmod succeeds
		}
		return errGapChmod // directory chmod fails
	}
	cfg := Config{
		Backend:          BackendSQLite,
		ProjectPath:      filepath.Join(dir, "project.db"),
		OrgPath:          orgPath,
		OrgID:            "github.com/acme",
		MaxEntryBytes:    8192,
		MaxEntries:       5,
		MaxSearchResults: 8,
	}
	if _, err := Open(cfg); !errors.Is(err, errGapChmod) {
		t.Fatalf("Open = %v, want chmod error %v", err, errGapChmod)
	}
}

func TestGapOpenSQLiteStoreProjectHardenChmodError(t *testing.T) {
	// An ad-hoc temp-dir project store must fail closed when the privacy
	// chmod cannot be applied, the same as the org store.
	orig := chmodFile
	t.Cleanup(func() { chmodFile = orig })
	chmodFile = func(string, os.FileMode) error { return errGapChmod }
	cfg := Config{
		Backend:          BackendSQLite,
		ProjectPath:      filepath.Join(t.TempDir(), "project.db"),
		HardenTempStore:  true,
		MaxEntryBytes:    8192,
		MaxEntries:       5,
		MaxSearchResults: 8,
	}
	if _, err := Open(cfg); !errors.Is(err, errGapChmod) {
		t.Fatalf("Open = %v, want chmod error %v", err, errGapChmod)
	}
}

func TestGapOpenSQLiteStoreProjectHardenDirChmodError(t *testing.T) {
	// Same for the project directory: the file chmod succeeds, the
	// directory chmod fails, and Open must still fail closed.
	orig := chmodFile
	t.Cleanup(func() { chmodFile = orig })
	dir := t.TempDir()
	projectPath := filepath.Join(dir, "project.db")
	chmodFile = func(p string, _ os.FileMode) error {
		if p == projectPath {
			return nil // file chmod succeeds
		}
		return errGapChmod // directory chmod fails
	}
	cfg := Config{
		Backend:          BackendSQLite,
		ProjectPath:      projectPath,
		HardenTempStore:  true,
		MaxEntryBytes:    8192,
		MaxEntries:       5,
		MaxSearchResults: 8,
	}
	if _, err := Open(cfg); !errors.Is(err, errGapChmod) {
		t.Fatalf("Open = %v, want chmod error %v", err, errGapChmod)
	}
}

// TestGapOpenSQLiteStoreProjectHardenFileChmodErrorAfterDirCreated pins the
// post-open project file chmod arm: the directory chmods (ensureHardenedDir,
// pre-open) pass, the file chmod fails, and Open must close the project DB
// and fail closed. An always-failing seam cannot reach this arm - it trips
// ensureHardenedDir first - so the mock passes every 0700 call.
func TestGapOpenSQLiteStoreProjectHardenFileChmodErrorAfterDirCreated(t *testing.T) {
	orig := chmodFile
	t.Cleanup(func() { chmodFile = orig })
	chmodFile = func(_ string, mode os.FileMode) error {
		if mode == 0o600 {
			return errGapChmod
		}
		return nil
	}
	cfg := Config{
		Backend:          BackendSQLite,
		ProjectPath:      filepath.Join(t.TempDir(), "project.db"),
		HardenTempStore:  true,
		MaxEntryBytes:    8192,
		MaxEntries:       5,
		MaxSearchResults: 8,
	}
	if _, err := Open(cfg); !errors.Is(err, errGapChmod) {
		t.Fatalf("Open = %v, want chmod error %v", err, errGapChmod)
	}
}

// TestGapOpenSQLiteStoreProjectHardenPostOpenDirChmodError pins the last
// arm: the file chmod succeeds and the SECOND directory chmod (the
// post-open defense-in-depth one, after ensureHardenedDir's pre-open pass)
// fails; Open must still close the project DB and fail closed.
func TestGapOpenSQLiteStoreProjectHardenPostOpenDirChmodError(t *testing.T) {
	orig := chmodFile
	t.Cleanup(func() { chmodFile = orig })
	dirChmods := 0
	chmodFile = func(_ string, mode os.FileMode) error {
		if mode == 0o700 {
			dirChmods++
			if dirChmods == 2 {
				return errGapChmod
			}
		}
		return nil
	}
	cfg := Config{
		Backend:          BackendSQLite,
		ProjectPath:      filepath.Join(t.TempDir(), "project.db"),
		HardenTempStore:  true,
		MaxEntryBytes:    8192,
		MaxEntries:       5,
		MaxSearchResults: 8,
	}
	if _, err := Open(cfg); !errors.Is(err, errGapChmod) {
		t.Fatalf("Open = %v, want chmod error %v", err, errGapChmod)
	}
}

// TestGapEnsureHardenedDirError covers ensureHardenedDir's two failure modes
// (MkdirAll and chmod) reached through Open, before openMemoryDB/sql.Open
// ever touch the path. Both must fail closed with no leaked DB handle: this
// runs before any *sql.DB for the tier under test is opened, so there is
// nothing for either branch to leak.
func TestGapEnsureHardenedDirError(t *testing.T) {
	t.Run("mkdir failure", func(t *testing.T) {
		dir := t.TempDir()
		blocker := filepath.Join(dir, "blocker")
		if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		// The project dir's parent collides with a regular file, so
		// ensureHardenedDir's os.MkdirAll fails before openMemoryDB ever runs.
		projectPath := filepath.Join(blocker, "sub", "project.db")
		_, err := Open(Config{Backend: BackendSQLite, ProjectPath: projectPath, HardenTempStore: true})
		if err == nil || !strings.Contains(err.Error(), "memory project dir") {
			t.Fatalf("Open with an uncreatable hardened dir must fail, got %v", err)
		}
	})
	t.Run("chmod failure", func(t *testing.T) {
		orig := chmodFile
		t.Cleanup(func() { chmodFile = orig })
		chmodFile = func(string, os.FileMode) error { return errGapChmod }
		dir := t.TempDir()
		_, err := Open(Config{Backend: BackendSQLite, ProjectPath: filepath.Join(dir, "sub", "project.db"), HardenTempStore: true})
		if !errors.Is(err, errGapChmod) {
			t.Fatalf("Open = %v, want chmod error %v", err, errGapChmod)
		}
	})
}

func TestGapCheckpointNilDB(t *testing.T) {
	// A nil database is a no-op checkpoint (e.g. an unconfigured org store).
	s := &sqliteStore{}
	if err := s.checkpoint(context.Background(), nil); err != nil {
		t.Fatalf("checkpoint(nil) = %v, want nil", err)
	}
}
