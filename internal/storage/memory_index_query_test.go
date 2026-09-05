package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestSearchMemoryIndexScopesAndReturnsContent(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	docs := []MemoryIndexDocument{
		{ID: "project", Scope: "project", ProjectID: "repo", SourcePath: "/p.md", SourceHash: "p", Title: "Project cache", Summary: "local", Verdict: "good", Content: "project cache"},
		{ID: "org", Scope: "org", OrgID: "acme", SourcePath: "/o.md", SourceHash: "o", Title: "Org cache", Summary: "shared", Verdict: "good", Content: "org cache"},
	}
	if err := store.SyncMemoryIndex(context.Background(), "project", "repo", "", docs[:1]); err != nil {
		t.Fatal(err)
	}
	if err := store.SyncMemoryIndex(context.Background(), "org", "", "acme", docs[1:]); err != nil {
		t.Fatal(err)
	}
	results, err := store.SearchMemoryIndex(context.Background(), "all", "repo", "acme", "cache", 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Content == "" || results[1].Content == "" {
		t.Fatalf("results = %#v, want both scoped documents with content", results)
	}
	if count, err := store.CountMemoryIndex(context.Background(), "project", "repo", ""); err != nil || count != 1 {
		t.Fatalf("project index count = %d, err=%v; want 1", count, err)
	}
}

func TestSearchMemoryIndexTreatsWildcardsAsText(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	doc := MemoryIndexDocument{ID: "literal", Scope: "project", ProjectID: "repo", SourcePath: "/m.md", SourceHash: "h", Title: "literal percent", Summary: "100%", Verdict: "good", Content: "literal"}
	if err := store.SyncMemoryIndex(context.Background(), "project", "repo", "", []MemoryIndexDocument{doc}); err != nil {
		t.Fatal(err)
	}
	results, err := store.SearchMemoryIndex(context.Background(), "project", "repo", "", "%", 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("literal wildcard search returned %d rows, want 1", len(results))
	}
}

func TestSearchMemoryIndexRejectsEmptyQuery(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.SearchMemoryIndex(context.Background(), "project", "repo", "", "   ", 8); err == nil {
		t.Fatal("expected error for blank query text")
	}
}

func TestSearchMemoryIndexRejectsInvalidScope(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.SearchMemoryIndex(context.Background(), "bogus", "repo", "", "cache", 8); err == nil {
		t.Fatal("expected error for invalid scope")
	}
}

func TestSearchMemoryIndexRejectsInvalidOrgID(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.SearchMemoryIndex(context.Background(), "org", "", "../escape", "cache", 8); err == nil {
		t.Fatal("expected error for invalid org_id")
	}
}

// TestSearchMemoryIndexDefaultsLimit drives limit<=0 through the exported
// API and proves the zero value is replaced with the documented default of
// 8 rather than returning every match.
func TestSearchMemoryIndexDefaultsLimit(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	docs := make([]MemoryIndexDocument, 0, 9)
	for i := 0; i < 9; i++ {
		docs = append(docs, MemoryIndexDocument{
			ID:         fmt.Sprintf("doc-%d", i),
			Scope:      "project",
			ProjectID:  "repo",
			SourcePath: fmt.Sprintf("/doc-%d.md", i),
			SourceHash: fmt.Sprintf("h%d", i),
			Title:      fmt.Sprintf("widget entry %d", i),
			Summary:    "widget",
			Verdict:    "good",
			Content:    "widget content",
		})
	}
	if err := store.SyncMemoryIndex(context.Background(), "project", "repo", "", docs); err != nil {
		t.Fatal(err)
	}
	results, err := store.SearchMemoryIndex(context.Background(), "project", "repo", "", "widget", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 8 {
		t.Fatalf("limit<=0 should default to 8, got %d results", len(results))
	}
}

// TestSearchMemoryIndexPhraseQuery drives a quoted phrase through the
// exported search path, exercising the phrase-clause branch that builds
// LIKE predicates from memoryQueryPhrases (as opposed to loose tokens).
func TestSearchMemoryIndexPhraseQuery(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	docs := []MemoryIndexDocument{
		{ID: "match", Scope: "project", ProjectID: "repo", SourcePath: "/m.md", SourceHash: "m", Title: "exact phrase match", Summary: "s", Verdict: "good", Content: "an exact phrase inside content"},
		{ID: "nomatch", Scope: "project", ProjectID: "repo", SourcePath: "/n.md", SourceHash: "n", Title: "phrase exact unrelated", Summary: "s", Verdict: "good", Content: "phrase appears, but exact does not follow it"},
	}
	if err := store.SyncMemoryIndex(context.Background(), "project", "repo", "", docs); err != nil {
		t.Fatal(err)
	}
	results, err := store.SearchMemoryIndex(context.Background(), "project", "repo", "", `"exact phrase"`, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ID != "match" {
		t.Fatalf("phrase search results = %#v, want only the exact-phrase document", results)
	}
}

// TestSearchMemoryIndexQueryContextError forces the SELECT itself to fail
// by dropping the table it reads from after schema creation, proving
// SearchMemoryIndex propagates the QueryContext error rather than
// swallowing it.
func TestSearchMemoryIndexQueryContextError(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.db.Exec(`DROP TABLE memory_entries`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SearchMemoryIndex(context.Background(), "project", "repo", "", "cache", 8); err == nil {
		t.Fatal("expected error once memory_entries no longer exists")
	}
}

func TestCountMemoryIndexRejectsInvalidScope(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CountMemoryIndex(context.Background(), "all", "repo", ""); err == nil {
		t.Fatal("expected error for scope \"all\", which CountMemoryIndex does not accept")
	}
}

// TestCountMemoryIndexQueryError forces the COUNT(*) query to fail the same
// way TestSearchMemoryIndexQueryContextError does, proving CountMemoryIndex
// wraps and returns the underlying error.
func TestCountMemoryIndexQueryError(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.db.Exec(`DROP TABLE memory_entries`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CountMemoryIndex(context.Background(), "project", "repo", ""); err == nil {
		t.Fatal("expected error once memory_entries no longer exists")
	} else if !strings.Contains(err.Error(), "count memory index") {
		t.Fatalf("error = %v, want it wrapped with \"count memory index\"", err)
	}
}

// TestMemoryQueryPhrasesDoubledQuotes exercises the whole-string doubled-
// quote branch: every quote in the input is immediately followed by
// another quote, so memoryAllQuotesDoubled reports true and the function
// returns the input with escaped quotes collapsed as a single phrase.
func TestMemoryQueryPhrasesDoubledQuotes(t *testing.T) {
	got := memoryQueryPhrases(`a""b`)
	want := []string{`a"b`}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("memoryQueryPhrases(%q) = %#v, want %#v", `a""b`, got, want)
	}
}

// TestMemoryQueryPhrasesUnterminated proves an opening quote with no
// matching close breaks out of the extraction loop instead of panicking
// or looping forever.
func TestMemoryQueryPhrasesUnterminated(t *testing.T) {
	got := memoryQueryPhrases(`find "unterminated phrase`)
	if len(got) != 0 {
		t.Fatalf("memoryQueryPhrases with an unterminated quote = %#v, want none", got)
	}
}

// TestMemoryQueryPhrasesExtractsQuotedSegment covers the normal
// open-quote/close-quote extraction loop (not the doubled-quote shortcut,
// since the leading quote here has ordinary text after it rather than
// another quote): each quoted segment becomes its own phrase, and the
// loop continues past the first pair to find a second one.
func TestMemoryQueryPhrasesExtractsQuotedSegment(t *testing.T) {
	got := memoryQueryPhrases(`find "exact phrase" and "second phrase" now`)
	want := []string{"exact phrase", "second phrase"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("memoryQueryPhrases(...) = %#v, want %#v", got, want)
	}
}

// TestMemoryAllQuotesDoubledUnpaired covers the early-return-false branch:
// a lone quote not immediately followed by another quote.
func TestMemoryAllQuotesDoubledUnpaired(t *testing.T) {
	if memoryAllQuotesDoubled(`abc"def`) {
		t.Fatal("expected false for a single unpaired quote")
	}
}

// TestMemoryAllQuotesDoubledPaired covers the i++ skip-ahead branch for a
// properly doubled quote pair, and the no-quotes-at-all false branch.
func TestMemoryAllQuotesDoubledPaired(t *testing.T) {
	if !memoryAllQuotesDoubled(`a""b`) {
		t.Fatal("expected true for a doubled quote pair")
	}
	if memoryAllQuotesDoubled("no quotes here") {
		t.Fatal("expected false when the input has no quotes at all")
	}
}
