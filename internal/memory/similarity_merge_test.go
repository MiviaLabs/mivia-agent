package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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

	merged, _ := MergeEntries(existing, incoming)

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

	merged, trunc := MergeEntries(existing, incoming)

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
	// Both collections exceeded limit and must be reported in trunc
	if !slices.Contains(trunc, "tags") {
		t.Errorf("trunc = %v, want to contain tags", trunc)
	}
	if !slices.Contains(trunc, "references") {
		t.Errorf("trunc = %v, want to contain references", trunc)
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

func TestMergeSimilarDerivesIDFromFilenameForGateCompliance(t *testing.T) {
	root := t.TempDir()
	source, err := NewMarkdownSource(root, "", "")
	if err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(root, ".agents", "memories")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Legacy or hand-authored file whose name is runner-image-notes.md
	// with a custom frontmatter ID "custom_id" that doesn't match the filename derivation.
	legacyContent := `---
id: custom_id
title: 'Runner image notes'
content: 'Runner image notes for pipeline.'
importance: medium
tags: [ci]
updated: 2026-09-01
---

# Runner image notes

## Summary
Runner image notes for pipeline.

## Why
Initial why.
`
	filePath := filepath.Join(dir, "runner-image-notes.md")
	if err := os.WriteFile(filePath, []byte(legacyContent), 0o600); err != nil {
		t.Fatal(err)
	}

	incoming := Entry{
		Title:      "Runner image notes updated",
		Summary:    "Runner image notes for pipeline update.",
		Why:        "Updated why.",
		Scope:      ScopeProject,
		Importance: ImportanceHigh,
		Verdict:    VerdictGood,
		Tags:       []string{"ci"},
	}

	doc, err := source.Save(context.Background(), incoming)
	if err != nil {
		t.Fatal(err)
	}

	// The protocol ID written must match the filename derivation: runner_image_notes
	if doc.ID != "runner_image_notes" {
		t.Fatalf("doc.ID = %q, want runner_image_notes", doc.ID)
	}

	// The on-disk file must also have id: runner_image_notes
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "id: runner_image_notes") {
		t.Fatalf("file content does not have derived id: %s", string(data))
	}
}

func TestClampWithReportNeverEmptiesNonEmptyWhy(t *testing.T) {
	whyLeadingHeading := "\n## " + strings.Repeat("x", 1500)
	clamped, truncated := Entry{Title: "t", Summary: "s", Why: whyLeadingHeading}.ClampWithReport()
	if strings.TrimSpace(clamped.Why) == "" {
		t.Fatal("ClampWithReport emptied a non-empty Why field")
	}
	if len(truncated) == 0 || truncated[0] != "why" {
		t.Fatalf("truncated = %v, want [why]", truncated)
	}
}

// TestSaveDistinctHighJaccardCollisionStaysSeparate reproduces the
// login/signup-style collision that motivated EntriesMergeable: two short,
// distinct memories about different features share nearly every
// implementation word (form, submit, spinner, redirect, validation) and
// clear the Jaccard merge threshold on vocabulary alone, but they record
// opposite outcomes about two different specific bugs. Entry has no
// identity field, so Verdict is the signal that must keep them apart:
// saving both through the real MarkdownSource.Save path must leave two
// files on disk, not silently collapse the signup fix report into the
// login bug report.
func TestSaveDistinctHighJaccardCollisionStaysSeparate(t *testing.T) {
	root := t.TempDir()
	source, err := NewMarkdownSource(root, "", "")
	if err != nil {
		t.Fatal(err)
	}

	loginBug := Entry{
		Title:      "Login form submit spinner never stops",
		Summary:    "Login form submit button spinner keeps spinning after failed login validation redirect",
		Why:        "The login form's submit handler never clears its loading state on a failed login redirect.",
		Scope:      ScopeProject,
		Importance: ImportanceMedium,
		Verdict:    VerdictBad,
		Tags:       []string{"login"},
	}
	signupBug := Entry{
		Title:      "Signup form submit spinner never stops",
		Summary:    "Signup form submit button spinner keeps spinning after failed signup validation redirect",
		Why:        "The signup form's submit handler was fixed to clear its loading state on a failed signup redirect.",
		Scope:      ScopeProject,
		Importance: ImportanceMedium,
		Verdict:    VerdictGood,
		Tags:       []string{"signup"},
	}

	// Confirm the fixture actually reproduces a high-Jaccard collision -
	// otherwise this test would pass for the wrong reason (no collision to
	// guard against).
	if sim := EntrySimilarity(loginBug, signupBug); sim < similarityMergeThreshold {
		t.Fatalf("fixture similarity = %.3f, want >= %.2f (test must reproduce a high-Jaccard collision)", sim, similarityMergeThreshold)
	}

	doc1, err := source.Save(context.Background(), loginBug)
	if err != nil {
		t.Fatal(err)
	}
	doc2, err := source.Save(context.Background(), signupBug)
	if err != nil {
		t.Fatal(err)
	}

	if doc2.ID == doc1.ID || doc2.Path == doc1.Path {
		t.Fatalf("distinct login/signup bug reports were merged into one entry: doc1=%+v doc2=%+v", doc1, doc2)
	}

	docs, err := source.Scan(context.Background(), ScopeProject)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 {
		t.Fatalf("Scan returned %d docs, want 2 (login and signup bug reports kept separate)", len(docs))
	}
}

// TestSaveTrueDuplicateMergesIntoOne is the merge-side counterpart of
// TestSaveDistinctHighJaccardCollisionStaysSeparate: a genuine resave of the
// same finding (near-identical wording, same verdict) must still merge into
// one file. EntriesMergeable's Verdict check must not turn into a blanket
// "never merge" - only a mismatched verdict on a high-similarity pair blocks
// the merge.
func TestSaveTrueDuplicateMergesIntoOne(t *testing.T) {
	root := t.TempDir()
	source, err := NewMarkdownSource(root, "", "")
	if err != nil {
		t.Fatal(err)
	}

	first := Entry{
		Title:      "deploy pipeline fix pinned runner image",
		Summary:    "deploy pipeline fix pinned runner image in github actions",
		Why:        "runner image broke pipeline",
		Scope:      ScopeProject,
		Importance: ImportanceMedium,
		Verdict:    VerdictGood,
		Tags:       []string{"ci"},
	}
	resave := Entry{
		Title:      "deploy pipeline fix pinned the runner image",
		Summary:    "deploy pipeline fix pinned the runner image in github actions",
		Why:        "runner image was pinned to fix the deploy pipeline",
		Scope:      ScopeProject,
		Importance: ImportanceHigh,
		Verdict:    VerdictGood,
		Tags:       []string{"deploy"},
	}

	doc1, err := source.Save(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	doc2, err := source.Save(context.Background(), resave)
	if err != nil {
		t.Fatal(err)
	}
	if doc2.ID != doc1.ID || doc2.Path != doc1.Path {
		t.Fatalf("same-verdict near-duplicate resave was not merged: doc1=%+v doc2=%+v", doc1, doc2)
	}

	docs, err := source.Scan(context.Background(), ScopeProject)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 {
		t.Fatalf("Scan returned %d docs, want 1 (true duplicate merged)", len(docs))
	}
}

// TestMergeEntriesPreservesDurableFieldsWhenIncomingOmitsThem checks the
// preservation half of a confirmed merge (gated by EntriesMergeable, as
// every caller is expected to do): fields the incoming resave does not
// carry - here References and the What worked/What did not work body -
// must survive from the existing entry rather than being wiped, while
// fields the incoming resave does carry (Importance, Created, Why) win.
func TestMergeEntriesPreservesDurableFieldsWhenIncomingOmitsThem(t *testing.T) {
	existing := Entry{
		Title:      "deploy pipeline fix pinned runner image",
		Summary:    "deploy pipeline fix pinned runner image in github actions",
		Good:       "Pinned runner image tag to v2.",
		Bad:        "Initial floating tag caused flaky builds.",
		References: []string{"docs/ci.md"},
		Scope:      ScopeProject,
		Importance: ImportanceMedium,
		Verdict:    VerdictGood,
		Created:    "2026-09-01",
		Why:        "Floating runner image tags drift silently.",
	}
	incoming := Entry{
		Title:      "deploy pipeline fix pinned the runner image",
		Summary:    "deploy pipeline fix pinned the runner image in github actions, now documented",
		Scope:      ScopeProject,
		Importance: ImportanceHigh,
		Verdict:    VerdictGood,
		Created:    "2026-09-12",
		Why:        "Documented the pin in the runbook.",
	}

	if !EntriesMergeable(existing, incoming) {
		t.Fatalf("fixture must be mergeable: EntriesMergeable(existing, incoming) = false")
	}
	merged, _ := MergeEntries(existing, incoming)

	if merged.Title != existing.Title {
		t.Errorf("Title = %q, want preserved existing title %q", merged.Title, existing.Title)
	}
	if merged.Scope != existing.Scope {
		t.Errorf("Scope = %q, want preserved existing scope %q", merged.Scope, existing.Scope)
	}
	if merged.Good != existing.Good {
		t.Errorf("Good = %q, want preserved existing %q (incoming carried none)", merged.Good, existing.Good)
	}
	if merged.Bad != existing.Bad {
		t.Errorf("Bad = %q, want preserved existing %q (incoming carried none)", merged.Bad, existing.Bad)
	}
	if !equalStrings(merged.References, existing.References) {
		t.Errorf("References = %v, want preserved existing %v (incoming carried none)", merged.References, existing.References)
	}
	if merged.Importance != ImportanceHigh {
		t.Errorf("Importance = %q, want incoming's higher importance high", merged.Importance)
	}
	if merged.Created != "2026-09-12" {
		t.Errorf("Created = %q, want bumped to incoming's date", merged.Created)
	}
	if merged.Why != incoming.Why+"\n\n## Prior context\n"+existing.Why {
		t.Errorf("Why = %q, want incoming's why with existing preserved as prior context", merged.Why)
	}
}

func TestMergeSimilarFallsThroughOnValidateFailure(t *testing.T) {
	root := t.TempDir()
	source, err := NewMarkdownSource(root, "", "")
	if err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(root, ".agents", "memories")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Legacy file with an invalid tag character that passes unvalidated Scan but fails Validate
	legacyContent := `---
id: legacy_bad_tag
title: 'Deploy pipeline invalid tag'
content: 'Deploy pipeline invalid tag in CI.'
importance: medium
tags: [ci:invalid]
updated: 2026-09-01
---

# Deploy pipeline invalid tag

## Summary
Deploy pipeline invalid tag in CI.

## Why
Some why.
`
	filePath := filepath.Join(dir, "legacy-bad-tag.md")
	if err := os.WriteFile(filePath, []byte(legacyContent), 0o600); err != nil {
		t.Fatal(err)
	}

	incoming := Entry{
		Title:      "Deploy pipeline invalid tag v2",
		Summary:    "Deploy pipeline invalid tag in CI workflows.",
		Why:        "Valid why.",
		Scope:      ScopeProject,
		Importance: ImportanceHigh,
		Verdict:    VerdictGood,
		Tags:       []string{"valid"},
	}

	// Save must succeed by falling through to write a new valid file, rather than failing
	doc, err := source.Save(context.Background(), incoming)
	if err != nil {
		t.Fatalf("Save failed on near-duplicate with legacy invalid entry: %v", err)
	}
	if doc.ID == "legacy_bad_tag" {
		t.Fatalf("doc.ID = %q, should have written a new file", doc.ID)
	}
}

func mostSimilarCandidates(t *testing.T) (e1, e2, incoming Entry) {
	t.Helper()
	e1 = Entry{
		Title:      "alpha deploy pipeline runner image caching failed",
		Summary:    "github actions workflow timeout alpha kubernetes pod",
		Why:        "runner image caching failed in pod",
		Scope:      ScopeProject,
		Importance: ImportanceMedium,
		Verdict:    VerdictGood,
	}
	e2 = Entry{
		Title:      "deploy pipeline runner image caching failed",
		Summary:    "github actions workflow timeout docker registry network cluster",
		Why:        "runner image caching failed in registry",
		Scope:      ScopeProject,
		Importance: ImportanceMedium,
		Verdict:    VerdictGood,
	}
	incoming = Entry{
		Title:      "deploy pipeline runner image caching failed",
		Summary:    "github actions workflow timeout docker registry",
		Why:        "incoming richer context",
		Scope:      ScopeProject,
		Importance: ImportanceHigh,
		Verdict:    VerdictGood,
	}

	// Sanity check similarities:
	incoming = Entry{
		Title:      "deploy pipeline runner image caching failed",
		Summary:    "github actions workflow timeout docker registry",
		Why:        "incoming richer context",
		Scope:      ScopeProject,
		Importance: ImportanceHigh,
		Verdict:    VerdictGood,
	}
	// e1 and e2 must NOT be mergeable with each other, so both exist as candidates.
	sim12 := EntrySimilarity(e1, e2)
	if EntriesMergeable(e1, e2) {
		t.Fatalf("precondition failed: e1 and e2 must not be mergeable (sim=%.3f)", sim12)
	}
	// Both e1 and e2 must be mergeable with incoming.
	if !EntriesMergeable(e1, incoming) {
		t.Fatalf("precondition failed: e1 and incoming must be mergeable (sim=%.3f)", EntrySimilarity(e1, incoming))
	}
	if !EntriesMergeable(e2, incoming) {
		t.Fatalf("precondition failed: e2 and incoming must be mergeable (sim=%.3f)", EntrySimilarity(e2, incoming))
	}
	// incoming must be strictly closer to e2 than e1.
	sim1 := EntrySimilarity(e1, incoming)
	sim2 := EntrySimilarity(e2, incoming)
	if sim2 <= sim1 {
		t.Fatalf("precondition failed: sim2 (%.3f) must be > sim1 (%.3f)", sim2, sim1)
	}
	return
}

func TestSaveMergesIntoMostSimilarCandidate(t *testing.T) {
	e1, e2, incoming := mostSimilarCandidates(t)

	t.Run("MarkdownSource", func(t *testing.T) {
		root := t.TempDir()
		source, err := NewMarkdownSource(root, "", "")
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		doc1, err := source.Save(ctx, e1)
		if err != nil {
			t.Fatal(err)
		}
		doc2, err := source.Save(ctx, e2)
		if err != nil {
			t.Fatal(err)
		}
		if doc1.ID == doc2.ID {
			t.Fatalf("doc1 and doc2 must have distinct IDs: %q == %q", doc1.ID, doc2.ID)
		}

		doc3, err := source.Save(ctx, incoming)
		if err != nil {
			t.Fatal(err)
		}
		if doc3.ID != doc2.ID {
			t.Fatalf("MarkdownSource.Save merged into ID %q (want %q, more similar candidate)", doc3.ID, doc2.ID)
		}
		if doc3.Path != doc2.Path {
			t.Fatalf("MarkdownSource.Save merged into path %q, want %q", doc3.Path, doc2.Path)
		}

		docs, err := source.Scan(ctx, ScopeProject)
		if err != nil {
			t.Fatal(err)
		}
		if len(docs) != 2 {
			t.Fatalf("Scan returned %d docs, want 2", len(docs))
		}
	})

	t.Run("memStore", func(t *testing.T) {
		store := newTestStore(t, "memory", "")
		ctx := context.Background()

		res1, err := store.Save(ctx, e1)
		if err != nil {
			t.Fatal(err)
		}
		res2, err := store.Save(ctx, e2)
		if err != nil {
			t.Fatal(err)
		}
		if res1.ID == res2.ID {
			t.Fatalf("res1 and res2 must have distinct IDs: %q == %q", res1.ID, res2.ID)
		}

		res3, err := store.Save(ctx, incoming)
		if err != nil {
			t.Fatal(err)
		}
		if res3.ID != res2.ID {
			t.Fatalf("memStore.Save merged into ID %q (want %q, more similar candidate)", res3.ID, res2.ID)
		}

		count, err := store.Count(ctx, ScopeProject)
		if err != nil {
			t.Fatal(err)
		}
		if count != 2 {
			t.Fatalf("Count returned %d, want 2", count)
		}
	})
}

func TestStoreSaveMergeReportsWhyTruncation(t *testing.T) {
	ctx := context.Background()

	t.Run("memStore", func(t *testing.T) {
		store := newTestStore(t, "memory", "")
		e1 := Entry{
			Title:   "deploy pipeline runner image caching failed",
			Summary: "github actions workflow timeout docker registry",
			Why:     strings.Repeat("a", 900),
			Scope:   ScopeProject,
			Verdict: VerdictGood,
		}
		if _, err := store.Save(ctx, e1); err != nil {
			t.Fatal(err)
		}

		e2 := Entry{
			Title:   "deploy pipeline runner image caching failed",
			Summary: "github actions workflow timeout docker registry mirror",
			Why:     strings.Repeat("b", 900),
			Scope:   ScopeProject,
			Verdict: VerdictGood,
		}
		res, err := store.Save(ctx, e2)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(res.Truncated, "why") {
			t.Fatalf("res.Truncated = %v, want to contain %q", res.Truncated, "why")
		}
	})

	t.Run("MarkdownSource", func(t *testing.T) {
		root := t.TempDir()
		source, err := NewMarkdownSource(root, "", "")
		if err != nil {
			t.Fatal(err)
		}
		e1 := Entry{
			Title:   "deploy pipeline runner image caching failed",
			Summary: "github actions workflow timeout docker registry",
			Why:     strings.Repeat("a", 900),
			Scope:   ScopeProject,
			Verdict: VerdictGood,
		}
		if _, err := source.Save(ctx, e1); err != nil {
			t.Fatal(err)
		}

		e2 := Entry{
			Title:   "deploy pipeline runner image caching failed",
			Summary: "github actions workflow timeout docker registry mirror",
			Why:     strings.Repeat("b", 900),
			Scope:   ScopeProject,
			Verdict: VerdictGood,
		}
		doc, err := source.Save(ctx, e2)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(doc.Truncated, "why") {
			t.Fatalf("doc.Truncated = %v, want to contain %q", doc.Truncated, "why")
		}
	})
}

// mergeTruncationBaseEntry is the canonical record for the collection-cap
// merge tests: every collection sits exactly at its limit (8 tags, 8
// references, 16 related), so any incoming-only item must be cut.
func mergeTruncationBaseEntry() Entry {
	return Entry{
		Title:      "deploy pipeline runner image caching failed",
		Summary:    "github actions workflow timeout docker registry",
		Why:        "runner image caching failed in workflow",
		Tags:       []string{"t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8"},
		References: []string{"r1", "r2", "r3", "r4", "r5", "r6", "r7", "r8"},
		Related:    []string{"rel01", "rel02", "rel03", "rel04", "rel05", "rel06", "rel07", "rel08", "rel09", "rel10", "rel11", "rel12", "rel13", "rel14", "rel15", "rel16"},
		Scope:      ScopeProject,
		Verdict:    VerdictGood,
	}
}

// mergeTruncationIncomingEntry is a near-duplicate of the base entry that
// carries one already-present and one NEW item per collection, so the new
// ones can only land by displacing something.
func mergeTruncationIncomingEntry() Entry {
	return Entry{
		Title:      "deploy pipeline runner image caching failed",
		Summary:    "github actions workflow timeout docker registry mirror",
		Why:        "runner image caching failed in workflow with registry mirror",
		Tags:       []string{"t1", "t9"},
		References: []string{"r1", "r9"},
		Related:    []string{"rel01", "rel17"},
		Scope:      ScopeProject,
		Verdict:    VerdictGood,
	}
}

// assertCollectionsTruncated pins the reporting contract: every collection
// whose cap cut an incoming item names itself in the caller's truncation
// slice, and existing-first ordering leaves the canonical record intact.
func assertCollectionsTruncated(t *testing.T, truncated []string, tags []string) {
	t.Helper()
	for _, name := range []string{"tags", "references", "related"} {
		if !slices.Contains(truncated, name) {
			t.Fatalf("truncated = %v, want to contain %q", truncated, name)
		}
	}
	wantTags := []string{"t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8"}
	if !slices.Equal(tags, wantTags) {
		t.Fatalf("tags = %v, want %v (existing-first preserved)", tags, wantTags)
	}
}

func seedMergeTruncationRelatedFixtures(t *testing.T, root string, targetID string) {
	t.Helper()
	memDir := filepath.Join(root, ".agents", "memories")
	if err := os.MkdirAll(memDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 16; i++ {
		id := fmt.Sprintf("rel%02d", i)
		content := fmt.Sprintf(`---
id: %s
title: 'Related memory %02d'
content: 'Summary for related memory %02d.'
importance: medium
verdict: good
related: [%s]
updated: 2026-09-01
---

# Related memory %02d

## Summary
Summary for related memory %02d.

## Why
Context for related memory %02d.
`, id, i, i, targetID, i, i, i)
		if err := os.WriteFile(filepath.Join(memDir, id+".md"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// rel17 is a valid target memory that has no outgoing links, allowing the incoming
	// entry to reference it without violating reciprocity before or after merge truncation.
	content17 := `---
id: rel17
title: 'Related memory 17'
content: 'Summary for related memory 17.'
importance: medium
verdict: good
updated: 2026-09-01
---

# Related memory 17

## Summary
Summary for related memory 17.

## Why
Context for related memory 17.
`
	if err := os.WriteFile(filepath.Join(memDir, "rel17.md"), []byte(content17), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestStoreSaveMergeReportsCollectionTruncation(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, "memory", "")
	if _, err := store.Save(ctx, mergeTruncationBaseEntry()); err != nil {
		t.Fatal(err)
	}
	res, err := store.Save(ctx, mergeTruncationIncomingEntry())
	if err != nil {
		t.Fatal(err)
	}
	assertCollectionsTruncated(t, res.Truncated, res.Tags)
}

func TestMarkdownSourceMergeReportsCollectionTruncation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	base := mergeTruncationBaseEntry()
	base.Created = "2026-09-01"
	baseStem := slug(base.Title) + "-" + entryID(base.Scope, "", base.Title, base.Render())
	baseProtocolID := strings.ReplaceAll(baseStem, "-", "_")

	seedMergeTruncationRelatedFixtures(t, root, baseProtocolID)

	source, err := NewMarkdownSource(root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Save(ctx, base); err != nil {
		t.Fatal(err)
	}
	incoming := mergeTruncationIncomingEntry()
	incoming.Created = "2026-09-02"
	doc, err := source.Save(ctx, incoming)
	if err != nil {
		t.Fatal(err)
	}
	assertCollectionsTruncated(t, doc.Truncated, doc.Entry.Tags)
}

// A merge that drops only items already present loses no information, so it
// must report nothing - a truncation note there would be a false alarm.
func TestStoreSaveMergeNoTruncationWhenDuplicateOnly(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, "memory", "")
	base := mergeTruncationBaseEntry()
	base.References = nil
	base.Related = nil
	if _, err := store.Save(ctx, base); err != nil {
		t.Fatal(err)
	}

	incoming := mergeTruncationIncomingEntry()
	incoming.Tags = []string{"t1", "t2"}
	incoming.References = nil
	incoming.Related = nil
	res, err := store.Save(ctx, incoming)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Truncated) != 0 {
		t.Fatalf("res.Truncated = %v, want empty when incoming tags are duplicates", res.Truncated)
	}
}
