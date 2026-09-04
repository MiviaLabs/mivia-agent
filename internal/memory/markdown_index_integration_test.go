package memory_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/memory"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

func TestMarkdownMemoryRebuildsSharedIndexAndSearchesAfterReopen(t *testing.T) {
	root := t.TempDir()
	source, err := memory.NewMarkdownSource(root, filepath.Join(t.TempDir(), "org-memories"), "github.com/acme")
	if err != nil {
		t.Fatal(err)
	}
	entry := memory.Entry{Title: "Global cache rule", Scope: memory.ScopeProject, Verdict: memory.VerdictGood, Created: "2026-09-04", Summary: "Use the shared cache lock.", Why: "Concurrent writers must not race."}
	doc, err := source.Save(context.Background(), entry)
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "context.db")
	store, err := storage.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	indexDoc := storage.MemoryIndexDocument{ID: doc.ID, Scope: "project", ProjectID: "repo-a", SourcePath: doc.Path, SourceHash: doc.Hash, Title: doc.Entry.Title, Summary: doc.Entry.Summary, Verdict: string(doc.Entry.Verdict), Created: doc.Entry.Created, Content: doc.Entry.Render()}
	if err := store.SyncMemoryIndex(context.Background(), "project", "repo-a", "", []storage.MemoryIndexDocument{indexDoc}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := storage.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	results, err := reopened.SearchMemoryIndex(context.Background(), "project", "repo-a", "", "cache lock", 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Title != entry.Title {
		t.Fatalf("reopened search results = %#v, want saved Markdown memory", results)
	}
}
