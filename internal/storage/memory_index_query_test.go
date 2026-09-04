package storage

import (
	"context"
	"path/filepath"
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
