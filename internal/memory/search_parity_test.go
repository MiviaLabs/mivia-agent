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
