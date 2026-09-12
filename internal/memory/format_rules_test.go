package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The four tests below exercise the Go-side format and relational rules, one
// rule each: an over-cap related list, an over-length or malformed related id,
// a dangling target, and a one-way (non-reciprocal) link. They are separate
// top-level functions rather than subtests of one parent so that no single
// function crosses the per-function LOC budget.

// TestFormatRulesRejectsOverCapRelatedList pins the MaxRelated cap.
func TestFormatRulesRejectsOverCapRelatedList(t *testing.T) {
	related := make([]string, MaxRelated+1)
	for i := range related {
		related[i] = "target_memory"
	}
	entry := Entry{
		Title:   "Over-cap related test",
		Summary: "Testing over cap related list",
		Why:     "Because limits must be strictly enforced",
		Scope:   ScopeProject,
		Verdict: VerdictGood,
		Related: related,
	}
	err := entry.Validate(Limits{})
	if err == nil {
		t.Fatal("expected Validate to reject over-cap related list, got nil")
	}
	if !strings.Contains(err.Error(), "related must have at most") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

// TestFormatRulesRejectsMalformedRelatedID pins MaxRelatedIDLen and the id
// charset rule.
func TestFormatRulesRejectsMalformedRelatedID(t *testing.T) {
	longID := strings.Repeat("a", MaxRelatedIDLen+1)
	entry := Entry{
		Title:   "Over-length related ID test",
		Summary: "Testing over length related id",
		Why:     "Because ID lengths must be strictly enforced",
		Scope:   ScopeProject,
		Verdict: VerdictGood,
		Related: []string{longID},
	}
	err := entry.Validate(Limits{})
	if err == nil {
		t.Fatal("expected Validate to reject over-length related id, got nil")
	}
	if !strings.Contains(err.Error(), "each related id must be 1-") {
		t.Fatalf("unexpected error message: %v", err)
	}
	for _, invalid := range []string{"", "has\nnewline", "has:colon", "has[bracket", "has]bracket", "has{brace", "has}brace", "has,comma", "has #comment"} {
		if err := ValidateRelatedIDFormat(invalid); err == nil {
			t.Fatalf("expected ValidateRelatedIDFormat to reject %q, got nil", invalid)
		}
	}
}

// TestFormatRulesRejectsDanglingTarget pins the cross-file rule that a related
// id must name an entry that exists.
func TestFormatRulesRejectsDanglingTarget(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	source, err := NewMarkdownSource(tempDir, "", "")
	if err != nil {
		t.Fatal(err)
	}

	entry := Entry{
		Title:   "Dangling Link Memory",
		Summary: "This points to a nonexistent target",
		Why:     "Testing dangling target rejection",
		Scope:   ScopeProject,
		Verdict: VerdictGood,
		Related: []string{"nonexistent_target_id"},
	}

	_, err = source.Save(ctx, entry)
	if err == nil {
		t.Fatal("expected Save to reject dangling related target, got nil")
	}
	if !strings.Contains(err.Error(), "dangling target") {
		t.Fatalf("expected dangling target error, got: %v", err)
	}
}

// TestFormatRulesRejectsOneWayLink pins the cross-file reciprocity rule: a
// link from A to B is refused unless B links back to A.
func TestFormatRulesRejectsOneWayLink(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	source, err := NewMarkdownSource(tempDir, "", "")
	if err != nil {
		t.Fatal(err)
	}

	// First entry has no related links.
	first := Entry{
		Title:   "First Memory",
		Summary: "First memory with no links",
		Why:     "Testing asymmetric relationship detection",
		Scope:   ScopeProject,
		Verdict: VerdictGood,
	}
	firstDoc, err := source.Save(ctx, first)
	if err != nil {
		t.Fatalf("failed to save first memory: %v", err)
	}

	// Second entry points to firstDoc.ID, but firstDoc does not point to second.
	second := Entry{
		Title:   "Second Memory",
		Summary: "Second memory linking to first",
		Why:     "Testing asymmetric link rejection",
		Scope:   ScopeProject,
		Verdict: VerdictGood,
		Related: []string{firstDoc.ID},
	}
	_, err = source.Save(ctx, second)
	if err == nil {
		t.Fatal("expected Save to reject asymmetric link, got nil")
	}
	if !strings.Contains(err.Error(), "reciprocal linking is mandatory") {
		t.Fatalf("expected reciprocity error, got: %v", err)
	}
}

// TestFormatRules_RealCorpusAccepted asserts that the real .agents/memories corpus
// is scanned cleanly and passes cross-entry relational validation (reciprocity and no dangling targets).
func TestFormatRules_RealCorpusAccepted(t *testing.T) {
	ctx := context.Background()

	// Locate repo root from current working directory or traverse upward.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	repoRoot := wd
	for {
		if _, err := os.Stat(filepath.Join(repoRoot, ".agents", "memories")); err == nil {
			break
		}
		parent := filepath.Dir(repoRoot)
		if parent == repoRoot {
			t.Fatal("could not find repository root containing .agents/memories")
		}
		repoRoot = parent
	}

	source, err := NewMarkdownSource(repoRoot, "", "")
	if err != nil {
		t.Fatalf("failed to create MarkdownSource for %s: %v", repoRoot, err)
	}

	docs, err := source.Scan(ctx, ScopeProject)
	if err != nil {
		t.Fatalf("Scan failed on real corpus: %v", err)
	}

	if len(docs) == 0 {
		t.Fatal("real .agents/memories corpus has 0 scanned documents")
	}

	// Validate cross-entry integrity (reciprocity & no dangling targets).
	if err := ValidateStoreRelations(docs); err != nil {
		t.Fatalf("real corpus failed cross-entry relational validation: %v", err)
	}
}
