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
	Save(ctx context.Context, e Entry) (Result, error)
	// Search returns up to the configured limit ranked matches. ScopeAll
	// merges project and org results. Org scope without a configured org
	// identity returns an empty result, never an error.
	Search(ctx context.Context, q Query) ([]Result, error)
	// Count returns the number of stored entries for one scope.
	Count(ctx context.Context, scope Scope) (int, error)
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
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_memories_scope_org ON memories(scope, org);
`

type sqliteStore struct {
	projectDB *sql.DB
	orgDB     *sql.DB // nil when OrgPath is empty
	cfg       Config
	mu        sync.Mutex // serializes Save across the two files (cap check + insert)
}

func openSQLiteStore(cfg Config) (*sqliteStore, error) {
	projectPath := strings.TrimSpace(cfg.ProjectPath)
	if projectPath == "" {
		return nil, errors.New("memory: project database path is required")
	}
	projectDB, err := openMemoryDB(projectPath)
	if err != nil {
		return nil, fmt.Errorf("memory project store %s: %w", projectPath, err)
	}
	s := &sqliteStore{projectDB: projectDB, cfg: cfg}
	if orgPath := strings.TrimSpace(cfg.OrgPath); orgPath != "" {
		orgDB, err := openMemoryDB(orgPath)
		if err != nil {
			projectDB.Close()
			return nil, fmt.Errorf("memory org store %s: %w", orgPath, err)
		}
		s.orgDB = orgDB
	}
	return s, nil
}

func openMemoryDB(path string) (*sql.DB, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	// sql.Open fails only on an unknown driver name; modernc.org/sqlite
	// registers "sqlite" in its init, so the error branch would be dead code
	// (diff-coverage gate).
	db, _ := sql.Open("sqlite", sqliteMemoryDSN(path))
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=FULL",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, err
		}
	}
	if _, err := db.Exec(memorySchema); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// sqliteMemoryDSN applies the same pragmas the storage package uses for its
// durable stores. Keep the two in sync when the set changes.
func sqliteMemoryDSN(path string) string {
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	return path + separator + "_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=busy_timeout(5000)"
}

func (s *sqliteStore) dbFor(scope Scope) (*sql.DB, string) {
	if scope == ScopeOrg {
		return s.orgDB, s.cfg.OrgID
	}
	return s.projectDB, ""
}

func (s *sqliteStore) Save(ctx context.Context, e Entry) (Result, error) {
	if err := e.Validate(s.cfg.limits()); err != nil {
		return Result{}, err
	}
	if e.Scope == ScopeOrg && s.cfg.OrgID == "" {
		return Result{}, errors.New("org scope is not configured: set [memory] org_id in the user config file")
	}
	if e.Created == "" {
		e.Created = time.Now().Format("2006-01-02")
	}
	rendered := e.Render()
	id := entryID(e.Scope, e.Title, rendered)
	db, org := s.dbFor(e.Scope)
	if db == nil {
		return Result{}, errors.New("org scope is not available (no org store on this machine)")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback()
	var count int
	where := "scope = ? AND org = ?"
	args := []any{string(e.Scope), org}
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM memories WHERE "+where, args...).Scan(&count); err != nil {
		return Result{}, err
	}
	if count >= s.cfg.MaxEntries {
		return Result{}, fmt.Errorf("memory store is full (max_entries=%d); consolidate or raise [memory] max_entries", s.cfg.MaxEntries)
	}
	insertArgs := []any{id, string(e.Scope), org, e.Title, e.Summary, string(e.Verdict), strings.Join(e.Tags, ", "), e.Created, rendered}
	_, err = tx.ExecContext(ctx,
		"INSERT OR IGNORE INTO memories(id,scope,org,title,summary,verdict,tags,created,content) VALUES(?,?,?,?,?,?,?,?,?)",
		insertArgs...)
	if err != nil {
		return Result{}, err
	}
	if err := tx.Commit(); err != nil {
		return Result{}, err
	}
	return Result{ID: id, Scope: e.Scope, Org: org, Title: e.Title, Verdict: e.Verdict, Tags: append([]string(nil), e.Tags...), Created: e.Created, Snippet: e.Summary}, nil
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
		return mergeRanked(proj, org, text, limit), nil
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

const searchSQL = `
SELECT id, scope, org, title, verdict, tags, created, summary
FROM memories
WHERE scope = ? AND org = ?
  AND (lower(title) = lower(?) OR lower(title) LIKE lower(?) ESCAPE '\' OR lower(summary) LIKE lower(?) ESCAPE '\' OR lower(content) LIKE lower(?) ESCAPE '\')
ORDER BY CASE
  WHEN lower(title) = lower(?) THEN 0
  WHEN lower(title) LIKE lower(?) ESCAPE '\' THEN 1
  WHEN lower(summary) LIKE lower(?) ESCAPE '\' THEN 2
  ELSE 3 END, created_at DESC, title ASC
LIMIT ?`

func (s *sqliteStore) searchDB(ctx context.Context, db *sql.DB, scope Scope, org, text string, limit int) ([]Result, error) {
	if db == nil {
		return nil, nil
	}
	escaped := escapeLike(text)
	contains := "%" + escaped + "%"
	args := []any{string(scope), org, text, contains, contains, contains, text, contains, contains, limit}
	rows, err := db.QueryContext(ctx, searchSQL, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Result
	for rows.Next() {
		var r Result
		var tags string
		if err := rows.Scan(&r.ID, &r.Scope, &r.Org, &r.Title, &r.Verdict, &tags, &r.Created, &r.Snippet); err != nil {
			return nil, err
		}
		r.Tags = splitTags(tags)
		out = append(out, r)
	}
	return out, rows.Err()
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
		if err := s.projectDB.Close(); err != nil {
			first = err
		}
	}
	if s.orgDB != nil {
		if err := s.orgDB.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}
