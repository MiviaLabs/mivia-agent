package memory

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// SQLite backend
// ---------------------------------------------------------------------------

const memorySchema = `
CREATE TABLE IF NOT EXISTS memories (
  id TEXT PRIMARY KEY,
  scope TEXT NOT NULL,
  org TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL,
  summary TEXT NOT NULL,
  verdict TEXT NOT NULL DEFAULT 'neutral',
  tags TEXT NOT NULL DEFAULT '',
  created TEXT NOT NULL DEFAULT '',
  content TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  tier TEXT NOT NULL DEFAULT 'archive' CHECK(tier IN ('core','archive'))
);
CREATE INDEX IF NOT EXISTS idx_memories_scope_org ON memories(scope, org);
`

type sqliteStore struct {
	projectDB *sql.DB
	orgDB     *sql.DB // nil when OrgPath is empty
	cfg       Config
	mu        sync.Mutex // serializes Save across the two files (cap check + insert)
	fts       bool       // FTS5 available at open; gates FTS index sync in Save/consolidation (Search always runs the LIKE path)
}

func openSQLiteStore(cfg Config) (*sqliteStore, error) {
	projectPath := strings.TrimSpace(cfg.ProjectPath)
	if projectPath == "" {
		return nil, errors.New("memory: project database path is required")
	}
	// An ad-hoc temp-dir store has no operator-managed project directory: harden
	// it to 0700 before the sqlite file is ever created inside it, so the file
	// is never briefly reachable through a world-traversable directory (see
	// ensureHardenedDir).
	if cfg.HardenTempStore {
		if err := ensureHardenedDir(filepath.Dir(projectPath)); err != nil {
			return nil, fmt.Errorf("memory project dir %s: %w", filepath.Dir(projectPath), err)
		}
	}
	projectDB, fts, err := openMemoryDB(projectPath, cfg.ReadOnly)
	if err != nil {
		return nil, fmt.Errorf("memory project store %s: %w", projectPath, err)
	}
	s := &sqliteStore{projectDB: projectDB, cfg: cfg, fts: fts}
	// An ad-hoc temp-dir store has no operator-managed project directory
	// protecting it (unlike the general project-tier case), so harden it the
	// same way the org store is always hardened.
	if cfg.HardenTempStore {
		if err := chmodFile(projectPath, 0o600); err != nil {
			projectDB.Close()
			return nil, fmt.Errorf("memory project store %s: %w", projectPath, err)
		}
		if err := chmodFile(filepath.Dir(projectPath), 0o700); err != nil {
			projectDB.Close()
			return nil, fmt.Errorf("memory project dir %s: %w", filepath.Dir(projectPath), err)
		}
	}
	// The org store is created only when an org identity is configured: with no
	// org_id the feature is project-only, and an unconfigured org must not
	// create side-effect files (or fail the session) in the user's home.
	if orgPath := strings.TrimSpace(cfg.OrgPath); orgPath != "" && cfg.OrgID != "" {
		// Org hardening is unconditional (never gated by HardenTempStore): the
		// org store is always user-owned, never operator-managed. Harden the
		// directory before the sqlite file is ever created inside it, same as
		// the project/ad-hoc block above.
		if err := ensureHardenedDir(filepath.Dir(orgPath)); err != nil {
			projectDB.Close()
			return nil, fmt.Errorf("memory org dir %s: %w", filepath.Dir(orgPath), err)
		}
		orgDB, _, err := openMemoryDB(orgPath, cfg.ReadOnly)
		if err != nil {
			projectDB.Close()
			return nil, fmt.Errorf("memory org store %s: %w", orgPath, err)
		}
		// The org store is user-owned (not repo content): keep it private so
		// other local users cannot read or write org memories on shared machines.
		if err := chmodFile(orgPath, 0o600); err != nil {
			orgDB.Close()
			projectDB.Close()
			return nil, fmt.Errorf("memory org store %s: %w", orgPath, err)
		}
		if err := chmodFile(filepath.Dir(orgPath), 0o700); err != nil {
			orgDB.Close()
			projectDB.Close()
			return nil, fmt.Errorf("memory org dir %s: %w", filepath.Dir(orgPath), err)
		}
		s.orgDB = orgDB
	}
	return s, nil
}

// chmodFile is a test seam: the OS-error branches of openSQLiteStore are not
// reachable with a real filesystem (an owner can always chmod its own files).
var chmodFile = os.Chmod

// ensureHardenedDir creates dir at 0700 if it does not already exist (so the
// os.MkdirAll(dir, 0755) inside openMemoryDB that runs immediately afterward
// is a documented no-op on an already-existing directory) and then chmods it
// to 0700 unconditionally, correcting a directory left at a looser mode by a
// session that ran before this fix shipped. This must run BEFORE
// openMemoryDB/sql.Open ever touch the path: modernc.org/sqlite creates a
// brand-new database file at 0644 regardless of umask, so a world-traversable
// (0755) directory at file-creation time is a real, if brief, local privacy
// exposure even though the file gets chmod'd to 0600 moments later.
func ensureHardenedDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return chmodFile(dir, 0o700)
}

func openMemoryDB(path string, readOnly bool) (*sql.DB, bool, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, false, err
	}
	dsn := sqliteMemoryDSN(path)
	if readOnly {
		dsn = sqliteMemoryDSNReadOnly(path)
	}
	// sql.Open fails only on an unknown driver name; modernc.org/sqlite
	// registers "sqlite" in its init, so the error branch would be dead code
	// (diff-coverage gate).
	db, _ := sql.Open("sqlite", dsn)
	db.SetMaxOpenConns(1)
	if readOnly {
		// No WAL switch, schema exec, or FTS rebuild (all writes); the DSN
		// carries query_only, and fts=false routes search to the LIKE path.
		return db, false, nil
	}
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, false, err
		}
	}
	if _, err := db.Exec(memorySchema); err != nil {
		db.Close()
		return nil, false, err
	}
	if err := migrateTierColumn(db); err != nil {
		db.Close()
		return nil, false, err
	}
	// The FTS5 index is an optional acceleration layer: on a build without
	// FTS5 (or when the index cannot be created) the store transparently uses
	// the Phase-1 LIKE path with identical results and no error.
	return db, ensureFTSIndex(db), nil
}
