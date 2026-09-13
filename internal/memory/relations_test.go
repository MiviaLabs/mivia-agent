package memory

// Cross-entry relation rules and the in-memory store's dedup-merge path.
// The relation rules are what stop the Go writer emitting a store that
// scripts/check_memories.py then rejects, so both the accept and the refuse
// direction are pinned here.

import (
	"context"
	"strings"
	"testing"
)

// relEntry builds a valid entry carrying the supplied related ids. Scope and
// Verdict are always set: Validate refuses an entry without them, so an
// omitted field would fail the test for the wrong reason.
func relEntry(title string, related ...string) Entry {
	return Entry{
		Title:   title,
		Summary: "summary for " + title,
		Why:     "why " + title + " exists",
		Scope:   ScopeProject,
		Verdict: VerdictGood,
		Related: related,
	}
}

// relDoc pairs an id with an entry the way Scan returns them.
func relDoc(id string, e Entry) MarkdownDocument {
	return MarkdownDocument{Path: id + ".md", ID: id, Entry: e}
}

// TestValidateStoreRelationsAcceptsAReciprocalPair is the positive case: two
// entries that name each other satisfy both rules and must pass.
func TestValidateStoreRelationsAcceptsAReciprocalPair(t *testing.T) {
	docs := []MarkdownDocument{
		relDoc("alpha", relEntry("Alpha", "beta")),
		relDoc("beta", relEntry("Beta", "alpha")),
	}
	if err := ValidateStoreRelations(docs); err != nil {
		t.Fatalf("ValidateStoreRelations on a reciprocal pair = %v, want nil", err)
	}
}

// TestValidateStoreRelationsRejectsADanglingTarget pins rule 1: a related id
// naming no entry in the store is refused, because the pre-push gate refuses
// exactly the same shape.
func TestValidateStoreRelationsRejectsADanglingTarget(t *testing.T) {
	docs := []MarkdownDocument{
		relDoc("alpha", relEntry("Alpha", "nowhere")),
	}
	err := ValidateStoreRelations(docs)
	if err == nil {
		t.Fatal("ValidateStoreRelations accepted a dangling target, want an error")
	}
	if !strings.Contains(err.Error(), "dangling target") {
		t.Fatalf("error = %v, want it to name the dangling target rule", err)
	}
}

// TestValidateStoreRelationsRejectsAOneWayLink pins rule 2: A naming B while
// B does not name A is asymmetric and refused.
func TestValidateStoreRelationsRejectsAOneWayLink(t *testing.T) {
	docs := []MarkdownDocument{
		relDoc("alpha", relEntry("Alpha", "beta")),
		relDoc("beta", relEntry("Beta")),
	}
	err := ValidateStoreRelations(docs)
	if err == nil {
		t.Fatal("ValidateStoreRelations accepted a one-way link, want an error")
	}
	if !strings.Contains(err.Error(), "reciprocal linking is mandatory") {
		t.Fatalf("error = %v, want it to name the reciprocity rule", err)
	}
}

// TestValidateStoreRelationsIgnoresBlankTargets pins the skip for empty
// related entries: whitespace is not a link and must not be reported as a
// dangling one.
func TestValidateStoreRelationsIgnoresBlankTargets(t *testing.T) {
	docs := []MarkdownDocument{
		relDoc("alpha", relEntry("Alpha", "  ", "")),
	}
	if err := ValidateStoreRelations(docs); err != nil {
		t.Fatalf("ValidateStoreRelations with blank targets = %v, want nil", err)
	}
}

// TestValidateStoreRelationsDerivesIDFromPath covers the id fallback: a
// document with no explicit ID takes it from the filename, hyphens becoming
// underscores, matching check_memories.py's expected_id derivation.
func TestValidateStoreRelationsDerivesIDFromPath(t *testing.T) {
	docs := []MarkdownDocument{
		{Path: "some-memory.md", Entry: relEntry("Some", "other_memory")},
		{Path: "other-memory.md", Entry: relEntry("Other", "some_memory")},
	}
	if err := ValidateStoreRelations(docs); err != nil {
		t.Fatalf("ValidateStoreRelations with path-derived ids = %v, want nil", err)
	}
}

// TestValidateEntryRelationsRefusesANewDanglingLink pins the incoming-entry
// gate: a save that would introduce a link to a nonexistent memory is
// refused before anything is written.
func TestValidateEntryRelationsRefusesANewDanglingLink(t *testing.T) {
	existing := []MarkdownDocument{relDoc("alpha", relEntry("Alpha"))}
	incoming := relEntry("Beta", "does_not_exist")

	err := ValidateEntryRelations(existing, incoming, "beta")
	if err == nil {
		t.Fatal("ValidateEntryRelations accepted a new dangling link, want an error")
	}
	if !strings.Contains(err.Error(), "dangling target") {
		t.Fatalf("error = %v, want it to name the dangling target rule", err)
	}
}

// TestValidateEntryRelationsAcceptsAReciprocatedUpdate is the positive
// counterpart: updating an entry so it links back to one that already names
// it satisfies both rules.
func TestValidateEntryRelationsAcceptsAReciprocatedUpdate(t *testing.T) {
	existing := []MarkdownDocument{
		relDoc("alpha", relEntry("Alpha", "beta")),
		relDoc("beta", relEntry("Beta")),
	}
	incoming := relEntry("Beta", "alpha")

	if err := ValidateEntryRelations(existing, incoming, "beta"); err != nil {
		t.Fatalf("ValidateEntryRelations on a reciprocated update = %v, want nil", err)
	}
}

// TestMemoryStoreSaveMergesANearDuplicate covers store_memory.go's dedup
// branch: a second save close enough to merge updates the existing row in
// place rather than appending a second one.
func TestMemoryStoreSaveMergesANearDuplicate(t *testing.T) {
	store := newMemStore(Config{MaxEntries: DefaultMaxEntries})
	ctx := context.Background()

	first := Entry{
		Title:   "CI cache miss on the runner image",
		Summary: "the runner image cache missed on every build",
		Why:     "registry mirror was not reachable from the runner",
		Scope:   ScopeProject,
		Verdict: VerdictGood,
		Tags:    []string{"ci", "cache"},
	}
	firstRes, err := store.Save(ctx, first)
	if err != nil {
		t.Fatalf("first Save: %v", err)
	}

	second := first
	second.Summary = "the runner image cache missed on every build again"
	second.Tags = []string{"ci", "cache", "runner"}
	secondRes, err := store.Save(ctx, second)
	if err != nil {
		t.Fatalf("second Save: %v", err)
	}

	if secondRes.ID != firstRes.ID {
		t.Fatalf("near-duplicate save produced id %q, want it merged into %q", secondRes.ID, firstRes.ID)
	}

	// The merge is what this test pins: the second save returned the FIRST
	// row's id, so it updated that row instead of appending a second one.
	// Its merged tag set must carry both saves' tags.
	if got := strings.Join(secondRes.Tags, ","); !strings.Contains(got, "runner") {
		t.Fatalf("merged tags = %q, want them to include the second save's \"runner\" tag", got)
	}
	if secondRes.Title != first.Title {
		t.Fatalf("merged title = %q, want the existing row's title %q", secondRes.Title, first.Title)
	}
}
