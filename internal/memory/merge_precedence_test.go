package memory

// Merge-precedence and id-derivation branches. MergeEntries decides which
// side of a near-duplicate wins each field; getting that backwards silently
// rewrites a settled memory with a weaker one, so the precedence rules are
// pinned per field here rather than inferred from a whole-entry round trip.

import (
	"strings"
	"testing"
)

// TestMergeEntriesTakesTheIncomingGoodAndBadWhenTheVerdictAllows covers the
// Good/Bad override arms: an incoming entry with the same (overridable)
// verdict contributes its own Good/Bad text, because that is the newer
// observation of the same situation.
func TestMergeEntriesTakesTheIncomingGoodAndBadWhenTheVerdictAllows(t *testing.T) {
	existing := Entry{
		Title:   "Pipeline cache",
		Summary: "cache missed",
		Why:     "mirror unreachable",
		Scope:   ScopeProject,
		Verdict: VerdictGood,
		Good:    "old good",
		Bad:     "old bad",
	}
	incoming := existing
	incoming.Good = "new good"
	incoming.Bad = "new bad"

	merged, _ := MergeEntries(existing, incoming)

	if merged.Good != "new good" {
		t.Fatalf("merged Good = %q, want the incoming %q", merged.Good, "new good")
	}
	if merged.Bad != "new bad" {
		t.Fatalf("merged Bad = %q, want the incoming %q", merged.Bad, "new bad")
	}
}

// TestMergeEntriesKeepsTheExistingGoodWhenTheIncomingIsEmpty pins the
// guard on those same arms: an incoming entry that says nothing must not
// erase what the existing entry already recorded.
func TestMergeEntriesKeepsTheExistingGoodWhenTheIncomingIsEmpty(t *testing.T) {
	existing := Entry{
		Title:   "Pipeline cache",
		Summary: "cache missed",
		Why:     "mirror unreachable",
		Scope:   ScopeProject,
		Verdict: VerdictGood,
		Good:    "keep me",
		Bad:     "keep me too",
	}
	incoming := existing
	incoming.Good = ""
	incoming.Bad = ""

	merged, _ := MergeEntries(existing, incoming)

	if merged.Good != "keep me" {
		t.Fatalf("merged Good = %q, want the existing text preserved", merged.Good)
	}
	if merged.Bad != "keep me too" {
		t.Fatalf("merged Bad = %q, want the existing text preserved", merged.Bad)
	}
}

// TestMergeEntriesRaisesImportanceButNeverLowersIt covers the importance
// rank comparison in both directions: a stronger incoming importance wins,
// a weaker one is ignored. A merge that silently downgraded importance
// would bury a memory the operator had deliberately promoted.
func TestMergeEntriesRaisesImportanceButNeverLowersIt(t *testing.T) {
	base := Entry{
		Title:   "Importance rules",
		Summary: "summary",
		Why:     "why",
		Scope:   ScopeProject,
		Verdict: VerdictGood,
	}

	low := base
	low.Importance = ImportanceLow
	high := base
	high.Importance = ImportanceHigh

	raised, _ := MergeEntries(low, high)
	if raised.Importance != ImportanceHigh {
		t.Fatalf("merged importance = %q, want it raised to %q", raised.Importance, ImportanceHigh)
	}

	kept, _ := MergeEntries(high, low)
	if kept.Importance != ImportanceHigh {
		t.Fatalf("merged importance = %q, want the higher %q kept", kept.Importance, ImportanceHigh)
	}
}

// TestMergeEntriesSkipsBlankCollectionItems pins the trim-and-skip arm of
// the collection merge: whitespace-only tags are not real tags and must not
// reach the merged entry (they would fail the format gate downstream).
func TestMergeEntriesSkipsBlankCollectionItems(t *testing.T) {
	existing := Entry{
		Title:   "Collections",
		Summary: "summary",
		Why:     "why",
		Scope:   ScopeProject,
		Verdict: VerdictGood,
		Tags:    []string{"real"},
	}
	incoming := existing
	incoming.Tags = []string{"  ", "", "second"}

	merged, _ := MergeEntries(existing, incoming)

	for _, tag := range merged.Tags {
		if strings.TrimSpace(tag) == "" {
			t.Fatalf("merged tags = %v, want no blank entries", merged.Tags)
		}
	}
	var haveSecond bool
	for _, tag := range merged.Tags {
		if tag == "second" {
			haveSecond = true
		}
	}
	if !haveSecond {
		t.Fatalf("merged tags = %v, want the incoming non-blank tag kept", merged.Tags)
	}
}

// TestValidateEntryRelationsDerivesExistingIDsFromTheirPaths covers the id
// fallback inside the incoming-entry check: an existing document with no
// explicit ID is matched by its filename, so updating it in place is not
// mistaken for adding a second, conflicting entry.
func TestValidateEntryRelationsDerivesExistingIDsFromTheirPaths(t *testing.T) {
	existing := []MarkdownDocument{
		{Path: "alpha-memory.md", Entry: relEntry("Alpha", "beta_memory")},
		{Path: "beta-memory.md", Entry: relEntry("Beta")},
	}
	// Updating beta-memory (matched by path, not by an explicit ID) so it
	// links back to alpha_memory satisfies reciprocity.
	incoming := relEntry("Beta", "alpha_memory")

	if err := ValidateEntryRelations(existing, incoming, "beta_memory"); err != nil {
		t.Fatalf("ValidateEntryRelations with path-derived existing ids = %v, want nil", err)
	}
}
