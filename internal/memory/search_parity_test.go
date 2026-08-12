package memory

// Regression test for the backend-parity defect in memStore.matchRows: the
// in-memory backend built its searchable body from tags+references+good+bad+why
// only, while the sqlite backend searches the full rendered Markdown stored in
// the content column (lower(content) LIKE in searchSQL). A query matching only
// rendered metadata - the "verdict: good" line, the "scope: project" line, the
// created line, or a section heading - returned the entry on sqlite but
// silently returned zero results on the memory backend.

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

func TestStoreSearchMatchesRenderedMetadataAcrossBackends(t *testing.T) {
	for _, backend := range []string{"sqlite", "memory"} {
		t.Run(backend, func(t *testing.T) {
			s := newTestStore(t, backend, "")
			ctx := context.Background()
			// testEntry("parity", ScopeProject) picks fields so "good" appears
			// ONLY in the rendered "verdict: good" line and "project" appears
			// ONLY in the rendered "scope: project" line: neither word occurs
			// in the title, summary, good, bad, why, tags, or references.
			if _, err := s.Save(ctx, testEntry("parity", ScopeProject)); err != nil {
				t.Fatal(err)
			}
			for _, text := range []string{"good", "project"} {
				got, err := s.Search(ctx, Query{Text: text, Scope: ScopeProject})
				if err != nil {
					t.Fatalf("search %q: %v", text, err)
				}
				if len(got) != 1 {
					t.Errorf("search %q = %d results, want 1 (both backends must match rendered metadata)", text, len(got))
					continue
				}
				if got[0].Title != "parity" {
					t.Errorf("search %q matched %q, want the parity entry", text, got[0].Title)
				}
			}
		})
	}
}

// TestSearchOrderingParityAcrossBackends pins the cross-backend ordering
// contract: for identical data, the SQLite and in-memory backends must return
// results in the same order - rank, then Created DESC, then title ASC (the
// ordering matchRows and mergeRanked implement). The fixtures deliberately
// reverse Created-vs-insertion order and include an equal-Created pair, so any
// backend that sorts same-rank results by insertion time (the old created_at
// ORDER BY on sqlite) diverges from the memory backend. Before the fix the
// sqlite project-scope order was [mike, beta, alpha, zeta] (insertion DESC)
// instead of [zeta, beta, mike, alpha]; after the fix both backends agree for
// scope project and scope all.
func TestSearchOrderingParityAcrossBackends(t *testing.T) {
	type fixture struct {
		title   string
		created string
		scope   Scope
	}
	projectFixtures := []fixture{
		{"zeta cache", "2026-01-05", ScopeProject},  // saved first, newest Created
		{"alpha cache", "2026-01-01", ScopeProject}, // saved second, oldest Created
		{"beta cache", "2026-01-03", ScopeProject},  // equal-Created pair ...
		{"mike cache", "2026-01-03", ScopeProject},  // ... tie-breaks by title ASC
	}
	orgFixtures := []fixture{
		{"delta cache", "2026-01-04", ScopeOrg},
		{"charlie cache", "2026-01-02", ScopeOrg},
	}
	wantProject := []string{"zeta cache", "beta cache", "mike cache", "alpha cache"}
	wantAll := []string{"zeta cache", "delta cache", "beta cache", "mike cache", "charlie cache", "alpha cache"}

	for _, backend := range []string{"sqlite", "memory"} {
		t.Run(backend, func(t *testing.T) {
			s := newTestStore(t, backend, "github.com/acme")
			ctx := context.Background()
			save := func(f fixture) {
				t.Helper()
				e := testEntry(f.title, f.scope)
				e.Created = f.created
				if _, err := s.Save(ctx, e); err != nil {
					t.Fatalf("save %s: %v", f.title, err)
				}
			}
			for _, f := range projectFixtures {
				save(f)
			}
			for _, f := range orgFixtures {
				save(f)
			}
			for _, tc := range []struct {
				scope Scope
				want  []string
			}{
				{ScopeProject, wantProject},
				{ScopeAll, wantAll},
			} {
				got, err := s.Search(ctx, Query{Text: "cache", Scope: tc.scope})
				if err != nil {
					t.Fatalf("scope %s search: %v", tc.scope, err)
				}
				titles := make([]string, len(got))
				for i, r := range got {
					titles[i] = r.Title
				}
				if !equalStrings(titles, tc.want) {
					t.Errorf("scope %s order = %v, want %v (rank, Created DESC, title ASC)", tc.scope, titles, tc.want)
				}
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// searchTitles runs one search and returns the ordered result titles.
func searchTitles(t *testing.T, s Store, text string, scope Scope) []string {
	t.Helper()
	got, err := s.Search(context.Background(), Query{Text: text, Scope: scope})
	if err != nil {
		t.Fatalf("search %q: %v", text, err)
	}
	titles := make([]string, len(got))
	for i, r := range got {
		titles[i] = r.Title
	}
	return titles
}

// TestIdAscTieBreakParity pins the deterministic final tie-break: rows with
// the same rank, created date, and title order by id ASC, identically on both
// backends (per-scope matchRows/searchSQL and the ScopeAll merge re-rank).
func TestIdAscTieBreakParity(t *testing.T) {
	var orders [][]string
	for _, backend := range []string{"sqlite", "memory"} {
		s := newTestStore(t, backend, "github.com/acme")
		ctx := context.Background()
		// Identical title and created date, different content: different
		// content-addressed ids, so only the id-ASC final tie-break can order
		// the two rows.
		for i := 0; i < 2; i++ {
			e := testEntry("tie cache", ScopeProject)
			e.Created = "2026-01-01"
			e.Why = fmt.Sprintf("why variant %d", i)
			if _, err := s.Save(ctx, e); err != nil {
				t.Fatal(err)
			}
		}
		got, err := s.Search(ctx, Query{Text: "cache", Scope: ScopeAll})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[0].Title != "tie cache" || got[1].Title != "tie cache" {
			t.Fatalf("%s tie-break results = %+v, want two 'tie cache' rows", backend, got)
		}
		orders = append(orders, []string{got[0].ID, got[1].ID})
	}
	if orders[0][0] != orders[1][0] || orders[0][1] != orders[1][1] {
		t.Errorf("id-ASC tie-break must be identical across backends: sqlite %v vs memory %v", orders[0], orders[1])
	}
}

// TestMultiWordOrderIndependentMatchAcrossBackends is the regression for the
// contiguous-substring defect: 'DeepSeek v4-flash: transient HTTP 400
// escalation' is NOT found by 'DeepSeek v4-flash HTTP 400' when the backend
// matches the whole query as one substring (': transient ' separates the
// words). Token-AND matching must find it, in any word order, on both
// backends.
func TestMultiWordOrderIndependentMatchAcrossBackends(t *testing.T) {
	for _, backend := range []string{"sqlite", "memory"} {
		t.Run(backend, func(t *testing.T) {
			s := newTestStore(t, backend, "")
			if _, err := s.Save(context.Background(), testEntry("DeepSeek v4-flash: transient HTTP 400 escalation", ScopeProject)); err != nil {
				t.Fatal(err)
			}
			for _, q := range []string{"DeepSeek 400", "400 DeepSeek", "DeepSeek v4-flash HTTP 400", "deepseek http 400"} {
				titles := searchTitles(t, s, q, ScopeProject)
				if len(titles) != 1 || titles[0] != "DeepSeek v4-flash: transient HTTP 400 escalation" {
					t.Errorf("query %q = %v, want the transient-400 entry (token-AND, order-independent)", q, titles)
				}
			}
		})
	}
}

// TestPunctuationSplitTokensAcrossBackends pins that the tokenizer splits on
// punctuation on both backends: 'v4-flash' finds 'v4 flash' (tokens v4 +
// flash), while 'v4flash' (one token) finds nothing.
func TestPunctuationSplitTokensAcrossBackends(t *testing.T) {
	for _, backend := range []string{"sqlite", "memory"} {
		t.Run(backend, func(t *testing.T) {
			s := newTestStore(t, backend, "")
			if _, err := s.Save(context.Background(), testEntry("v4 flash guide", ScopeProject)); err != nil {
				t.Fatal(err)
			}
			for _, q := range []string{"v4-flash", "v4 flash"} {
				if titles := searchTitles(t, s, q, ScopeProject); len(titles) != 1 {
					t.Errorf("query %q = %v, want 1 result", q, titles)
				}
			}
			if titles := searchTitles(t, s, "v4flash", ScopeProject); len(titles) != 0 {
				t.Errorf("query v4flash = %v, want 0 results (one token, not a substring)", titles)
			}
		})
	}
}

// TestStopwordOnlyFallsBackToPhraseAcrossBackends pins the zero-token fallback
// on both backends: a query whose words are all stopwords (or punctuation)
// degrades to today's whole-phrase substring behavior over title|summary|
// content instead of matching nothing. The approved plan pins '-' as matching
// ALL entries (the rendered content of every fixture carries '-' bullets), and
// 'the of' matches only the row containing the verbatim phrase. The fixtures
// use controlled summaries so the reversed phrase 'of the' is not verbatim
// anywhere.
func TestStopwordOnlyFallsBackToPhraseAcrossBackends(t *testing.T) {
	for _, backend := range []string{"sqlite", "memory"} {
		t.Run(backend, func(t *testing.T) {
			s := newTestStore(t, backend, "")
			ctx := context.Background()
			for _, title := range []string{"proj-1", "proj-2", "the of day", "the daily of coffee"} {
				e := testEntry(title, ScopeProject)
				// testEntry's default summary ("summary of <title>") contains the
				// verbatim substring "of the", which would defeat the reversed
				// phrase assertion; use a controlled summary instead.
				e.Summary = "fixture summary " + title
				if _, err := s.Save(ctx, e); err != nil {
					t.Fatal(err)
				}
			}
			// '-' is punctuation-only: every token is dropped and the query
			// degrades to the whole-phrase fallback, which matches every entry
			// (each rendered content carries '-' bullets).
			if titles := searchTitles(t, s, "-", ScopeProject); len(titles) != 4 {
				t.Errorf("dash query = %v, want all 4 entries (whole-phrase fallback over rendered content)", titles)
			}
			// 'the of' degrades to the whole-phrase substring behavior: only the
			// verbatim phrase row matches, not the row containing both words
			// non-contiguously.
			if titles := searchTitles(t, s, "the of", ScopeProject); len(titles) != 1 || titles[0] != "the of day" {
				t.Errorf("stopword-only query = %v, want only the verbatim phrase row", titles)
			}
			if titles := searchTitles(t, s, "of the", ScopeProject); len(titles) != 0 {
				t.Errorf("reversed stopword phrase = %v, want empty (no verbatim 'of the' anywhere)", titles)
			}
		})
	}
}

// TestPhraseInTitleRanksAboveTokenRanksAcrossBackends pins the extended rank
// ladder: rank 1 (full phrase in title) sorts above rank 4 (all tokens in
// title, phrase not contiguous) which sorts above rank 6 (all tokens in
// content only).
func TestPhraseInTitleRanksAboveTokenRanksAcrossBackends(t *testing.T) {
	for _, backend := range []string{"sqlite", "memory"} {
		t.Run(backend, func(t *testing.T) {
			s := newTestStore(t, backend, "")
			ctx := context.Background()
			phraseTitle := testEntry("cache invalidation is hard", ScopeProject)
			tokenTitle := testEntry("cache the invalidation", ScopeProject)
			contentOnly := testEntry("zzz content", ScopeProject)
			contentOnly.Good = "cache detail"
			contentOnly.Why = "invalidation why"
			for _, e := range []Entry{phraseTitle, tokenTitle, contentOnly} {
				if _, err := s.Save(ctx, e); err != nil {
					t.Fatal(err)
				}
			}
			want := []string{"cache invalidation is hard", "cache the invalidation", "zzz content"}
			if titles := searchTitles(t, s, "cache invalidation", ScopeProject); !equalStrings(titles, want) {
				t.Errorf("ranking = %v, want %v (rank 1 < rank 4 < rank 6)", titles, want)
			}
		})
	}
}

// TestExtendedRankOrderingAcrossBackends pins the per-scope ordering contract
// on both backends: rank (0..6; rank 7 is a ScopeAll merge re-rank, covered by
// TestScopeAllMergeParityMultiToken, because the content field embeds title
// and summary), then created DESC, then title ASC, then id ASC.
func TestExtendedRankOrderingAcrossBackends(t *testing.T) {
	for _, backend := range []string{"sqlite", "memory"} {
		t.Run(backend, func(t *testing.T) {
			// This test saves seven fixtures, so it needs its own store with a
			// MaxEntries cap above seven (newTestStore caps at five).
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
				t.Fatalf("Open: %v", err)
			}
			t.Cleanup(func() { _ = s.Close() })
			ctx := context.Background()
			fixtures := []struct {
				title, summary, good, why string
			}{
				{"deepseek http", "summary", "- worked", "- none"},         // rank 0: title equals query
				{"deepseek http 400", "summary", "- worked", "- none"},     // rank 1: phrase in title
				{"zzz two", "deepseek http summary", "- worked", "- none"}, // rank 2: phrase in summary
				{"zzz three", "summary", "deepseek http detail", "- none"}, // rank 3: phrase in content
				{"deepseek the http", "summary", "- worked", "- none"},     // rank 4: tokens in title, no phrase
				{"zzz five", "deepseek and http", "- worked", "- none"},    // rank 5: tokens in summary, no phrase
				{"zzz six", "summary", "deepseek detail", "http why"},      // rank 6: tokens in content, no phrase
			}
			for _, f := range fixtures {
				e := testEntry(f.title, ScopeProject)
				e.Summary = f.summary
				e.Good = f.good
				e.Why = f.why
				if _, err := s.Save(ctx, e); err != nil {
					t.Fatal(err)
				}
			}
			want := []string{
				"deepseek http", "deepseek http 400", "zzz two", "zzz three",
				"deepseek the http", "zzz five", "zzz six",
			}
			if titles := searchTitles(t, s, "deepseek http", ScopeProject); !equalStrings(titles, want) {
				t.Errorf("extended ranks = %v, want %v", titles, want)
			}
		})
	}
}

// TestZeroHitRelaxationAcrossBackends pins the relaxation contract: a
// multi-token query with zero hits retries by dropping the longest token, at
// most twice, identically on both backends; single-token and all-missing
// queries stay empty.
func TestZeroHitRelaxationAcrossBackends(t *testing.T) {
	for _, backend := range []string{"sqlite", "memory"} {
		t.Run(backend, func(t *testing.T) {
			s := newTestStore(t, backend, "")
			if _, err := s.Save(context.Background(), testEntry("DeepSeek v4-flash: transient HTTP 400 escalation", ScopeProject)); err != nil {
				t.Fatal(err)
			}
			// One retry: the longest token is the missing one.
			if titles := searchTitles(t, s, "extraneous DeepSeek 400", ScopeProject); len(titles) != 1 {
				t.Errorf("one-retry relaxation = %v, want 1 result", titles)
			}
			// Two retries: the two longest tokens are the missing ones.
			if titles := searchTitles(t, s, "firstmissing secondmissing DeepSeek 400", ScopeProject); len(titles) != 1 {
				t.Errorf("two-retry relaxation = %v, want 1 result", titles)
			}
			// A two-token query can relax only once (the second retry would drop
			// everything): bounded, stays empty when both tokens miss.
			if titles := searchTitles(t, s, "zzzqqq wwwrrr", ScopeProject); len(titles) != 0 {
				t.Errorf("two-token all-missing = %v, want empty", titles)
			}
			// All tokens missing: stays empty after the bounded retries.
			if titles := searchTitles(t, s, "zzzqqq wwwrrr eee", ScopeProject); len(titles) != 0 {
				t.Errorf("all-missing relaxation = %v, want empty", titles)
			}
			// Single-token queries never relax.
			if titles := searchTitles(t, s, "zzzqqq", ScopeProject); len(titles) != 0 {
				t.Errorf("single-token query = %v, want empty (never relaxes)", titles)
			}
		})
	}
}

// TestScopeAllMergeParityMultiToken pins that ScopeAll merges project and org
// rows for a multi-token query with identical ordering on both backends:
// rank (body-only rows re-ranked to 7), created DESC, title ASC, id ASC.
func TestScopeAllMergeParityMultiToken(t *testing.T) {
	for _, backend := range []string{"sqlite", "memory"} {
		t.Run(backend, func(t *testing.T) {
			s := newTestStore(t, backend, "github.com/acme")
			ctx := context.Background()
			project := []Entry{testEntry("cache invalidation is hard", ScopeProject)}
			project = append(project, testEntry("cache the invalidation", ScopeProject))
			bodyOnly := testEntry("alpha content", ScopeProject)
			bodyOnly.Good = "cache detail"
			bodyOnly.Why = "invalidation why"
			project = append(project, bodyOnly)
			for _, e := range project {
				if _, err := s.Save(ctx, e); err != nil {
					t.Fatal(err)
				}
			}
			for _, title := range []string{"cache invalidation guide", "invalidation cache zeta"} {
				if _, err := s.Save(ctx, testEntry(title, ScopeOrg)); err != nil {
					t.Fatal(err)
				}
			}
			want := []string{
				"cache invalidation guide", "cache invalidation is hard", // rank 1, title ASC
				"cache the invalidation", "invalidation cache zeta", // rank 4, title ASC
				"alpha content", // rank 7 (body-only re-rank)
			}
			if titles := searchTitles(t, s, "cache invalidation", ScopeAll); !equalStrings(titles, want) {
				t.Errorf("ScopeAll merge = %v, want %v", titles, want)
			}
		})
	}
}
