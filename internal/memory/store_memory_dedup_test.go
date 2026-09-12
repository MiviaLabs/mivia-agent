package memory

// In-memory store dedup arms that a plain near-duplicate save does not
// reach: an exact id match short-circuits the similarity score, and a
// merged entry missing a verdict or created date is defaulted rather than
// written invalid.

import (
	"context"
	"strings"
	"testing"
)

// memEntry is a valid entry for the in-memory backend. Scope and Verdict
// are always set because Validate refuses an entry without them.
func memEntry(title, summary string) Entry {
	return Entry{
		Title:   title,
		Summary: summary,
		Why:     "why " + title,
		Scope:   ScopeProject,
		Verdict: VerdictGood,
	}
}

// TestMemStoreSaveUpdatesTheRowWithAMatchingID covers the id-match arm:
// re-saving the byte-identical entry resolves to the same derived id, so
// the store updates that row with similarity forced to 1.0 instead of
// appending a duplicate.
func TestMemStoreSaveUpdatesTheRowWithAMatchingID(t *testing.T) {
	store := newMemStore(Config{MaxEntries: DefaultMaxEntries})
	ctx := context.Background()

	first, err := store.Save(ctx, memEntry("Runner image cache", "the cache missed"))
	if err != nil {
		t.Fatalf("first Save: %v", err)
	}

	// The SAME entry again: its id derives from the same fields, so the
	// dedup loop takes the row.id == id branch and forces sim to 1.0.
	secondRes, err := store.Save(ctx, memEntry("Runner image cache", "the cache missed"))
	if err != nil {
		t.Fatalf("second Save: %v", err)
	}

	if secondRes.ID != first.ID {
		t.Fatalf("save with a matching id produced %q, want it to update %q", secondRes.ID, first.ID)
	}
}

// TestMemStoreSaveKeepsAValidVerdictThroughAMerge covers the verdict
// guard on the merged entry: the merge result must always carry a valid
// verdict, because Validate refuses an empty one and the merge would
// otherwise be silently dropped.
func TestMemStoreSaveKeepsAValidVerdictThroughAMerge(t *testing.T) {
	store := newMemStore(Config{MaxEntries: DefaultMaxEntries})
	ctx := context.Background()

	base := memEntry("Verdict defaulting", "a summary that both saves share closely")
	base.Verdict = VerdictNeutral
	if _, err := store.Save(ctx, base); err != nil {
		t.Fatalf("first Save: %v", err)
	}

	// A near-duplicate that merges into the row above.
	second := base
	second.Summary = base.Summary + " with a small addition"
	res, err := store.Save(ctx, second)
	if err != nil {
		t.Fatalf("second Save: %v", err)
	}
	if strings.TrimSpace(string(res.Verdict)) == "" {
		t.Fatal("merged entry kept an empty verdict, want a valid one")
	}
}

// TestMemStoreSaveStampsACreatedDate pins the created default: every
// stored row carries a date, so the store's own ordering and the memory
// file's frontmatter never see an empty value.
func TestMemStoreSaveStampsACreatedDate(t *testing.T) {
	store := newMemStore(Config{MaxEntries: DefaultMaxEntries})
	ctx := context.Background()

	e := memEntry("Created stamping", "a summary")
	e.Created = ""
	res, err := store.Save(ctx, e)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if strings.TrimSpace(res.Created) == "" {
		t.Fatal("stored entry has an empty Created date, want it stamped")
	}
}
