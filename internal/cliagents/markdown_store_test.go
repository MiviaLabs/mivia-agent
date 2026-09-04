package cliagents

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/memory"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func TestMarkdownSourceScanIgnoresReadme(t *testing.T) {
	root := t.TempDir()
	source, err := memory.NewMarkdownSource(root, filepath.Join(t.TempDir(), "org"), "acme")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, ".agents", "memories")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Documentation\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if docs, err := source.Scan(context.Background(), memory.ScopeProject); err != nil || len(docs) != 0 {
		t.Fatalf("Scan docs=%d err=%v, want no documents", len(docs), err)
	}
}

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

func TestOpenMemoryStoreUsesMarkdownAndGlobalIndex(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	store, err := OpenMemoryStoreWithReadOnly(root, config.MemoryConfig{
		StoreBackend: memory.BackendMarkdown, MaxEntryBytes: memory.DefaultMaxEntryBytes,
		MaxSearchResults: memory.DefaultMaxSearchResults,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Save(context.Background(), memory.Entry{
		Title: "Global index", Scope: memory.ScopeProject, Verdict: memory.VerdictGood,
		Summary: "Use Markdown files.", Why: "The index is derived.",
	}); err != nil {
		t.Fatal(err)
	}
	hits, err := store.Search(context.Background(), memory.Query{Text: "Markdown", Scope: memory.ScopeProject})
	if err != nil || len(hits) != 1 {
		t.Fatalf("hits=%d err=%v, want one Markdown result", len(hits), err)
	}
	if _, err := os.Stat(workspace.GlobalContextStorePath(root)); err != nil {
		t.Fatalf("global context index: %v", err)
	}
	if _, err := os.Stat(workspace.MemoryDBPath(root)); !os.IsNotExist(err) {
		t.Fatalf("project memory.db exists or stat failed: %v", err)
	}
}

func TestMarkdownStorePromoteCoreAndDelete(t *testing.T) {
	root, indexPath := t.TempDir(), filepath.Join(t.TempDir(), "context.db")
	source, err := memory.NewMarkdownSource(root, filepath.Join(t.TempDir(), "org"), "acme")
	if err != nil {
		t.Fatal(err)
	}
	index, err := storage.OpenSQLite(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	store, err := OpenMarkdownStore(context.Background(), MarkdownStoreConfig{Source: source, Index: index, ProjectID: "repo"})
	if err != nil {
		t.Fatal(err)
	}
	entry := memory.Entry{Title: "Tiered memory", Scope: memory.ScopeProject, Verdict: memory.VerdictGood, Summary: "Keep this fact.", Why: "It is useful."}
	result, err := store.Save(context.Background(), entry)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PromoteToCore(context.Background(), result.ID); err != nil {
		t.Fatal(err)
	}
	core, err := store.CoreEntries(context.Background(), memory.ScopeProject)
	if err != nil || len(core) != 1 || core[0].ID != result.ID {
		t.Fatalf("core=%+v err=%v, want promoted entry", core, err)
	}
	if err := store.Delete(context.Background(), result.ID); err != nil {
		t.Fatal(err)
	}
	if count, err := store.Count(context.Background(), memory.ScopeProject); err != nil || count != 0 {
		t.Fatalf("count=%d err=%v, want zero after delete", count, err)
	}
}

func TestMarkdownStoreSerializesConcurrentSaves(t *testing.T) {
	root, indexPath := t.TempDir(), filepath.Join(t.TempDir(), "context.db")
	source, err := memory.NewMarkdownSource(root, filepath.Join(t.TempDir(), "org"), "acme")
	if err != nil {
		t.Fatal(err)
	}
	index, err := storage.OpenSQLite(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	store, err := OpenMarkdownStore(context.Background(), MarkdownStoreConfig{Source: source, Index: index, ProjectID: "repo"})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = store.Save(context.Background(), memory.Entry{Title: fmt.Sprintf("Concurrent %d", i), Scope: memory.ScopeProject, Verdict: memory.VerdictGood, Summary: "A concurrent fact.", Why: "The store serializes writes."})
		}(i)
	}
	wg.Wait()
	if count, err := store.Count(context.Background(), memory.ScopeProject); err != nil || count != 8 {
		t.Fatalf("count=%d err=%v, want eight concurrent saves", count, err)
	}
}
