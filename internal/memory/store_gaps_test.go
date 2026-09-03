package memory

// TestGap* tests close the diff-coverage gaps in store.go: error and edge
// branches that the existing store_test.go does not exercise. They are kept in
// a separate file so other agents can work on entry.go/org.go tests in the
// same package without conflicts.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Open() error branches
// ---------------------------------------------------------------------------

func TestGapOpenRejectsInvalidOrgID(t *testing.T) {
	_, err := Open(Config{Backend: "memory", OrgID: "bad org id"})
	if err == nil || !strings.Contains(err.Error(), "org_id") {
		t.Fatalf("Open with an invalid org_id must fail, got %v", err)
	}
}

func TestGapOpenRequiresProjectPath(t *testing.T) {
	_, err := Open(Config{Backend: "sqlite"})
	if err == nil || !strings.Contains(err.Error(), "project database path is required") {
		t.Fatalf("Open without a project path must fail, got %v", err)
	}
}

func TestGapOpenProjectMkdirAllFailure(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// filepath.Dir(path) is blocker/sub; MkdirAll fails because blocker is a
	// regular file, which exercises both openMemoryDB's MkdirAll error and
	// openSQLiteStore's project-store wrapper.
	_, err := Open(Config{Backend: "sqlite", ProjectPath: filepath.Join(blocker, "sub", "project.db")})
	if err == nil || !strings.Contains(err.Error(), "project store") {
		t.Fatalf("Open with an uncreatable project dir must fail, got %v", err)
	}
}

func TestGapOpenOrgStoreFailure(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The project store opens fine; ensureHardenedDir's MkdirAll for the org
	// dir fails before openMemoryDB(orgPath, ...) ever runs, which exercises
	// the projectDB.Close() + org-dir wrapper branch.
	_, err := Open(Config{
		Backend:     "sqlite",
		ProjectPath: filepath.Join(dir, "project.db"),
		OrgPath:     filepath.Join(blocker, "org.db"),
		OrgID:       "github.com/acme",
	})
	if err == nil || !strings.Contains(err.Error(), "org dir") {
		t.Fatalf("Open with an uncreatable org dir must fail, got %v", err)
	}
}

func TestGapOpenProjectPragmaFailure(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "project.db")
	// A non-empty file that is not a SQLite database: sql.Open succeeds
	// lazily, so the first PRAGMA Exec fails.
	if err := os.WriteFile(bad, []byte("this is not a sqlite database"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Open(Config{Backend: "sqlite", ProjectPath: bad})
	if err == nil {
		t.Fatal("Open on an invalid database file must fail")
	}
}

func TestGapOpenProjectSchemaFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "project.db")
	// Pre-create a valid SQLite file whose memories table lacks the columns
	// the schema's CREATE INDEX references, so the schema Exec fails after
	// the pragmas succeed.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE memories (id TEXT PRIMARY KEY);"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(Config{Backend: "sqlite", ProjectPath: path}); err == nil {
		t.Fatal("Open with an incompatible memories table must fail")
	}
}

// TestGapSQLiteMemoryDSNQuestionMarkSeparator pins the DSN-shape contract for
// sqliteMemoryDSN, mirroring TestSQLiteDSNQuestionMarkUsesFileURI in
// internal/storage: plain paths keep the historical path+"?"+pragmas DSN
// byte-for-byte, and '?'-paths become a file: URI whose path portion contains
// no literal '?' and percent-decodes back to the input path. The contract
// matters because the modernc.org/sqlite driver (v1.54.0) splits a DSN at the
// first literal '?', so any literal '?' in the filename portion would silently
// open the wrong database file.
func TestGapSQLiteMemoryDSNQuestionMarkSeparator(t *testing.T) {
	t.Run("plain path keeps historical shape", func(t *testing.T) {
		for _, path := range []string{"dir/db.sqlite", "events.db", "/tmp/events.db", "dir/with space.db", "dir/with#hash.db", "dir/with%percent.db"} {
			got := sqliteMemoryDSN(path)
			want := path + "?" + pragmaMemoryDSNParams
			if got != want {
				t.Fatalf("sqliteMemoryDSN(%q) = %q, want %q", path, got, want)
			}
		}
	})
	t.Run("question mark path becomes a file URI", func(t *testing.T) {
		for _, path := range []string{"dir/db?.sqlite", "ledger?part.db", "/tmp/ctx?name.db", "a?b?c.db", "?leading.db"} {
			dsn := sqliteMemoryDSN(path)
			if !strings.HasPrefix(dsn, "file:") {
				t.Fatalf("sqliteMemoryDSN(%q) = %q, want a file: URI", path, dsn)
			}
			rest := strings.TrimPrefix(dsn, "file:")
			sep := strings.IndexByte(rest, '?')
			if sep < 0 {
				t.Fatalf("sqliteMemoryDSN(%q) = %q has no query separator", path, dsn)
			}
			encoded := rest[:sep]
			if strings.Contains(encoded, "?") {
				t.Fatalf("path portion of sqliteMemoryDSN(%q) = %q still contains a literal '?'", path, encoded)
			}
			decoded, err := url.PathUnescape(encoded)
			if err != nil {
				t.Fatalf("PathUnescape(%q): %v", encoded, err)
			}
			if decoded != path {
				t.Fatalf("PathUnescape(%q) = %q, want %q", encoded, decoded, path)
			}
			query := rest[sep+1:]
			if query != pragmaMemoryDSNParams {
				t.Fatalf("query of sqliteMemoryDSN(%q) = %q, want %q", path, query, pragmaMemoryDSNParams)
			}
		}
	})
}

// TestSQLiteMemoryQuestionMarkPathOpensLiteralFile is the end-to-end
// regression for the memory-store variant of the
// storage-dsn-question-mark-truncation bug: a store path containing a literal
// '?' must open as the literal file, never truncated at the '?'. Before the
// fix sqliteMemoryDSN returned ".../ctx?name.db&_pragma=..." and the
// modernc.org/sqlite v1.54.0 driver split the DSN at the first '?', so the
// store silently opened ".../ctx" - a different, wrong database file (silent
// data mislocation or cross-store mixing). '?' is not a legal Windows filename
// character, so the bug (and this regression) is POSIX-only.
func TestSQLiteMemoryQuestionMarkPathOpensLiteralFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("'?' is not a legal Windows filename character; the bug is POSIX-only")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "ctx?name.db")
	s, err := Open(Config{
		Backend:          BackendSQLite,
		ProjectPath:      path,
		MaxEntryBytes:    8192,
		MaxEntries:       5,
		MaxSearchResults: 8,
	})
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	defer s.Close()

	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %q: %v", path, err)
	}
	if !st.Mode().IsRegular() {
		t.Fatalf("%q mode = %v, want a regular file", path, st.Mode())
	}
	truncated := filepath.Join(dir, "ctx")
	if _, err := os.Stat(truncated); err == nil {
		t.Fatalf("truncated file %q exists: the store opened the wrong database file", truncated)
	}

	ctx := context.Background()
	if _, err := s.Save(ctx, testEntry("qpath", ScopeProject)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Search(ctx, Query{Text: "qpath", Scope: ScopeProject})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 || got[0].Title != "qpath" {
		t.Fatalf("Search = %+v, want the saved entry", got)
	}
}

// TestSQLiteSearchOrdersSameRankByCreatedDate is the regression for the
// f2-memory-search-ordering-parity defect: the SQLite backend ordered
// same-rank results by the insertion-time created_at column (schema default
// CURRENT_TIMESTAMP, second resolution), while the memory backend orders by
// the user-visible Created date (YYYY-MM-DD, day resolution). Two same-rank
// entries with reversed Created-vs-insertion order therefore came back in
// different orders on the two backends. The ORDER BY must use the created
// column so both backends agree: rank, Created DESC, title ASC. This fails
// before the fix (returns [beta, alpha]) and passes after.
func TestSQLiteSearchOrdersSameRankByCreatedDate(t *testing.T) {
	s := newTestStore(t, "sqlite", "")
	ctx := context.Background()
	// Reversed Created-vs-insertion order: alpha has the newer Created date
	// but is saved first (older insertion time).
	alpha := testEntry("alpha cache", ScopeProject)
	alpha.Created = "2026-01-05"
	beta := testEntry("beta cache", ScopeProject)
	beta.Created = "2026-01-01"
	if _, err := s.Save(ctx, alpha); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(ctx, beta); err != nil {
		t.Fatal(err)
	}
	// Force beta's insertion timestamp strictly later so the OLD ORDER BY
	// (created_at DESC) deterministically returns [beta, alpha]: without this
	// the second-resolution CURRENT_TIMESTAMP could tie the two rows and the
	// old title-ASC tie-break would mask the defect.
	st := s.(*sqliteStore)
	if _, err := st.projectDB.Exec("UPDATE memories SET created_at = '2999-01-01 00:00:00' WHERE title = ?", "beta cache"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Search(ctx, Query{Text: "cache", Scope: ScopeProject})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Title != "alpha cache" || got[1].Title != "beta cache" {
		t.Fatalf("same-rank search order = %+v, want [alpha cache, beta cache] (Created DESC)", got)
	}
}

// ---------------------------------------------------------------------------
// sqliteStore Save() error branches
// ---------------------------------------------------------------------------

// TestGapSaveOrgScopeUnavailable covers Save with a configured org identity
// but no org store file, plus the nil-org-db Search/Count branches.
func TestGapSaveOrgScopeUnavailable(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(Config{Backend: "sqlite", ProjectPath: filepath.Join(dir, "project.db"), OrgID: "github.com/acme"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if _, err := s.Save(ctx, testEntry("x", ScopeOrg)); err == nil || !strings.Contains(err.Error(), "no org store") {
		t.Fatalf("Save scope=org without an org store must fail, got %v", err)
	}
	got, err := s.Search(ctx, Query{Text: "x", Scope: ScopeOrg})
	if err != nil || got != nil {
		t.Fatalf("Search scope=org with a nil org db = %v, %v; want nil, nil", got, err)
	}
	n, err := s.Count(ctx, ScopeOrg)
	if err != nil || n != 0 {
		t.Fatalf("Count scope=org with a nil org db = %d, %v; want 0, nil", n, err)
	}
}

func TestGapSaveBeginTxError(t *testing.T) {
	s := newTestStore(t, "sqlite", "")
	st := s.(*sqliteStore)
	// Close the underlying DB first: BeginTx then fails with
	// "sql: database is closed".
	if err := st.projectDB.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(context.Background(), testEntry("x", ScopeProject)); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("Save on a closed database must fail, got %v", err)
	}
}

func TestGapSaveCountQueryError(t *testing.T) {
	s := newTestStore(t, "sqlite", "")
	st := s.(*sqliteStore)
	// BeginTx succeeds but the COUNT query fails against the missing table.
	if _, err := st.projectDB.Exec("DROP TABLE memories"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(context.Background(), testEntry("x", ScopeProject)); err == nil {
		t.Fatal("Save with a dropped memories table must fail")
	}
}

func TestGapSaveInsertError(t *testing.T) {
	s := newTestStore(t, "sqlite", "")
	st := s.(*sqliteStore)
	// The COUNT query still works, but the INSERT references the dropped
	// content column and fails.
	if _, err := st.projectDB.Exec("ALTER TABLE memories DROP COLUMN content"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(context.Background(), testEntry("x", ScopeProject)); err == nil {
		t.Fatal("Save with a missing content column must fail")
	}
}

var (
	errGapCommit     = errors.New("gap: commit failed")
	errGapClose      = errors.New("gap: db close failed")
	errGapCount      = errors.New("gap: count query failed")
	errGapCheckpoint = errors.New("gap: checkpoint failed")
	errGapChmod      = errors.New("gap: chmod failed")
)

// gapSQLConnector is a minimal database/sql connector that lets a test force
// transaction-commit failures (commitErr), query failures (countErr,
// checkpointErr), and DB.Close failures (closeErr). existsFalse makes the
// idempotency probe report "not present" so Save proceeds to the capacity
// COUNT query.
type gapSQLConnector struct {
	commitErr     error
	countErr      error
	checkpointErr error
	closeErr      error
	existsFalse   bool
}

func (c gapSQLConnector) Connect(context.Context) (driver.Conn, error) {
	return gapSQLConn{commitErr: c.commitErr, countErr: c.countErr, checkpointErr: c.checkpointErr, existsFalse: c.existsFalse}, nil
}

func (c gapSQLConnector) Driver() driver.Driver { return gapSQLDriver{} }

// Close lets DB.Close surface a forced error: database/sql calls the
// connector's Close when it implements io.Closer.
func (c gapSQLConnector) Close() error { return c.closeErr }

type gapSQLDriver struct{}

func (gapSQLDriver) Open(string) (driver.Conn, error) { return gapSQLConn{}, nil }

type gapSQLConn struct {
	commitErr     error
	countErr      error
	checkpointErr error
	existsFalse   bool
}

func (gapSQLConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("gap: Prepare not supported")
}

func (gapSQLConn) Close() error { return nil }

func (c gapSQLConn) Begin() (driver.Tx, error) { return gapSQLTx{commitErr: c.commitErr}, nil }

func (c gapSQLConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	switch {
	case c.checkpointErr != nil && strings.Contains(query, "wal_checkpoint"):
		return nil, c.checkpointErr
	case c.countErr != nil && strings.Contains(query, "COUNT"):
		return nil, c.countErr
	case strings.Contains(query, "wal_checkpoint"):
		// The checkpoint pragma returns three columns (busy, log, checkpointed).
		return &gapSQLCountRows{cols: []string{"busy", "log", "checkpointed"}}, nil
	case strings.Contains(query, "EXISTS"):
		// The idempotency probe reports 1 when the entry already exists.
		value := int64(1)
		if c.existsFalse {
			value = 0
		}
		return &gapSQLCountRows{cols: []string{"exists"}, value: value}, nil
	default:
		return &gapSQLCountRows{cols: []string{"COUNT(*)"}}, nil
	}
}

func (c gapSQLConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(1), nil
}

type gapSQLTx struct{ commitErr error }

func (t gapSQLTx) Commit() error   { return t.commitErr }
func (t gapSQLTx) Rollback() error { return nil }

type gapSQLCountRows struct {
	done  bool
	cols  []string
	value int64
}

func (r *gapSQLCountRows) Columns() []string { return r.cols }
func (r *gapSQLCountRows) Close() error      { return nil }
func (r *gapSQLCountRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	// Fill every destination: Scan refuses NULL-to-int conversion, and the
	// checkpoint pragma row has three integer columns.
	for i := range dest {
		dest[i] = r.value
	}
	return nil
}

func TestGapSaveCommitError(t *testing.T) {
	// A fake driver whose transactions always fail at Commit exercises
	// Save's tx.Commit() error branch. existsFalse routes past the
	// idempotent-hit branch so the capacity path's INSERT commit fails.
	db := sql.OpenDB(gapSQLConnector{commitErr: errGapCommit, existsFalse: true})
	s := &sqliteStore{
		projectDB: db,
		cfg:       Config{Backend: "sqlite", MaxEntryBytes: 8192, MaxEntries: 5, MaxSearchResults: 8},
	}
	_, err := s.Save(context.Background(), testEntry("commit-fail", ScopeProject))
	if !errors.Is(err, errGapCommit) {
		t.Fatalf("Save = %v, want %v", err, errGapCommit)
	}
}

// ---------------------------------------------------------------------------
// sqliteStore Search()/Count() error branches
// ---------------------------------------------------------------------------

func TestGapSearchScopeAllProjectDBError(t *testing.T) {
	s := newTestStore(t, "sqlite", "github.com/acme")
	st := s.(*sqliteStore)
	if err := st.projectDB.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Search(context.Background(), Query{Text: "x", Scope: ScopeAll}); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("ScopeAll search with a closed project db must fail, got %v", err)
	}
}

func TestGapSearchScopeAllOrgDBError(t *testing.T) {
	s := newTestStore(t, "sqlite", "github.com/acme")
	st := s.(*sqliteStore)
	// Project search succeeds (empty), org search hits the closed db.
	if err := st.orgDB.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Search(context.Background(), Query{Text: "x", Scope: ScopeAll}); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("ScopeAll search with a closed org db must fail, got %v", err)
	}
}

func TestGapSearchInvalidScope(t *testing.T) {
	for _, backend := range []string{"sqlite", "memory"} {
		t.Run(backend, func(t *testing.T) {
			s := newTestStore(t, backend, "")
			_, err := s.Search(context.Background(), Query{Text: "x", Scope: "bogus"})
			if err == nil || !strings.Contains(err.Error(), "scope") {
				t.Fatalf("Search with an invalid scope must fail, got %v", err)
			}
		})
	}
}

func TestGapSearchScanError(t *testing.T) {
	s := newTestStore(t, "sqlite", "")
	st := s.(*sqliteStore)
	// Force the LIKE path: this test recreates the memories table directly via
	// SQL, so its row is intentionally absent from the FTS index (which is kept
	// in sync only by Save). The rows.Scan error branch is exercised by the
	// LIKE path scanning the recreated table directly. No behavioral pin
	// changes: ftsEnabled=false is exactly the transparent degradation path.
	st.fts = false
	// Recreate memories with a NULLable tags column and insert a NULL there:
	// the row is returned by the search but Scan into the tags string field
	// fails, exercising searchDB's rows.Scan error branch.
	if _, err := st.projectDB.Exec("DROP TABLE memories"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.projectDB.Exec(`CREATE TABLE memories (
  id TEXT PRIMARY KEY,
  scope TEXT NOT NULL,
  org TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL,
  summary TEXT NOT NULL,
  verdict TEXT NOT NULL DEFAULT 'neutral',
  tags TEXT,
  created TEXT NOT NULL DEFAULT '',
  content TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.projectDB.Exec(`INSERT INTO memories (id, scope, org, title, summary, verdict, tags, created, content)
VALUES ('1', 'project', '', 'scanfail', 'summary x', 'good', NULL, '2026-01-01', 'content x')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Search(context.Background(), Query{Text: "scanfail", Scope: ScopeProject}); err == nil {
		t.Fatal("Search over a row with a NULL tags column must fail at Scan")
	}
}

// ---------------------------------------------------------------------------
// sqliteStore Close() error branches
// ---------------------------------------------------------------------------

func TestGapStoreClosePropagatesProjectDBError(t *testing.T) {
	db := sql.OpenDB(gapSQLConnector{closeErr: errGapClose})
	s := &sqliteStore{projectDB: db, cfg: Config{Backend: "sqlite"}}
	if err := s.Close(); !errors.Is(err, errGapClose) {
		t.Fatalf("Close = %v, want %v", err, errGapClose)
	}
}

func TestGapStoreClosePropagatesOrgDBError(t *testing.T) {
	projectDB := sql.OpenDB(gapSQLConnector{}) // connector.Close returns nil
	orgDB := sql.OpenDB(gapSQLConnector{closeErr: errGapClose})
	s := &sqliteStore{projectDB: projectDB, orgDB: orgDB, cfg: Config{Backend: "sqlite"}}
	if err := s.Close(); !errors.Is(err, errGapClose) {
		t.Fatalf("Close = %v, want %v", err, errGapClose)
	}
}

func TestGapStoreCloseCheckpointError(t *testing.T) {
	// Close runs wal_checkpoint(TRUNCATE) before closing; a checkpoint query
	// failure must surface (durability-relevant on the committed DB).
	db := sql.OpenDB(gapSQLConnector{checkpointErr: errGapCheckpoint})
	s := &sqliteStore{projectDB: db, cfg: Config{Backend: "sqlite"}}
	if err := s.Close(); !errors.Is(err, errGapCheckpoint) {
		t.Fatalf("Close = %v, want checkpoint error %v", err, errGapCheckpoint)
	}
}

func TestGapStoreSaveIdempotentCommitError(t *testing.T) {
	// The idempotent-hit branch commits a read-only transaction; a commit
	// failure there must surface.
	db := sql.OpenDB(gapSQLConnector{commitErr: errGapCommit})
	s := &sqliteStore{projectDB: db, cfg: Config{Backend: "sqlite", MaxEntries: 5}}
	_, err := s.Save(context.Background(), testEntry("dup", ScopeProject))
	if !errors.Is(err, errGapCommit) {
		t.Fatalf("Save = %v, want commit error %v", err, errGapCommit)
	}
}

func TestGapStoreSaveCountScanError(t *testing.T) {
	// The capacity COUNT query failing must surface from Save.
	db := sql.OpenDB(gapSQLConnector{countErr: errGapCount, existsFalse: true})
	s := &sqliteStore{projectDB: db, cfg: Config{Backend: "sqlite", MaxEntries: 5}}
	_, err := s.Save(context.Background(), testEntry("countfail", ScopeProject))
	if !errors.Is(err, errGapCount) {
		t.Fatalf("Save = %v, want count error %v", err, errGapCount)
	}
}

// ---------------------------------------------------------------------------
// memStore error and edge branches
// ---------------------------------------------------------------------------

func TestGapMemStoreCountInvalidScope(t *testing.T) {
	s := newTestStore(t, "memory", "")
	_, err := s.Count(context.Background(), "bogus")
	if err == nil || !strings.Contains(err.Error(), "scope") {
		t.Fatalf("Count with an invalid scope must fail, got %v", err)
	}
}

func TestGapMemStoreMatchRowsRankAndDateTieBreaks(t *testing.T) {
	s := newTestStore(t, "memory", "")
	ctx := context.Background()
	// Different ranks: an exact title match sorts before a contains match.
	if _, err := s.Save(ctx, testEntry("widgets", ScopeProject)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(ctx, testEntry("widgets guide", ScopeProject)); err != nil {
		t.Fatal(err)
	}
	got, err := s.Search(ctx, Query{Text: "widgets", Scope: ScopeProject})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Title != "widgets" || got[1].Title != "widgets guide" {
		t.Fatalf("rank tie-break wrong: %+v", got)
	}
	// Same rank, different created dates: newer sorts first.
	older := testEntry("cache one", ScopeProject)
	older.Created = "2026-01-01"
	newer := testEntry("cache two", ScopeProject)
	newer.Created = "2026-01-02"
	if _, err := s.Save(ctx, older); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(ctx, newer); err != nil {
		t.Fatal(err)
	}
	got, err = s.Search(ctx, Query{Text: "cache", Scope: ScopeProject})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Title != "cache two" || got[1].Title != "cache one" {
		t.Fatalf("created-date tie-break wrong: %+v", got)
	}
}

func TestGapMemStoreRankMatchBodyOnly(t *testing.T) {
	s := newTestStore(t, "memory", "")
	e := testEntry("alpha", ScopeProject)
	e.Summary = "beta"
	e.Good = "gamma detail"
	e.Bad = "none"
	e.Why = "because gamma"
	if _, err := s.Save(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	// "gamma" matches only in the body fields, so rankMatch must return 3.
	got, err := s.Search(context.Background(), Query{Text: "gamma", Scope: ScopeProject})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Title != "alpha" {
		t.Fatalf("body-only match wrong: %+v", got)
	}
}

func TestGapMemStoreMergeRankedFallbackTieBreaksAndLimit(t *testing.T) {
	s := newTestStore(t, "memory", "github.com/acme")
	ctx := context.Background()
	// Project row matching only in body content.
	bodyOnly := testEntry("alpha", ScopeProject)
	bodyOnly.Summary = "beta"
	bodyOnly.Good = "gamma detail"
	bodyOnly.Bad = "none"
	bodyOnly.Why = "because gamma"
	if _, err := s.Save(ctx, bodyOnly); err != nil {
		t.Fatal(err)
	}
	// Org rows: two exact-title matches with different created dates plus one
	// contains match.
	exactOld := testEntry("gamma", ScopeOrg)
	exactOld.Created = "2026-01-01"
	exactNew := testEntry("gamma", ScopeOrg)
	exactNew.Created = "2026-01-03"
	contains := testEntry("gamma notes", ScopeOrg)
	contains.Created = "2026-01-02"
	for _, e := range []Entry{exactOld, exactNew, contains} {
		if _, err := s.Save(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Search(ctx, Query{Text: "gamma", Scope: ScopeAll})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"gamma", "gamma", "gamma notes", "alpha"}
	if len(got) != len(want) {
		t.Fatalf("ScopeAll search = %d results, want %d: %+v", len(got), len(want), got)
	}
	for i, title := range want {
		if got[i].Title != title {
			t.Errorf("result %d = %q, want %q (full: %+v)", i, got[i].Title, title, got)
		}
	}
	// A small MaxResults forces mergeRanked to truncate its combined list.
	got, err = s.Search(ctx, Query{Text: "gamma", Scope: ScopeAll, MaxResults: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Title != "gamma" {
		t.Fatalf("limited ScopeAll search = %+v, want 2 results with the exact match first", got)
	}
}
