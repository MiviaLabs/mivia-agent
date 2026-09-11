package memory

import (
	"context"
	"testing"
)

func TestEntrySimilarityNearDuplicate(t *testing.T) {
	a := Entry{
		Title:   "Commit format: fix commits need Regression/Class/Sweep trailers",
		Summary: "fix commits in this repo are rejected by the commit-msg hook unless the body carries three trailers: Regression, Class, and Sweep.",
	}
	b := Entry{
		Title:   "fix(scope) commits in mivia-agent require Regression/Class/Sweep trailers",
		Summary: "In mivia-agent, fix commits are rejected by the commit-msg hook unless the body carries three trailers: Regression, Class, and Sweep.",
	}
	sim := EntrySimilarity(a, b)
	if sim < similarityMergeThreshold {
		t.Fatalf("EntrySimilarity = %.3f, want >= %.2f", sim, similarityMergeThreshold)
	}

	distinct := Entry{
		Title:   "Avoid blocking subagent dispatches",
		Summary: "Never use wait:run for multi-agent dispatches.",
	}
	if sim2 := EntrySimilarity(a, distinct); sim2 >= similarityMergeThreshold {
		t.Fatalf("EntrySimilarity for distinct = %.3f, want < %.2f", sim2, similarityMergeThreshold)
	}
}

func TestMergeEntries(t *testing.T) {
	existing := Entry{
		Title:      "Original Title",
		Summary:    "Original summary.",
		Scope:      ScopeProject,
		Created:    "2026-09-01",
		Importance: ImportanceMedium,
		Verdict:    VerdictGood,
		Tags:       []string{"git", "hooks"},
		References: []string{"AGENTS.md"},
		Why:        "Original why.",
	}
	incoming := Entry{
		Title:      "Incoming Title",
		Summary:    "Updated summary with more details.",
		Scope:      ScopeProject,
		Created:    "2026-09-11",
		Importance: ImportanceHigh,
		Verdict:    VerdictMixed,
		Tags:       []string{"hooks", "commits"},
		References: []string{"AGENTS.md", "scripts/check.py"},
		Why:        "Updated why.",
		Good:       "Good stuff.",
	}

	merged := MergeEntries(existing, incoming)

	if merged.Title != existing.Title {
		t.Errorf("Title = %q, want %q", merged.Title, existing.Title)
	}
	if merged.Created != existing.Created {
		t.Errorf("Created = %q, want %q", merged.Created, existing.Created)
	}
	if merged.Importance != ImportanceHigh {
		t.Errorf("Importance = %q, want high", merged.Importance)
	}
	if len(merged.Tags) != 3 { // git, hooks, commits
		t.Errorf("Tags = %v, want 3 tags", merged.Tags)
	}
	if len(merged.References) != 2 { // AGENTS.md, scripts/check.py
		t.Errorf("References = %v, want 2 references", merged.References)
	}
	if merged.Good != "Good stuff." {
		t.Errorf("Good = %q, want Good stuff.", merged.Good)
	}
}

func TestMarkdownSourceSaveNearDuplicateMergesExisting(t *testing.T) {
	root := t.TempDir()
	source, err := NewMarkdownSource(root, "", "")
	if err != nil {
		t.Fatal(err)
	}

	e1 := Entry{
		Title:      "deploy pipeline fix pinned runner image",
		Summary:    "deploy pipeline fix pinned runner image in github actions",
		Why:        "runner image broke pipeline",
		Scope:      ScopeProject,
		Importance: ImportanceMedium,
		Verdict:    VerdictGood,
		Tags:       []string{"ci", "deploy"},
	}
	doc1, err := source.Save(context.Background(), e1)
	if err != nil {
		t.Fatal(err)
	}

	e2 := Entry{
		Title:      "deploy pipeline fix pinned the runner image",
		Summary:    "deploy pipeline fix pinned the runner image in github actions",
		Why:        "runner image was pinned to fix the deploy pipeline",
		Scope:      ScopeProject,
		Importance: ImportanceHigh,
		Verdict:    VerdictGood,
		Tags:       []string{"ci", "github"},
	}
	doc2, err := source.Save(context.Background(), e2)
	if err != nil {
		t.Fatal(err)
	}

	// Must have updated the same file rather than creating a second file
	if doc2.ID != doc1.ID {
		t.Fatalf("doc2.ID = %q, want %q", doc2.ID, doc1.ID)
	}
	if doc2.Path != doc1.Path {
		t.Fatalf("doc2.Path = %q, want %q", doc2.Path, doc1.Path)
	}

	// Scan must only return 1 document
	docs, err := source.Scan(context.Background(), ScopeProject)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 {
		t.Fatalf("Scan returned %d docs, want 1", len(docs))
	}
	if docs[0].Entry.Importance != ImportanceHigh {
		t.Errorf("Importance = %v, want high", docs[0].Entry.Importance)
	}
}
