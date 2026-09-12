package memory

import (
	"context"
	"strings"
	"testing"
)

// TestMergeSimilarReportsValidationFailure pins the discarded-error fix in
// mergeSimilar: when the MERGED entry fails Validate (here, a configured
// BlockPatterns rule matching MergeEntries' own "## Prior context" marker,
// which only appears in a merged Why - never in either side alone), Save
// must still fall through and write a new file (the documented merge/dedup
// fallback), but the Validate error it discards internally must now reach
// the caller as a reported warning instead of vanishing.
func TestMergeSimilarReportsValidationFailure(t *testing.T) {
	root := t.TempDir()
	source, err := NewMarkdownSource(root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	limited := source.WithLimits(Limits{BlockPatterns: []string{"## Prior context"}})
	ctx := context.Background()

	first := Entry{
		Title:   "runner image pinned to a digest",
		Summary: "the CI runner image is pinned to a sha256 digest, not a floating tag",
		Why:     "a floating tag let an upstream base image change silently underneath the pipeline",
		Verdict: VerdictBad,
		Scope:   ScopeProject,
	}
	if _, err := limited.Save(ctx, first); err != nil {
		t.Fatalf("save first: %v", err)
	}

	second := Entry{
		Title:   "runner image pin to a digest",
		Summary: "the CI runner image is pinned to a sha256 digest, not a floating tag",
		Why:     "found the same drift again on a followup audit of the pipeline",
		Verdict: VerdictBad,
		Scope:   ScopeProject,
	}
	// Precondition: MergeEntries really does produce a "## Prior context"
	// marker the BlockPatterns rule above will reject, so this exercises the
	// merged-Validate-failure branch specifically, not some other one.
	if merged, _ := MergeEntries(first, second); !strings.Contains(merged.Why, "## Prior context") {
		t.Fatalf("precondition: merged.Why = %q, want it to contain \"## Prior context\"", merged.Why)
	}

	doc, err := limited.Save(ctx, second)
	if err != nil {
		t.Fatalf("save second: %v", err)
	}

	// The merge must still have been skipped: two files on disk, not one
	// merged one.
	docs, err := limited.Scan(ctx, ScopeProject)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 {
		t.Fatalf("stored %d memories, want 2 (merge must still be skipped on a Validate failure)", len(docs))
	}

	// The discarded Validate error must now be reported, not silent.
	found := false
	for _, w := range doc.Truncated {
		if strings.Contains(w, "merge skipped") {
			found = true
		}
	}
	if !found {
		t.Fatalf("doc.Truncated = %v, want an entry reporting the skipped merge", doc.Truncated)
	}
}
