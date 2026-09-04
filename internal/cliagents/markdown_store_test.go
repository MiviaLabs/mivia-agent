package cliagents

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/memory"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

func TestMarkdownStoreSaveSearchCountAndReopen(t *testing.T) {
	root, indexPath := t.TempDir(), filepath.Join(t.TempDir(), "context.db")
	source, err := memory.NewMarkdownSource(root, filepath.Join(t.TempDir(), "org"), "acme")
	if err != nil {
		t.Fatal(err)
	}
	index, err := storage.OpenSQLite(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenMarkdownStore(context.Background(), MarkdownStoreConfig{Source: source, Index: index, ProjectID: "repo"})
	if err != nil {
		t.Fatal(err)
	}
	e := memory.Entry{Title: "Cache lock", Scope: memory.ScopeProject, Verdict: memory.VerdictGood, Summary: "Use a lock.", Why: "Writers overlap."}
	first, err := store.Save(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Save(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("IDs differ: %q %q", first.ID, second.ID)
	}
	hits, err := store.Search(context.Background(), memory.Query{Text: "cache lock", Scope: memory.ScopeProject})
	if err != nil || len(hits) != 1 {
		t.Fatalf("hits=%d err=%v", len(hits), err)
	}
	if count, err := store.Count(context.Background(), memory.ScopeProject); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenMarkdownStore(context.Background(), MarkdownStoreConfig{Source: source, Index: index, ProjectID: "repo"})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if count, err := reopened.Count(context.Background(), memory.ScopeProject); err != nil || count != 1 {
		t.Fatalf("reopened count=%d err=%v", count, err)
	}
}

func TestMarkdownStoreReadOnlyRejectsSave(t *testing.T) {
	source, err := memory.NewMarkdownSource(t.TempDir(), filepath.Join(t.TempDir(), "org"), "acme")
	if err != nil {
		t.Fatal(err)
	}
	index, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenMarkdownStore(context.Background(), MarkdownStoreConfig{Source: source, Index: index, ProjectID: "repo", ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Save(context.Background(), memory.Entry{Title: "no", Scope: memory.ScopeProject, Verdict: memory.VerdictGood, Summary: "no", Why: "no"}); err == nil {
		t.Fatal("read-only save succeeded")
	}
}
