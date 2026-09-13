package memory

import (
	"context"
	"strings"
	"testing"
)

// A configured MaxEntryBytes must bound the MERGED entry the merge path
// validates, not only the incoming one: MergeEntries concatenates the existing
// Why under "## Prior context", which can push the merged entry past the cap
// even when each side alone is within it. mergeSimilar must then skip the
// merge (its documented fallback) instead of persisting the oversized entry.
func TestMarkdownSourceSaveMergeRespectsConfiguredMaxEntryBytes(t *testing.T) {
	root := t.TempDir()
	source, err := NewMarkdownSource(root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	const cap = 600
	limited := source.WithLimits(Limits{MaxEntryBytes: cap})
	ctx := context.Background()
	first := Entry{
		Title:   "pinned runner image",
		Summary: strings.Repeat("the runner image is pinned ", 8),
		Why:     strings.Repeat("drift found during audit ", 6),
		Verdict: VerdictBad,
		Scope:   ScopeProject,
	}
	if _, err := limited.Save(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := Entry{
		Title:   "pinned the runner image",
		Summary: strings.Repeat("the runner image is pinned ", 8),
		Why:     strings.Repeat("drift found again during followup ", 6),
		Verdict: VerdictBad,
		Scope:   ScopeProject,
	}
	// Sanity: the merged entry really would exceed the cap, so this test
	// exercises the merged-validation path.
	if m, _ := MergeEntries(first, second); len(m.Clamp().Render()) <= cap {
		t.Fatalf("precondition: merged render is %d bytes, want > %d", len(m.Clamp().Render()), cap)
	}
	if _, err := limited.Save(ctx, second); err != nil {
		t.Fatal(err)
	}
	docs, err := limited.Scan(ctx, ScopeProject)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 {
		t.Fatalf("stored %d memories, want 2 (oversized merge must be skipped)", len(docs))
	}
	for _, doc := range docs {
		if strings.Contains(doc.Entry.Why, "## Prior context") {
			t.Fatalf("memory %q was persisted as an oversized merge", doc.Entry.Title)
		}
	}
}
