package memory

import (
	"context"
	"fmt"
	"testing"
)

func TestSaveConsolidatesAtEightyPercentAndSucceeds(t *testing.T) {
	// sqlite only: consolidation is a sqlite-backend mechanism (Save's
	// transaction-scoped count check); the in-memory backend's own
	// MaxEntries cap is out of this plan's scope (decision-time call: no
	// driver deadlock risk to fix there, and the ephemeral backend is not
	// what gets committed to a repo).
	dir := t.TempDir()
	cfg := Config{
		Backend:          BackendSQLite,
		ProjectPath:      dir + "/project.db",
		MaxEntryBytes:    8192,
		MaxEntries:       10,
		MaxSearchResults: 8,
	}
	s, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	ctx := context.Background()

	// Fill to exactly the cap with distinct (non-similar) entries so no
	// consolidation merge is possible - only eviction can make room, proving
	// consolidation activates on its own past the 80% threshold rather than
	// only in the presence of mergeable duplicates.
	var lastID string
	for i := 0; i < 10; i++ {
		res, err := s.Save(ctx, testEntry(fmt.Sprintf("distinct entry number %d about topic %d", i, i*17), ScopeProject))
		if err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
		lastID = res.ID
	}

	// A cap-exactly-full store without consolidation would refuse this next
	// save (see the pre-Wave-5 hard-wall behavior); with consolidation
	// active it must succeed by evicting the oldest archive row instead.
	if _, err := s.Save(ctx, testEntry("one more distinct entry about topic 999", ScopeProject)); err != nil {
		t.Fatalf("save past cap with consolidation active: %v", err)
	}

	count, err := s.Count(ctx, ScopeProject)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count > 10 {
		t.Fatalf("count = %d after consolidating save, want <= 10", count)
	}
	_ = lastID
}

func TestConsolidateMergesNearDuplicatesBeforeEvicting(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		Backend:          BackendSQLite,
		ProjectPath:      dir + "/project.db",
		MaxEntryBytes:    8192,
		MaxEntries:       10,
		MaxSearchResults: 8,
	}
	s, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	ctx := context.Background()

	// Two near-identical rewordings of the same fact, repeated to fill 8 of
	// the 10 slots, plus two genuinely distinct fillers - past the 80%
	// threshold (8/10), consolidation should merge the duplicate pairs
	// rather than only evicting, so total count drops well under the cap.
	pairs := []string{"deploy pipeline fix pinned runner image", "deploy pipeline fix pinned the runner image"}
	for i := 0; i < 4; i++ {
		title := pairs[i%2] + fmt.Sprintf(" variant %d", i/2)
		if _, err := s.Save(ctx, entryWithBody(title, title, ScopeProject)); err != nil {
			t.Fatalf("save duplicate-ish %d: %v", i, err)
		}
	}
	for i := 0; i < 4; i++ {
		if _, err := s.Save(ctx, testEntry(fmt.Sprintf("distinct filler %d about topic %d", i, i*71), ScopeProject)); err != nil {
			t.Fatalf("save filler %d: %v", i, err)
		}
	}
	// This ninth save crosses the 80% threshold and must trigger consolidation.
	if _, err := s.Save(ctx, testEntry("distinct filler 4 about topic 999", ScopeProject)); err != nil {
		t.Fatalf("save 9th entry: %v", err)
	}

	count, err := s.Count(ctx, ScopeProject)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count >= 9 {
		t.Fatalf("count = %d after a consolidating save with mergeable duplicates present, want it reduced below the pre-save total", count)
	}
}

// entryWithBody is testEntry with an explicit summary/why so near-duplicate
// title pairs can be constructed without testEntry's title-derived summary
// diluting the similarity score.
func entryWithBody(title, summary string, scope Scope) Entry {
	e := testEntry(title, scope)
	e.Summary = summary
	e.Why = "because " + summary
	return e
}

// TestMergeNeverDeletesACoreNearDuplicate is a regression for a Step 5
// hostile-audit finding: mergeNearDuplicates always kept the earlier-created
// row of a near-duplicate pair regardless of tier, so an archive row created
// before a later-promoted core near-duplicate would survive the merge and
// silently delete the core row - violating "core entries are never
// auto-evicted, only merged."
func TestMergeNeverDeletesACoreNearDuplicate(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		Backend:          BackendSQLite,
		ProjectPath:      dir + "/project.db",
		MaxEntryBytes:    8192,
		MaxEntries:       10,
		MaxSearchResults: 8,
	}
	s, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	ctx := context.Background()

	// A (day 1, archive) is created before B (day 5), which is then
	// promoted to core - the exact ordering the bug depended on
	// (mergeNearDuplicates always kept the earlier `created` row).
	entryA := entryWithBody("deploy pipeline fix pinned runner image A", "deploy pipeline fix pinned runner image", ScopeProject)
	entryA.Created = "2026-01-01"
	resA, err := s.Save(ctx, entryA)
	if err != nil {
		t.Fatalf("save A: %v", err)
	}
	entryB := entryWithBody("deploy pipeline fix pinned runner image B", "deploy pipeline fix pinned the runner image", ScopeProject)
	entryB.Created = "2026-01-05"
	resB, err := s.Save(ctx, entryB)
	if err != nil {
		t.Fatalf("save B: %v", err)
	}
	if err := s.PromoteToCore(ctx, resB.ID); err != nil {
		t.Fatalf("promote B: %v", err)
	}

	// Fill past the 80% threshold with distinct fillers to trigger
	// consolidation, which must merge A and B without deleting B (core).
	for i := 0; i < 7; i++ {
		if _, err := s.Save(ctx, testEntry(fmt.Sprintf("filler %d about topic %d", i, i*97), ScopeProject)); err != nil {
			t.Fatalf("save filler %d: %v", i, err)
		}
	}

	if err := s.PromoteToCore(ctx, resB.ID); err != nil {
		t.Fatalf("core entry B was evicted by a merge with an earlier archive near-duplicate: %v", err)
	}
	_ = resA
}

func TestSaveConsolidationNeverEvictsCore(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		Backend:          BackendSQLite,
		ProjectPath:      dir + "/project.db",
		MaxEntryBytes:    8192,
		MaxEntries:       10,
		MaxSearchResults: 8,
	}
	s, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	ctx := context.Background()

	// Promote the very first (oldest) entries to core - the entries
	// consolidation's eviction would normally pick first by "oldest
	// archive." If eviction ever touched a core row, one of these ids would
	// disappear.
	var coreIDs []string
	for i := 0; i < 5; i++ {
		res, err := s.Save(ctx, testEntry(fmt.Sprintf("core candidate %d about topic %d", i, i*31), ScopeProject))
		if err != nil {
			t.Fatalf("save core candidate %d: %v", i, err)
		}
		if err := s.PromoteToCore(ctx, res.ID); err != nil {
			t.Fatalf("promote %d: %v", i, err)
		}
		coreIDs = append(coreIDs, res.ID)
	}
	for i := 0; i < 6; i++ {
		if _, err := s.Save(ctx, testEntry(fmt.Sprintf("archive filler %d about topic %d", i, i*53), ScopeProject)); err != nil {
			t.Fatalf("save filler %d: %v", i, err)
		}
	}

	for _, id := range coreIDs {
		// PromoteToCore on a still-present core entry is a no-op success;
		// on an evicted (deleted) one it fails with ErrEntryNotFound.
		if err := s.PromoteToCore(ctx, id); err != nil {
			t.Fatalf("core entry %q was evicted by consolidation: %v", id, err)
		}
	}
}
