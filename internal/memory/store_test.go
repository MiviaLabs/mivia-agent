package memory

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func testEntry(title string, scope Scope) Entry {
	return Entry{
		Title:   title,
		Scope:   scope,
		Verdict: VerdictGood,
		Created: "2026-08-09",
		Summary: "summary of " + title,
		Why:     "why " + title,
		Good:    "- worked",
		Bad:     "- none",
	}
}

func newTestStore(t *testing.T, backend, orgID string) Store {
	t.Helper()
	dir := t.TempDir()
	cfg := Config{
		Backend:          backend,
		ProjectPath:      filepath.Join(dir, "project.db"),
		OrgPath:          filepath.Join(dir, "org.db"),
		OrgID:            orgID,
		MaxEntryBytes:    8192,
		MaxEntries:       5,
		MaxSearchResults: 8,
	}
	s, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestStoreSaveAndSearchScopes(t *testing.T) {
	for _, backend := range []string{"sqlite", "memory"} {
		t.Run(backend, func(t *testing.T) {
			s := newTestStore(t, backend, "github.com/acme")
			ctx := context.Background()
			for i := 0; i < 3; i++ {
				if _, err := s.Save(ctx, testEntry(fmt.Sprintf("proj-%d", i), ScopeProject)); err != nil {
					t.Fatalf("save project: %v", err)
				}
			}
			for i := 0; i < 2; i++ {
				if _, err := s.Save(ctx, testEntry(fmt.Sprintf("org-%d", i), ScopeOrg)); err != nil {
					t.Fatalf("save org: %v", err)
				}
			}
			proj, err := s.Search(ctx, Query{Text: "proj", Scope: ScopeProject})
			if err != nil {
				t.Fatalf("search project: %v", err)
			}
			if len(proj) != 3 {
				t.Errorf("project search = %d results, want 3", len(proj))
			}
			for _, r := range proj {
				if r.Scope != ScopeProject {
					t.Errorf("project search leaked scope %q", r.Scope)
				}
			}
			org, err := s.Search(ctx, Query{Text: "org", Scope: ScopeOrg})
			if err != nil {
				t.Fatalf("search org: %v", err)
			}
			if len(org) != 2 {
				t.Errorf("org search = %d results, want 2", len(org))
			}
			for _, r := range org {
				if r.Scope != ScopeOrg || r.Org != "github.com/acme" {
					t.Errorf("org search leaked scope/org: %+v", r)
				}
			}
			all, err := s.Search(ctx, Query{Text: "-", Scope: ScopeAll})
			if err != nil {
				t.Fatalf("search all: %v", err)
			}
			if len(all) != 5 {
				t.Errorf("all search = %d results, want 5", len(all))
			}
			if n, err := s.Count(ctx, ScopeProject); err != nil || n != 3 {
				t.Errorf("project count = %d, %v", n, err)
			}
			if n, err := s.Count(ctx, ScopeOrg); err != nil || n != 2 {
				t.Errorf("org count = %d, %v", n, err)
			}
		})
	}
}

func TestStoreSaveIsIdempotentForIdenticalEntry(t *testing.T) {
	for _, backend := range []string{"sqlite", "memory"} {
		t.Run(backend, func(t *testing.T) {
			s := newTestStore(t, backend, "")
			ctx := context.Background()
			first, err := s.Save(ctx, testEntry("same", ScopeProject))
			if err != nil {
				t.Fatal(err)
			}
			second, err := s.Save(ctx, testEntry("same", ScopeProject))
			if err != nil {
				t.Fatal(err)
			}
			if first.ID == "" || first.ID != second.ID {
				t.Errorf("identical re-save must return the same id: %q vs %q", first.ID, second.ID)
			}
			if n, _ := s.Count(ctx, ScopeProject); n != 1 {
				t.Errorf("count = %d, want 1 (no duplicate rows)", n)
			}
		})
	}
}

func TestStoreSaveOrgRequiresOrgID(t *testing.T) {
	for _, backend := range []string{"sqlite", "memory"} {
		t.Run(backend, func(t *testing.T) {
			s := newTestStore(t, backend, "")
			_, err := s.Save(context.Background(), testEntry("x", ScopeOrg))
			if err == nil || !strings.Contains(err.Error(), "org") {
				t.Fatalf("save scope=org without org_id must fail clearly, got %v", err)
			}
		})
	}
}

func TestStoreMaxEntriesCap(t *testing.T) {
	for _, backend := range []string{"sqlite", "memory"} {
		t.Run(backend, func(t *testing.T) {
			s := newTestStore(t, backend, "github.com/acme")
			ctx := context.Background()
			for i := 0; i < 5; i++ {
				if _, err := s.Save(ctx, testEntry(fmt.Sprintf("p-%d", i), ScopeProject)); err != nil {
					t.Fatalf("save %d: %v", i, err)
				}
			}
			_, err := s.Save(ctx, testEntry("overflow", ScopeProject))
			if err == nil || !strings.Contains(err.Error(), "max_entries") {
				t.Fatalf("over-cap save must fail with max_entries, got %v", err)
			}
		})
	}
}

func TestStoreSearchResultLimit(t *testing.T) {
	for _, backend := range []string{"sqlite", "memory"} {
		t.Run(backend, func(t *testing.T) {
			dir := t.TempDir()
			s, err := Open(Config{
				Backend:          backend,
				ProjectPath:      filepath.Join(dir, "project.db"),
				OrgPath:          filepath.Join(dir, "org.db"),
				MaxEntryBytes:    8192,
				MaxEntries:       50,
				MaxSearchResults: 8,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			ctx := context.Background()
			for i := 0; i < 10; i++ {
				if _, err := s.Save(ctx, testEntry(fmt.Sprintf("limit-%d", i), ScopeProject)); err != nil {
					t.Fatal(err)
				}
			}
			got, err := s.Search(ctx, Query{Text: "limit", Scope: ScopeProject, MaxResults: 3})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 3 {
				t.Errorf("max_results=3 returned %d", len(got))
			}
			// 0 means the store default (8).
			got, err = s.Search(ctx, Query{Text: "limit", Scope: ScopeProject})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 8 {
				t.Errorf("default limit returned %d, want 8", len(got))
			}
			// A request above the store cap is clamped, never exceeded.
			got, err = s.Search(ctx, Query{Text: "limit", Scope: ScopeProject, MaxResults: 50})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 8 {
				t.Errorf("clamped limit returned %d, want 8", len(got))
			}
		})
	}
}

func TestStoreSearchExactTitleRankedFirst(t *testing.T) {
	for _, backend := range []string{"sqlite", "memory"} {
		t.Run(backend, func(t *testing.T) {
			s := newTestStore(t, backend, "")
			ctx := context.Background()
			for _, title := range []string{"cache invalidation is hard", "cache", "the cache key"} {
				if _, err := s.Save(ctx, testEntry(title, ScopeProject)); err != nil {
					t.Fatal(err)
				}
			}
			got, err := s.Search(ctx, Query{Text: "cache", Scope: ScopeProject})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 3 {
				t.Fatalf("want 3 matches, got %d", len(got))
			}
			if got[0].Title != "cache" {
				t.Errorf("exact title match must rank first, got %q", got[0].Title)
			}
		})
	}
}

func TestStoreSearchEscapesWildcards(t *testing.T) {
	for _, backend := range []string{"sqlite", "memory"} {
		t.Run(backend, func(t *testing.T) {
			s := newTestStore(t, backend, "")
			ctx := context.Background()
			if _, err := s.Save(ctx, testEntry("100%_done", ScopeProject)); err != nil {
				t.Fatal(err)
			}
			for _, q := range []string{"100%", "%", "_done", "done"} {
				got, err := s.Search(ctx, Query{Text: q, Scope: ScopeProject})
				if err != nil {
					t.Fatalf("search %q: %v", q, err)
				}
				if len(got) != 1 {
					t.Errorf("search %q = %d results, want 1 (literal matching)", q, len(got))
				}
			}
		})
	}
}

func TestStoreSearchEmptyQueryFails(t *testing.T) {
	for _, backend := range []string{"sqlite", "memory"} {
		t.Run(backend, func(t *testing.T) {
			s := newTestStore(t, backend, "")
			if _, err := s.Search(context.Background(), Query{Text: "  ", Scope: ScopeProject}); err == nil {
				t.Fatal("empty query must fail")
			}
		})
	}
}

func TestStoreSearchOrgWithoutOrgIDReturnsEmpty(t *testing.T) {
	for _, backend := range []string{"sqlite", "memory"} {
		t.Run(backend, func(t *testing.T) {
			s := newTestStore(t, backend, "")
			got, err := s.Search(context.Background(), Query{Text: "anything", Scope: ScopeOrg})
			if err != nil {
				t.Fatalf("org search without org_id must not error: %v", err)
			}
			if len(got) != 0 {
				t.Errorf("want empty, got %d", len(got))
			}
		})
	}
}

func TestStoreOrgIsolationAcrossOrgIDs(t *testing.T) {
	dir := t.TempDir()
	orgPath := filepath.Join(dir, "org.db")
	open := func(orgID string) Store {
		t.Helper()
		s, err := Open(Config{
			Backend:          "sqlite",
			ProjectPath:      filepath.Join(dir, "project.db"),
			OrgPath:          orgPath,
			OrgID:            orgID,
			MaxEntryBytes:    8192,
			MaxEntries:       50,
			MaxSearchResults: 8,
		})
		if err != nil {
			t.Fatalf("Open %q: %v", orgID, err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	a := open("github.com/acme")
	b := open("github.com/beta")
	ctx := context.Background()
	if _, err := a.Save(ctx, testEntry("acme-secret", ScopeOrg)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Save(ctx, testEntry("beta-secret", ScopeOrg)); err != nil {
		t.Fatal(err)
	}
	got, err := b.Search(ctx, Query{Text: "secret", Scope: ScopeOrg})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Title != "beta-secret" {
		t.Errorf("org isolation broken: %+v", got)
	}
	if n, _ := a.Count(ctx, ScopeOrg); n != 1 {
		t.Errorf("org a count = %d, want 1", n)
	}
}

func TestStoreConcurrentSaves(t *testing.T) {
	for _, backend := range []string{"sqlite", "memory"} {
		t.Run(backend, func(t *testing.T) {
			dir := t.TempDir()
			s, err := Open(Config{
				Backend:          backend,
				ProjectPath:      filepath.Join(dir, "project.db"),
				OrgPath:          filepath.Join(dir, "org.db"),
				MaxEntryBytes:    8192,
				MaxEntries:       100,
				MaxSearchResults: 8,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			ctx := context.Background()
			var wg sync.WaitGroup
			for i := 0; i < 20; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					_, err := s.Save(ctx, testEntry(fmt.Sprintf("race-%d", i), ScopeProject))
					if err != nil {
						t.Errorf("concurrent save %d: %v", i, err)
					}
				}(i)
			}
			wg.Wait()
			if n, err := s.Count(ctx, ScopeProject); err != nil || n != 20 {
				t.Errorf("count = %d, %v; want 20", n, err)
			}
		})
	}
}

func TestOpenRejectsInvalidBlockPattern(t *testing.T) {
	dir := t.TempDir()
	_, err := Open(Config{
		Backend:       "sqlite",
		ProjectPath:   filepath.Join(dir, "project.db"),
		BlockPatterns: []string{"["},
	})
	if err == nil {
		t.Fatal("invalid block pattern must fail Open")
	}
}

func TestStoreSaveRejectsBlockedContent(t *testing.T) {
	for _, backend := range []string{"sqlite", "memory"} {
		t.Run(backend, func(t *testing.T) {
			dir := t.TempDir()
			s, err := Open(Config{
				Backend:          backend,
				ProjectPath:      filepath.Join(dir, "project.db"),
				OrgPath:          filepath.Join(dir, "org.db"),
				MaxEntryBytes:    8192,
				MaxEntries:       5,
				MaxSearchResults: 8,
				BlockPatterns:    []string{`sk-[A-Za-z0-9]+`},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			e := testEntry("deploy", ScopeProject)
			e.Good = "token sk-x"
			if _, err := s.Save(context.Background(), e); err == nil {
				t.Fatal("blocked content must be refused")
			}
		})
	}
}

func TestStoreSearchResultShape(t *testing.T) {
	for _, backend := range []string{"sqlite", "memory"} {
		t.Run(backend, func(t *testing.T) {
			s := newTestStore(t, backend, "")
			ctx := context.Background()
			e := testEntry("shape", ScopeProject)
			e.Tags = []string{"go", "sqlite"}
			e.Verdict = VerdictMixed
			if _, err := s.Save(ctx, e); err != nil {
				t.Fatal(err)
			}
			got, err := s.Search(ctx, Query{Text: "shape", Scope: ScopeProject})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 {
				t.Fatalf("want 1 result, got %d", len(got))
			}
			r := got[0]
			if r.ID == "" || r.Title != "shape" || r.Verdict != VerdictMixed || r.Created != "2026-08-09" || r.Snippet != "summary of shape" {
				t.Errorf("result shape wrong: %+v", r)
			}
			if len(r.Tags) != 2 || r.Tags[0] != "go" {
				t.Errorf("result tags wrong: %+v", r.Tags)
			}
		})
	}
}

func TestStoreSaveFillsCreatedDate(t *testing.T) {
	for _, backend := range []string{"sqlite", "memory"} {
		t.Run(backend, func(t *testing.T) {
			s := newTestStore(t, backend, "")
			e := testEntry("dated", ScopeProject)
			e.Created = ""
			saved, err := s.Save(context.Background(), e)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Created == "" {
				t.Fatal("Save must fill the created date")
			}
		})
	}
}

var errNoRows = errors.New("no rows")
