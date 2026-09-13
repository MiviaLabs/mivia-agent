package cliagents

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/memory"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// memory_save must report the entry that is actually stored. When the save
// merges into a near-duplicate, the stored entry keeps the existing one's
// Title/Verdict/Tags; reporting the incoming (discarded) metadata would point
// later searches and deletes at a differently-titled memory.
func TestMarkdownStoreSaveReportsMergedEntryMetadata(t *testing.T) {
	root, indexPath := t.TempDir(), filepath.Join(t.TempDir(), "context.db")
	index, err := storage.OpenSQLite(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	source, err := memory.NewMarkdownSource(root, filepath.Join(t.TempDir(), "org"), "acme")
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenMarkdownStore(context.Background(), MarkdownStoreConfig{Source: source, Index: index, ProjectID: "repo"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(context.Background(), memory.Entry{
		Title:   "pinned runner image",
		Summary: "the runner image is pinned by digest in every workflow",
		Why:     "the unpinned tag drifted and broke the release build",
		Verdict: memory.VerdictGood,
		Scope:   memory.ScopeProject,
	}); err != nil {
		t.Fatal(err)
	}
	res, err := store.Save(context.Background(), memory.Entry{
		Title:   "pinned the runner image",
		Summary: "the runner image is pinned by digest in every workflow file",
		Why:     "the unpinned tag drifted and broke the release build again",
		Verdict: memory.VerdictGood,
		Scope:   memory.ScopeProject,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Title != "pinned runner image" {
		t.Fatalf("Result.Title = %q, want the stored existing title %q", res.Title, "pinned runner image")
	}
	if res.Verdict != memory.VerdictGood {
		t.Fatalf("Result.Verdict = %q, want %q", res.Verdict, memory.VerdictGood)
	}
	// And the stored file must carry the existing title, proving the merge
	// happened rather than the result merely echoing an unmerged save.
	matches, _ := filepath.Glob(filepath.Join(root, ".agents", "memories", "pinned-runner-image*.md"))
	if len(matches) != 1 {
		t.Fatalf("expected one merged file under the existing slug, got %v", matches)
	}
}
