package memory

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

var errGapFTS = errors.New("gap: fts index failed")

// ---------------------------------------------------------------------------
// Availability probe and index setup
// ---------------------------------------------------------------------------

func TestHasFTS5Option(t *testing.T) {
	cases := []struct {
		options []string
		want    bool
	}{
		{[]string{"ENABLE_FTS5"}, true},
		{[]string{"THREADSAFE=1", "ENABLE_FTS5"}, true},
		{[]string{"THREADSAFE=1"}, false},
		{nil, false},
	}
	for _, tc := range cases {
		if got := hasFTS5Option(tc.options); got != tc.want {
			t.Errorf("hasFTS5Option(%v) = %v, want %v", tc.options, got, tc.want)
		}
	}
}

func TestProbeFTS5OnRealStore(t *testing.T) {
	s := newTestStore(t, "sqlite", "")
	st := s.(*sqliteStore)
	if !probeFTS5(st.projectDB) {
		t.Fatal("the pinned modernc.org/sqlite build must report ENABLE_FTS5")
	}
}

func TestGapFTSProbeClosedDB(t *testing.T) {
	s := newTestStore(t, "sqlite", "")
	st := s.(*sqliteStore)
	if err := st.projectDB.Close(); err != nil {
		t.Fatal(err)
	}
	if probeFTS5(st.projectDB) {
		t.Fatal("probeFTS5 on a closed database must report unavailable (fail closed)")
	}
}

func TestGapFTSProbeScanError(t *testing.T) {
	// The gapSQLConnector's default row yields an int64 where probeFTS5 scans
	// a string: the scan fails and the probe reports unavailable.
	db := sql.OpenDB(gapSQLConnector{})
	if probeFTS5(db) {
		t.Fatal("probeFTS5 must report unavailable when scanning a compile-option row fails")
	}
}

func TestFTSIndexExistsAfterOpen(t *testing.T) {
	s := newTestStore(t, "sqlite", "")
	st := s.(*sqliteStore)
	var name string
	if err := st.projectDB.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='memories_fts'").Scan(&name); err != nil || name != "memories_fts" {
		t.Fatalf("memories_fts missing after Open: %q, %v", name, err)
	}
}

func TestFTSBackfillIdempotentAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "project.db")
	open := func() *sqliteStore {
		t.Helper()
		s, err := Open(Config{Backend: "sqlite", ProjectPath: path, MaxEntryBytes: 8192, MaxEntries: 50, MaxSearchResults: 8})
		if err != nil {
			t.Fatal(err)
		}
		return s.(*sqliteStore)
	}
	st := open()
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := st.Save(ctx, testEntry(fmt.Sprintf("backfill-%d", i), ScopeProject)); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st = open()
	if ftsRowCount(t, st.projectDB) != 3 {
		t.Fatalf("after reopen: fts rows = %d, want 3", ftsRowCount(t, st.projectDB))
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st = open()
	defer st.Close()
	if ftsRowCount(t, st.projectDB) != 3 {
		t.Fatalf("after second reopen: fts rows = %d, want 3 (backfill must stay idempotent)", ftsRowCount(t, st.projectDB))
	}
}

func TestFTSLegacyDBMigration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "project.db")
	// Pre-create a database with only the old memories schema and one row.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(memorySchema); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO memories(id,scope,org,title,summary,verdict,tags,created,content)
VALUES('legacy-1','project','','legacy title','legacy summary','good','','2026-01-01','legacy rendered')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(Config{Backend: "sqlite", ProjectPath: path, MaxEntryBytes: 8192, MaxEntries: 50, MaxSearchResults: 8})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	st := s.(*sqliteStore)
	if ftsRowCount(t, st.projectDB) != 1 {
		t.Fatalf("legacy row not backfilled: fts rows = %d, want 1", ftsRowCount(t, st.projectDB))
	}
	got, err := s.Search(context.Background(), Query{Text: "legacy", Scope: ScopeProject})
	if err != nil || len(got) != 1 || got[0].Title != "legacy title" {
		t.Fatalf("legacy row searchable via FTS: %+v, %v", got, err)
	}
}

func TestFTSSaveSyncsInSameTransaction(t *testing.T) {
	s := newTestStore(t, "sqlite", "")
	st := s.(*sqliteStore)
	ctx := context.Background()
	if _, err := s.Save(ctx, testEntry("sync", ScopeProject)); err != nil {
		t.Fatal(err)
	}
	if ftsRowCount(t, st.projectDB) != 1 {
		t.Fatalf("Save must index the row in the same transaction, fts rows = %d", ftsRowCount(t, st.projectDB))
	}
	// Idempotent re-save adds no FTS rows.
	if _, err := s.Save(ctx, testEntry("sync", ScopeProject)); err != nil {
		t.Fatal(err)
	}
	if ftsRowCount(t, st.projectDB) != 1 {
		t.Fatalf("idempotent re-save must not duplicate the index row, fts rows = %d", ftsRowCount(t, st.projectDB))
	}
	var mem int
	if err := st.projectDB.QueryRow("SELECT COUNT(*) FROM memories").Scan(&mem); err != nil {
		t.Fatal(err)
	}
	if mem != 1 {
		t.Fatalf("memories rows = %d, want 1", mem)
	}
}

func TestFTSPhraseRequiresContiguousOrder(t *testing.T) {
	s := newTestStore(t, "sqlite", "")
	st := s.(*sqliteStore)
	ctx := context.Background()
	if _, err := s.Save(ctx, testEntry("v4 flash guide", ScopeProject)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(ctx, testEntry("flash v4 separate", ScopeProject)); err != nil {
		t.Fatal(err)
	}
	rows, err := st.projectDB.Query("SELECT title FROM memories_fts WHERE memories_fts MATCH ? ORDER BY title", `"v4 flash"`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var titles []string
	for rows.Next() {
		var title string
		if err := rows.Scan(&title); err != nil {
			t.Fatal(err)
		}
		titles = append(titles, title)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !equalStrings(titles, []string{"v4 flash guide"}) {
		t.Errorf("phrase match = %v, want only the contiguous-order row", titles)
	}
	var n int
	if err := st.projectDB.QueryRow("SELECT COUNT(*) FROM memories_fts WHERE memories_fts MATCH ?", "fla*").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("prefix tok* matched %d rows, want 2", n)
	}
}

func TestFTSDisabledDegradationIdenticalResults(t *testing.T) {
	s := newTestStore(t, "sqlite", "")
	st := s.(*sqliteStore)
	ctx := context.Background()
	for _, title := range []string{
		"DeepSeek v4-flash: transient HTTP 400 escalation",
		"cache invalidation is hard",
		"v4 flash guide",
		"the cache key",
	} {
		if _, err := s.Save(ctx, testEntry(title, ScopeProject)); err != nil {
			t.Fatal(err)
		}
	}
	queries := []string{"DeepSeek 400", "cache", "v4 flash", "-", "good", "project", "DeepSeek v4-flash HTTP 400", "the of"}
	run := func() []string {
		var out []string
		for _, q := range queries {
			got, err := s.Search(ctx, Query{Text: q, Scope: ScopeProject})
			if err != nil {
				t.Fatalf("search %q: %v", q, err)
			}
			titles := make([]string, len(got))
			for i, r := range got {
				titles[i] = r.Title
			}
			out = append(out, fmt.Sprintf("%q:%v", q, titles))
		}
		return out
	}
	enabled := run()
	st.fts = false
	disabled := run()
	if !equalStrings(enabled, disabled) {
		t.Errorf("FTS-enabled and FTS-disabled results differ for the parity surface:\nenabled:  %v\ndisabled: %v", enabled, disabled)
	}
}

func TestFTSMissingIndexFailsClosed(t *testing.T) {
	s := newTestStore(t, "sqlite", "")
	st := s.(*sqliteStore)
	ctx := context.Background()
	if _, err := s.Save(ctx, testEntry("cache one", ScopeProject)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(ctx, testEntry("cache two", ScopeProject)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.projectDB.Exec("DROP TABLE memories_fts"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Search(ctx, Query{Text: "cache", Scope: ScopeProject})
	if err != nil {
		t.Fatalf("search with a missing index must fall back to LIKE, not error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("search with a missing index = %d results, want 2", len(got))
	}
}

// TestFTSSaveFailsClosedOnBrokenIndex pins that Save with a missing index
// fails closed and rolls back the whole transaction: a row must never be
// stored without its FTS index entry.
func TestFTSSaveFailsClosedOnBrokenIndex(t *testing.T) {
	s := newTestStore(t, "sqlite", "")
	st := s.(*sqliteStore)
	if _, err := st.projectDB.Exec("DROP TABLE memories_fts"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(context.Background(), testEntry("broken-index", ScopeProject)); err == nil {
		t.Fatal("Save with a missing FTS index must fail closed")
	}
	var n int
	if err := st.projectDB.QueryRow("SELECT COUNT(*) FROM memories").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("memories rows = %d, want 0 (Save must roll back the whole transaction)", n)
	}
}

func ftsRowCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM memories_fts").Scan(&n); err != nil {
		t.Fatalf("count memories_fts: %v", err)
	}
	return n
}

// ---------------------------------------------------------------------------
// ensureFTSIndex failure branches (fake driver)
// ---------------------------------------------------------------------------

// ftsProbeConnector is a fake driver whose compile_options query reports
// ENABLE_FTS5 and whose ExecContext fails when the query contains execErrTerm,
// exercising the schema-creation and backfill error branches of
// ensureFTSIndex.
type ftsProbeConnector struct {
	execErr     error
	execErrTerm string
}

func (c ftsProbeConnector) Connect(context.Context) (driver.Conn, error) {
	return ftsProbeConn{execErr: c.execErr, execErrTerm: c.execErrTerm}, nil
}

func (c ftsProbeConnector) Driver() driver.Driver { return gapSQLDriver{} }

type ftsProbeConn struct {
	execErr     error
	execErrTerm string
}

func (ftsProbeConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("gap: Prepare not supported")
}
func (ftsProbeConn) Close() error              { return nil }
func (ftsProbeConn) Begin() (driver.Tx, error) { return gapSQLTx{}, nil }

func (c ftsProbeConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, "compile_options") {
		return &ftsProbeRows{value: "ENABLE_FTS5"}, nil
	}
	return &gapSQLCountRows{cols: []string{"COUNT(*)"}}, nil
}

func (c ftsProbeConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if c.execErr != nil && strings.Contains(query, c.execErrTerm) {
		return nil, c.execErr
	}
	return driver.RowsAffected(1), nil
}

// ftsProbeRows is a one-row driver.Rows returning a single compile option.
type ftsProbeRows struct {
	value string
	done  bool
}

func (r *ftsProbeRows) Columns() []string { return []string{"compile_option"} }
func (r *ftsProbeRows) Close() error      { return nil }
func (r *ftsProbeRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	dest[0] = r.value
	return nil
}

func TestGapEnsureFTSIndexOK(t *testing.T) {
	db := sql.OpenDB(ftsProbeConnector{})
	if !ensureFTSIndex(db) {
		t.Fatal("ensureFTSIndex must report usable when the probe, schema, and backfill succeed")
	}
}

func TestGapEnsureFTSIndexSchemaFailure(t *testing.T) {
	db := sql.OpenDB(ftsProbeConnector{execErr: errGapFTS, execErrTerm: "USING fts5"})
	if ensureFTSIndex(db) {
		t.Fatal("ensureFTSIndex must report unavailable when the FTS schema creation fails")
	}
}

func TestGapEnsureFTSIndexBackfillFailure(t *testing.T) {
	db := sql.OpenDB(ftsProbeConnector{execErr: errGapFTS, execErrTerm: "INSERT INTO memories_fts"})
	if ensureFTSIndex(db) {
		t.Fatal("ensureFTSIndex must report unavailable when the backfill fails")
	}
}

// ---------------------------------------------------------------------------
// Clean-query eligibility and MATCH building
// ---------------------------------------------------------------------------

func TestParseFTSQueryEligibility(t *testing.T) {
	cases := []struct {
		query string
		ok    bool
		kinds []ftsPartKind
	}{
		{"cache", true, []ftsPartKind{ftsPartToken}},
		{"DeepSeek 400", true, []ftsPartKind{ftsPartToken, ftsPartToken}},
		{"tok*", true, []ftsPartKind{ftsPartPrefix}},
		{"cache*", true, []ftsPartKind{ftsPartPrefix}},
		{`"exact phrase"`, true, []ftsPartKind{ftsPartPhrase}},
		{`say ""hi""`, true, []ftsPartKind{ftsPartPhrase}},
		{`"exact phrase" more`, true, []ftsPartKind{ftsPartPhrase, ftsPartToken}},
		{"the cache", true, []ftsPartKind{ftsPartToken, ftsPartToken}},
		// Not clean: must fall back to the LIKE path.
		{"v4-flash", false, nil}, // hyphen is not a clean token char
		{"and", false, nil},      // reserved operator bareword
		{"AND", false, nil},      // case-insensitive
		{"not", false, nil},
		{"near", false, nil},
		{"or", false, nil},
		{"col:filter", false, nil},               // column-filter colon
		{"café", false, nil},                     // non-ASCII
		{"cache*extra", false, nil},              // stray asterisk not at the end
		{`"unbalanced`, false, nil},              // unbalanced quote
		{`""`, false, nil},                       // empty phrase
		{`"exact phrase"junk`, false, nil},       // quote followed by a non-space
		{"internal/memory/store.go", false, nil}, // file path
		{"a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6", false, nil}, // 65+ chars
	}
	for _, tc := range cases {
		parts, ok := parseFTSQuery(tc.query)
		if ok != tc.ok {
			t.Errorf("parseFTSQuery(%q) ok = %v, want %v", tc.query, ok, tc.ok)
			continue
		}
		if !ok {
			continue
		}
		if len(parts) != len(tc.kinds) {
			t.Errorf("parseFTSQuery(%q) = %d parts, want %d", tc.query, len(parts), len(tc.kinds))
			continue
		}
		for i, want := range tc.kinds {
			if parts[i].kind != want {
				t.Errorf("parseFTSQuery(%q) part %d kind = %v, want %v", tc.query, i, parts[i].kind, want)
			}
		}
	}
}

func TestBuildFTSMatch(t *testing.T) {
	cases := []struct {
		parts []ftsPart
		want  string
	}{
		{[]ftsPart{{kind: ftsPartToken, text: "cache"}}, "cache"},
		{[]ftsPart{{kind: ftsPartToken, text: "the"}, {kind: ftsPartToken, text: "cache"}}, "cache"}, // stopwords dropped
		{[]ftsPart{{kind: ftsPartToken, text: "the"}}, ""},                                           // all stopwords -> empty (caller falls back)
		{[]ftsPart{{kind: ftsPartPrefix, text: "tok*"}}, "tok*"},
		{[]ftsPart{{kind: ftsPartPhrase, text: "exact phrase"}}, `"exact phrase"`},
		{[]ftsPart{{kind: ftsPartToken, text: "cache"}, {kind: ftsPartPhrase, text: "exact phrase"}}, `cache "exact phrase"`},
		{[]ftsPart{{kind: ftsPartPhrase, text: "the of"}}, `"the of"`}, // phrases keep stopwords verbatim
	}
	for _, tc := range cases {
		if got := buildFTSMatch(tc.parts); got != tc.want {
			t.Errorf("buildFTSMatch(%+v) = %q, want %q", tc.parts, got, tc.want)
		}
	}
}

func TestFTSMatchFromParsed(t *testing.T) {
	cases := []struct {
		p    parsedQuery
		want string
	}{
		{parsedQuery{tokens: []string{"v4", "flash"}}, "v4 flash"},
		{parsedQuery{tokens: []string{"deepseek"}, phrases: []string{"http 400"}}, `deepseek "http 400"`},
		{parsedQuery{phrases: []string{`say "hi"`}}, `"say ""hi"""`}, // interior quotes re-doubled
	}
	for _, tc := range cases {
		if got := ftsMatchFromParsed(tc.p); got != tc.want {
			t.Errorf("ftsMatchFromParsed(%+v) = %q, want %q", tc.p, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// M-1: FTS MATCH fast path must never diverge from the LIKE path
// ---------------------------------------------------------------------------

// TestFTSGateAllPlainTokens unit-tests the ftsPartsAllPlainTokens gate that
// routes prefix and phrase queries away from the FTS5 MATCH path: only a
// non-empty list of plain bare tokens may take the MATCH path; a prefix part
// ('run*'), a phrase part ('"cache invalidation"'), a mixed list, or an empty
// list must be false.
func TestFTSGateAllPlainTokens(t *testing.T) {
	cases := []struct {
		name  string
		parts []ftsPart
		want  bool
	}{
		{"empty", nil, false},
		{"single plain token", []ftsPart{{kind: ftsPartToken, text: "cache"}}, true},
		{"all plain tokens", []ftsPart{{kind: ftsPartToken, text: "cache"}, {kind: ftsPartToken, text: "key"}}, true},
		{"prefix", []ftsPart{{kind: ftsPartPrefix, text: "run*"}}, false},
		{"phrase", []ftsPart{{kind: ftsPartPhrase, text: "cache invalidation"}}, false},
		{"token then phrase", []ftsPart{{kind: ftsPartToken, text: "more"}, {kind: ftsPartPhrase, text: "exact phrase"}}, false},
		{"phrase then token", []ftsPart{{kind: ftsPartPhrase, text: "exact phrase"}, {kind: ftsPartToken, text: "more"}}, false},
		{"token then prefix", []ftsPart{{kind: ftsPartToken, text: "cache"}, {kind: ftsPartPrefix, text: "run*"}}, false},
	}
	for _, tc := range cases {
		if got := ftsPartsAllPlainTokens(tc.parts); got != tc.want {
			t.Errorf("%s: ftsPartsAllPlainTokens(%+v) = %v, want %v", tc.name, tc.parts, got, tc.want)
		}
	}
}

// parityStore opens a store for the M-1 backend-parity tests: the sqlite and
// in-memory backends share this config so the three paths answer identical
// queries over identical corpora.
func parityStore(t *testing.T, backend string) Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(Config{
		Backend:          backend,
		ProjectPath:      filepath.Join(dir, "project.db"),
		OrgPath:          filepath.Join(dir, "org.db"),
		MaxEntryBytes:    8192,
		MaxEntries:       10,
		MaxSearchResults: 8,
	})
	if err != nil {
		t.Fatalf("Open(%s): %v", backend, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// saveParityFixtures writes the M-1 parity corpus into s, one project-scoped
// entry per title.
func saveParityFixtures(ctx context.Context, t *testing.T, s Store, fixtures []string) {
	t.Helper()
	for _, title := range fixtures {
		e := testEntry(title, ScopeProject)
		e.Summary = "fixture " + title
		if _, err := s.Save(ctx, e); err != nil {
			t.Fatalf("save %q: %v", title, err)
		}
	}
}

// searchParityQueries runs the M-1 parity query set against s and returns the
// ordered result titles per query.
func searchParityQueries(ctx context.Context, t *testing.T, s Store, queries []string) map[string][]string {
	t.Helper()
	out := make(map[string][]string, len(queries))
	for _, q := range queries {
		got, err := s.Search(ctx, Query{Text: q, Scope: ScopeProject})
		if err != nil {
			t.Fatalf("search %q: %v", q, err)
		}
		titles := make([]string, len(got))
		for i, r := range got {
			titles[i] = r.Title
		}
		out[q] = titles
	}
	return out
}

// TestFTSPrefixAndPhraseParityWithLikePath is the regression test for M-1:
// the FTS5 MATCH fast path and the Phase-1 LIKE path returned different
// results for prefix and phrase queries. FTS5 'run*' matches only tokens that
// START with 'run' (so 'rerun' misses), while LIKE '%run%' matches any
// substring; FTS5 '"cache invalidation"' matches contiguous tokens (so
// 'cache-invalidation' hits), while LIKE requires the verbatim substring
// 'cache invalidation'. Before the fix the FTS-enabled path therefore
// disagreed with the FTS-disabled and in-memory paths for both queries. The
// fix routes every query whose parsed FTS parts are not all plain tokens
// through the authoritative LIKE path, so all three paths return identical
// ordered results. The corpus also carries malformed (unbalanced quote) and
// oversized (65+ char token) entries that must never spuriously match, and
// the negative path is a phrase with zero hits on every path.
func TestFTSPrefixAndPhraseParityWithLikePath(t *testing.T) {
	ctx := context.Background()
	fixtures := []string{
		"rerun the build",            // 'run' appears only as a substring: 'run*' must NOT hit
		"runbook steps",              // a token that starts with 'run': 'run*' must hit on every path
		"cache-invalidation",         // FTS5 contiguous tokens 'cache' 'invalidation'; no verbatim phrase substring
		"cache invalidation is hard", // verbatim phrase: must hit on every path
		`malformed "quote`,           // malformed corpus entry (unbalanced quote)
		strings.Repeat("z", 65),      // oversized corpus entry (65+ char token)
		"DeepSeek v4-flash: transient HTTP 400 escalation",
		"the of day",
	}
	queries := []string{"run*", `"cache invalidation"`, `"definitely absent phrase"`}
	want := map[string][]string{
		"run*":                       {"rerun the build", "runbook steps"},
		`"cache invalidation"`:       {"cache invalidation is hard"},
		`"definitely absent phrase"`: nil,
	}

	// Path 1: sqlite with the FTS MATCH path active (the pre-fix default).
	ftsOn := parityStore(t, BackendSQLite)
	saveParityFixtures(ctx, t, ftsOn, fixtures)
	// Path 2: sqlite with the FTS MATCH path disabled (LIKE only).
	ftsOff := parityStore(t, BackendSQLite)
	ftsOff.(*sqliteStore).fts = false
	saveParityFixtures(ctx, t, ftsOff, fixtures)
	// Path 3: the in-memory backend.
	mem := parityStore(t, BackendMemory)
	saveParityFixtures(ctx, t, mem, fixtures)

	enabled := searchParityQueries(ctx, t, ftsOn, queries)
	disabled := searchParityQueries(ctx, t, ftsOff, queries)
	memoryResults := searchParityQueries(ctx, t, mem, queries)
	for _, q := range queries {
		if !equalStrings(enabled[q], disabled[q]) {
			t.Errorf("query %q: FTS-enabled %v != FTS-disabled %v: prefix/phrase queries must run the same LIKE path on every backend", q, enabled[q], disabled[q])
		}
		if !equalStrings(enabled[q], memoryResults[q]) {
			t.Errorf("query %q: FTS-enabled %v != memory %v: prefix/phrase queries must run the same LIKE path on every backend", q, enabled[q], memoryResults[q])
		}
		if !equalStrings(enabled[q], want[q]) {
			t.Errorf("query %q = %v, want %v", q, enabled[q], want[q])
		}
	}
}

// fuzzParityFixtures are the corpus titles for FuzzMemorySearchParity: the
// same parity surface as TestFTSPrefixAndPhraseParityWithLikePath, so the
// fuzzer mutates queries on top of content that provokes the prefix and
// phrase divergences M-1 fixed.
func fuzzParityFixtures() []string {
	return []string{
		"rerun the build",
		"runbook steps",
		"cache-invalidation",
		"cache invalidation is hard",
		`malformed "quote`,
		strings.Repeat("z", 65),
		"DeepSeek v4-flash: transient HTTP 400 escalation",
		"the of day",
	}
}

// FuzzMemorySearchParity asserts the backend-parity invariant on every query:
// the FTS-enabled sqlite path, the FTS-disabled (LIKE) path, and the
// in-memory backend must return identical ordered results. Before the M-1 fix
// a prefix query ('run*': FTS5 token-prefix vs LIKE substring) or a phrase
// query ('"cache invalidation"': FTS5 contiguous tokens vs LIKE verbatim
// substring) diverged. Empty queries are refused by Search (not a parity
// violation) and are skipped; the query is bounded to 256 bytes and the
// tokenizer caps tokens and token count, so each iteration stays small.
func FuzzMemorySearchParity(f *testing.F) {
	fixtures := fuzzParityFixtures()
	for _, seed := range []string{
		"run*",
		`"cache invalidation"`,
		"cache invalidation",
		"DeepSeek 400",
		"the of",
		"-",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, q string) {
		if strings.TrimSpace(q) == "" {
			return // Search refuses empty queries on every backend; not a parity violation
		}
		if len(q) > 256 {
			q = q[:256]
		}
		run := func(backend string, disableFTS bool) []string {
			t.Helper()
			dir := t.TempDir()
			s, err := Open(Config{
				Backend:          backend,
				ProjectPath:      filepath.Join(dir, "project.db"),
				OrgPath:          filepath.Join(dir, "org.db"),
				MaxEntryBytes:    8192,
				MaxEntries:       10,
				MaxSearchResults: 8,
			})
			if err != nil {
				t.Fatalf("Open(%s): %v", backend, err)
			}
			defer s.Close()
			if disableFTS {
				s.(*sqliteStore).fts = false
			}
			for _, title := range fixtures {
				e := testEntry(title, ScopeProject)
				e.Summary = "fixture " + title
				if _, err := s.Save(context.Background(), e); err != nil {
					t.Fatalf("save %q: %v", title, err)
				}
			}
			got, err := s.Search(context.Background(), Query{Text: q, Scope: ScopeProject})
			if err != nil {
				t.Fatalf("search %q: %v", q, err)
			}
			titles := make([]string, len(got))
			for i, r := range got {
				titles[i] = r.Title
			}
			return titles
		}
		enabled := run(BackendSQLite, false)
		disabled := run(BackendSQLite, true)
		mem := run(BackendMemory, false)
		if !equalStrings(enabled, disabled) {
			t.Fatalf("query %q: FTS-enabled %v != FTS-disabled %v", q, enabled, disabled)
		}
		if !equalStrings(enabled, mem) {
			t.Fatalf("query %q: sqlite %v != memory %v", q, enabled, mem)
		}
	})
}
