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
