package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Result is one search hit.
type Result struct {
	ID      string
	Scope   Scope
	Org     string
	Title   string
	Verdict Verdict
	Tags    []string
	Created string
	Snippet string
}

// Query is a search request.
type Query struct {
	Text       string
	Scope      Scope // ScopeProject, ScopeOrg, or ScopeAll
	MaxResults int   // 0 uses the store default
}

// Store is the durable memory backend. Implementations must be safe for
// concurrent use: subagents share one store per session.
type Store interface {
	// Save validates and stores one entry. An identical re-save is
	// idempotent: it returns the existing result and stores no duplicate.
	// Save never sets tier: every entry it writes lands as "archive" (the
	// schema default). Promotion to "core" is a separate, operator-facing
	// action (PromoteToCore) - not reachable from Save, by design (D1a).
	Save(ctx context.Context, e Entry) (Result, error)
	// Search returns up to the configured limit ranked matches. ScopeAll
	// merges project and org results. Org scope without a configured org
	// identity returns an empty result, never an error.
	Search(ctx context.Context, q Query) ([]Result, error)
	// Count returns the number of stored entries for one scope.
	Count(ctx context.Context, scope Scope) (int, error)
	// PromoteToCore marks one existing entry (by id) as tier "core", subject
	// to CoreTierCap per (scope, org). Promoting an already-core entry is a
	// no-op. Returns ErrEntryNotFound if no entry with that id exists, or
	// ErrCoreTierFull if the bucket is already at CoreTierCap.
	PromoteToCore(ctx context.Context, id string) error
	// CoreEntries returns up to CoreTierCap "core"-tier entries for scope,
	// ordered by created DESC, title ASC, id ASC (the same tie-break Search
	// uses). Not a search - no query text, just the current core set. Used
	// to build the auto-injected system-prompt block (D1).
	CoreEntries(ctx context.Context, scope Scope) ([]Result, error)
	// Delete removes one entry (by id) from whichever store this Store has
	// access to (project, then org). Returns ErrEntryNotFound if no entry
	// with that id exists in any store. Refused on a read-only store.
	Delete(ctx context.Context, id string) error
	Close() error
}

// Config selects the backend and bounds.
type Config struct {
	// Backend is "memory" or "sqlite". Empty defaults to "sqlite".
	Backend string
	// ProjectPath is the project memory database file. Required for sqlite.
	// A repo owner may point it at a tracked path and commit memories with
	// the repository.
	ProjectPath string
	// OrgPath is the user-level org memory database file. Optional; without
	// it org scope is unavailable.
	OrgPath string
	// OrgID is the user-owned org identity. Empty means org scope is
	// unavailable. It must come from the user config, never from a workspace
	// file: a workspace config is repo-controlled and must not name the org
	// store its agents write into.
	OrgID string
	// MaxEntryBytes caps one rendered entry. Default 8192.
	MaxEntryBytes int
	// MaxEntries caps the row count per store file. Default 500.
	MaxEntries int
	// MaxSearchResults caps search results. Default 8.
	MaxSearchResults int
	// BlockPatterns are regexes; matching content is refused at save.
	BlockPatterns []string
	// ReadOnly opens the store without writing the database file: open skips
	// the journal_mode(WAL) switch, the schema CREATE, and the FTS5 rebuild
	// backfill; Close skips the WAL checkpoint; Save is refused. Search
	// works via the LIKE fallback with identical results; the file must
	// already exist with the schema. Zero value false = read-write.
	ReadOnly bool
	// HardenTempStore marks ProjectPath as an ad-hoc, OS-temp-dir-backed store
	// (config.TempStorePath) rather than an operator-managed project path.
	// When true, openSQLiteStore chmods the project database file to 0600 and
	// its parent directory to 0700, failing closed on error, the same way the
	// org store is always hardened: an ad-hoc temp-dir store has no project
	// directory whose permissions an operator manages, so mivia must protect
	// it itself.
	HardenTempStore bool
}

// Backend names.
const (
	BackendMemory = "memory"
	BackendSQLite = "sqlite"
)

// Store defaults.
const (
	DefaultMaxEntries       = 500
	DefaultMaxSearchResults = 8
)

// Open returns a Store for cfg.
func Open(cfg Config) (Store, error) {
	cfg = normalizeConfig(cfg)
	if cfg.OrgID != "" {
		norm, err := NormalizeOrgID(cfg.OrgID)
		if err != nil {
			return nil, fmt.Errorf("memory org_id: %w", err)
		}
		cfg.OrgID = norm
	}
	for _, pattern := range cfg.BlockPatterns {
		if _, err := regexp.Compile(pattern); err != nil {
			return nil, fmt.Errorf("memory block pattern %q: %w", pattern, err)
		}
	}
	switch cfg.Backend {
	case BackendMemory:
		return newMemStore(cfg), nil
	case BackendSQLite:
		return openSQLiteStore(cfg)
	default:
		return nil, fmt.Errorf("memory backend %q: must be \"memory\" or \"sqlite\"", cfg.Backend)
	}
}

func normalizeConfig(cfg Config) Config {
	cfg.Backend = strings.ToLower(strings.TrimSpace(cfg.Backend))
	if cfg.Backend == "" {
		cfg.Backend = BackendSQLite
	}
	if cfg.MaxEntryBytes <= 0 {
		cfg.MaxEntryBytes = DefaultMaxEntryBytes
	}
	if cfg.MaxEntries <= 0 {
		cfg.MaxEntries = DefaultMaxEntries
	}
	if cfg.MaxSearchResults <= 0 {
		cfg.MaxSearchResults = DefaultMaxSearchResults
	}
	return cfg
}

func (cfg Config) limits() Limits {
	return Limits{MaxEntryBytes: cfg.MaxEntryBytes, BlockPatterns: cfg.BlockPatterns}
}

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

func (s *sqliteStore) dbFor(scope Scope) (*sql.DB, string) {
	if scope == ScopeOrg {
		return s.orgDB, s.cfg.OrgID
	}
	return s.projectDB, ""
}

func (s *sqliteStore) Save(ctx context.Context, e Entry) (Result, error) {
	if s.cfg.ReadOnly {
		return Result{}, errors.New("memory store is read-only")
	}
	if err := e.Validate(s.cfg.limits()); err != nil {
		return Result{}, err
	}
	if e.Scope == ScopeOrg && s.cfg.OrgID == "" {
		return Result{}, errors.New("org scope is not configured: set [memory] org_id in the user config file")
	}
	if e.Created == "" {
		e.Created = time.Now().Format("2006-01-02")
	}
	db, org := s.dbFor(e.Scope)
	if db == nil {
		return Result{}, errors.New("org scope is not available (no org store on this machine)")
	}
	rendered := e.Render()
	// The content-addressed id includes the org identity, so an identical
	// entry saved under a different org id on a shared org.db file gets its
	// own namespace-distinct id and cannot collide with (or be dropped by) a
	// row another org already wrote.
	id := entryID(e.Scope, org, e.Title, rendered)
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback()
	// Idempotency first: an identical re-save at capacity returns the existing
	// result instead of a spurious "store is full" error (parity with memStore).
	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM memories WHERE id = ? AND scope = ? AND org = ?)", id, string(e.Scope), org).Scan(&exists); err != nil {
		return Result{}, err
	}
	if exists == 1 {
		if err := tx.Commit(); err != nil {
			return Result{}, err
		}
		return Result{ID: id, Scope: e.Scope, Org: org, Title: e.Title, Verdict: e.Verdict, Tags: append([]string(nil), e.Tags...), Created: e.Created, Snippet: e.Summary}, nil
	}
	if err := s.ensureCapacity(ctx, tx, e.Scope, org); err != nil {
		return Result{}, err
	}
	insertArgs := []any{id, string(e.Scope), org, e.Title, e.Summary, string(e.Verdict), strings.Join(e.Tags, ", "), e.Created, rendered}
	res, err := tx.ExecContext(ctx,
		"INSERT OR IGNORE INTO memories(id,scope,org,title,summary,verdict,tags,created,content) VALUES(?,?,?,?,?,?,?,?,?)",
		insertArgs...)
	if err != nil {
		return Result{}, err
	}
	// Keep the FTS index in sync inside the same transaction (see syncFTSRow).
	// The sync runs only when this insert actually created the row: an ignored
	// insert means a concurrent identical save already stored (and indexed)
	// the row, and inserting its rowid into the FTS index again would abort.
	if s.fts {
		if n, _ := res.RowsAffected(); n > 0 {
			if err := syncFTSRow(ctx, tx, id, e.Scope, org); err != nil {
				return Result{}, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return Result{}, err
	}
	// Checkpoint after every write so the main database file is always current:
	// the project DB is designed to be committed to the repo, and a stale main
	// file (with recent writes stranded in the gitignored -wal) would silently
	// ship a database missing the latest memories. The write itself is already
	// durable in WAL, so a checkpoint failure is not fatal for the save.
	s.checkpoint(ctx, db)
	return Result{ID: id, Scope: e.Scope, Org: org, Title: e.Title, Verdict: e.Verdict, Tags: append([]string(nil), e.Tags...), Created: e.Created, Snippet: e.Summary}, nil
}

// syncFTSRow indexes one saved row inside the Save transaction. It is called
// only when the memories INSERT OR IGNORE actually inserted a row (RowsAffected
// > 0), so the rowid is fresh and the index insert cannot collide with an
// existing entry; a concurrent identical save from another process owns the
// index row for its own insert. There is deliberately no NOT EXISTS guard over
// memories_fts: for an external content table a plain SELECT on memories_fts
// without MATCH is passed through to the content table, which would make the
// guard always false and silently skip the index insert. If the index is
// missing or broken the whole transaction rolls back: Save fails closed rather
// than silently storing a row the search index never sees.
func syncFTSRow(ctx context.Context, tx *sql.Tx, id string, scope Scope, org string) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO memories_fts(rowid, title, summary, content)
SELECT rowid, title, summary, content FROM memories
WHERE id = ? AND scope = ? AND org = ?`, id, string(scope), org); err != nil {
		return err
	}
	return nil
}

// checkpoint runs PRAGMA wal_checkpoint(TRUNCATE), folding committed WAL
// frames back into the main database file. Errors are returned for Close (a
// durability-relevant failure) and ignored for per-Save calls, where the write
// is already durable in WAL.
func (s *sqliteStore) checkpoint(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return nil
	}
	var busy, log, checkpointed int
	err := db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &checkpointed)
	if err != nil {
		return err
	}
	return nil
}

func (s *sqliteStore) Search(ctx context.Context, q Query) ([]Result, error) {
	text := strings.TrimSpace(q.Text)
	if text == "" {
		return nil, errors.New("query is required")
	}
	limit := s.searchLimit(q.MaxResults)
	switch q.Scope {
	case ScopeProject:
		return s.searchDB(ctx, s.projectDB, ScopeProject, "", text, limit)
	case ScopeOrg:
		if s.cfg.OrgID == "" {
			return nil, nil
		}
		return s.searchDB(ctx, s.orgDB, ScopeOrg, s.cfg.OrgID, text, limit)
	case ScopeAll:
		proj, err := s.searchDB(ctx, s.projectDB, ScopeProject, "", text, limit)
		if err != nil {
			return nil, err
		}
		var org []Result
		if s.cfg.OrgID != "" && s.orgDB != nil {
			org, err = s.searchDB(ctx, s.orgDB, ScopeOrg, s.cfg.OrgID, text, limit)
			if err != nil {
				return nil, err
			}
		}
		return mergeRanked(proj, org, parseQuery(text), text, limit), nil
	default:
		return nil, fmt.Errorf("scope must be project, org, or all, got %q", q.Scope)
	}
}

func (s *sqliteStore) searchLimit(requested int) int {
	if requested <= 0 || requested > s.cfg.MaxSearchResults {
		return s.cfg.MaxSearchResults
	}
	return requested
}

func (s *sqliteStore) searchDB(ctx context.Context, db *sql.DB, scope Scope, org, text string, limit int) ([]Result, error) {
	if db == nil {
		return nil, nil
	}
	p := parseQuery(text)
	if p.zeroToken {
		return s.searchLike(ctx, db, scope, org, text, p, limit)
	}
	return relaxSearch(p.tokens, func(tokens []string) ([]Result, error) {
		pp := p
		pp.tokens = tokens
		return s.searchLike(ctx, db, scope, org, text, pp, limit)
	})
}

func (s *sqliteStore) Count(ctx context.Context, scope Scope) (int, error) {
	db, org := s.dbFor(scope)
	if db == nil {
		return 0, nil
	}
	var n int
	err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM memories WHERE scope = ? AND org = ?", string(scope), org).Scan(&n)
	return n, err
}

func (s *sqliteStore) Close() error {
	var first error
	if s.projectDB != nil {
		if !s.cfg.ReadOnly {
			if err := s.checkpoint(context.Background(), s.projectDB); err != nil && first == nil {
				first = err
			}
		}
		if err := s.projectDB.Close(); err != nil && first == nil {
			first = err
		}
	}
	if s.orgDB != nil {
		if !s.cfg.ReadOnly {
			if err := s.checkpoint(context.Background(), s.orgDB); err != nil && first == nil {
				first = err
			}
		}
		if err := s.orgDB.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}
