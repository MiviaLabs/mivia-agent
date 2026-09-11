package memory

import (
	"context"
	"strings"
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
	if merged.Created != incoming.Created {
		t.Errorf("Created = %q, want %q", merged.Created, incoming.Created)
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

func TestMergeEntriesCapsAndBumpsDate(t *testing.T) {
	existing := Entry{
		Title:      "Deploy pipeline fix",
		Summary:    "Existing summary.",
		Why:        "Original why.",
		Created:    "2026-09-01",
		Tags:       []string{"t1", "t2", "t3", "t4", "t5", "t6"},
		References: []string{"r1", "r2", "r3", "r4", "r5", "r6"},
		Related:    []string{"rel1", "rel2"},
		Verdict:    VerdictGood,
	}
	incoming := Entry{
		Title:      "Deploy pipeline fix updated",
		Summary:    "Incoming summary.",
		Why:        "New different why.",
		Created:    "2026-09-11",
		Tags:       []string{"t5", "t6", "t7", "t8", "t9", "t10"},
		References: []string{"r5", "r6", "r7", "r8", "r9", "r10"},
		Related:    []string{"rel2", "rel3"},
		Verdict:    VerdictGood,
	}

	merged := MergeEntries(existing, incoming)

	// Created must be bumped to incoming date
	if merged.Created != "2026-09-11" {
		t.Errorf("Created = %q, want 2026-09-11", merged.Created)
	}
	// Tags must be capped at maxTags (8)
	if len(merged.Tags) != 8 {
		t.Errorf("Tags len = %d, want maxTags 8", len(merged.Tags))
	}
	// References must be capped at maxReferences (8)
	if len(merged.References) != 8 {
		t.Errorf("References len = %d, want maxReferences 8", len(merged.References))
	}
	// Why must preserve original why as context
	if !strings.Contains(merged.Why, "New different why.") || !strings.Contains(merged.Why, "Original why.") {
		t.Errorf("Why missing original or new text: %q", merged.Why)
	}
}

func TestValidateRejectsInvalidRelated(t *testing.T) {
	e := Entry{
		Title:      "Valid title",
		Summary:    "Valid summary",
		Why:        "Valid why",
		Verdict:    VerdictGood,
		Importance: ImportanceMedium,
		Tags:       []string{"valid"},
		Related:    []string{"valid_id", "invalid:id"},
	}
	if err := e.Validate(Limits{}); err == nil {
		t.Fatal("Validate accepted related ID containing colon")
	}
}

func TestConsecutiveNearDuplicateSavesPreservesStructure(t *testing.T) {
	root := t.TempDir()
	source, err := NewMarkdownSource(root, "", "")
	if err != nil {
		t.Fatal(err)
	}

	// 1. Initial save with rich structure
	e1 := Entry{
		Title:      "Deploy pipeline fix runner image",
		Summary:    "Deploy pipeline fix runner image in CI.",
		Good:       "Build speed improved.",
		Bad:        "Cached layers were invalidated.",
		Why:        "Runner image update was needed.",
		References: []string{"docs/ci.md"},
		Scope:      ScopeProject,
		Importance: ImportanceMedium,
		Verdict:    VerdictGood,
		Tags:       []string{"ci", "deploy"},
	}
	doc1, err := source.Save(context.Background(), e1)
	if err != nil {
		t.Fatal(err)
	}

	// 2. First near-duplicate save (triggers MergeEntries -> writes ## Prior context)
	e2 := Entry{
		Title:      "Deploy pipeline fix runner image v2",
		Summary:    "Deploy pipeline fix runner image in CI workflows.",
		Why:        "Runner image needed another tweak.",
		Scope:      ScopeProject,
		Importance: ImportanceHigh,
		Verdict:    VerdictGood,
		Tags:       []string{"ci", "workflows"},
	}
	if _, err := source.Save(context.Background(), e2); err != nil {
		t.Fatal(err)
	}

	// 3. Scan after first merge - must retain Good, Bad, References structurally
	docs, err := source.Scan(context.Background(), ScopeProject)
	if err != nil || len(docs) != 1 || docs[0].Entry.Good != e1.Good || docs[0].Entry.Bad != e1.Bad {
		t.Fatalf("First merge Scan failed: %+v (err=%v)", docs, err)
	}

	// 4. Second near-duplicate save
	e3 := Entry{
		Title:      "Deploy pipeline fix runner image v3",
		Summary:    "Deploy pipeline fix runner image in CI workflows update.",
		Why:        "Runner image needed final adjustment.",
		Scope:      ScopeProject,
		Importance: ImportanceHigh,
		Verdict:    VerdictGood,
		Tags:       []string{"ci"},
	}
	doc3, err := source.Save(context.Background(), e3)
	if err != nil || doc3.ID != doc1.ID {
		t.Fatalf("Save 3 failed: id=%q want %q, err=%v", doc3.ID, doc1.ID, err)
	}

	// 5. Final Scan: Good and Bad must still be preserved
	docs, err = source.Scan(context.Background(), ScopeProject)
	if err != nil || len(docs) != 1 || docs[0].Entry.Good != e1.Good || docs[0].Entry.Bad != e1.Bad {
		t.Fatalf("Final Scan lost structure: %+v (err=%v)", docs, err)
	}
}
