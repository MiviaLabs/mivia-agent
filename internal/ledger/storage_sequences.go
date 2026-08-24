package ledger

import (
	"context"
	"fmt"
	"sort"
)

// rebaseRunSequence preserves sequence monotonicity when a deleted run ID is
// recreated. DeleteRun leaves a tombstone but clears projection watermarks, so
// creating the ID again must continue after the tombstone rather than minting
// sequence one. It also folds whatever the store already holds for the run
// into this instance's projection BEFORE advancing the watermark: applied must
// never run ahead of what the projection holds, or catch-up would skip events
// it never applied. In an admission race the loser reads the winner's already
// committed batch here; without the fold the run would be invisible to that
// instance forever.
func (s *StorageLedgerRepository) rebaseRunSequence(ctx context.Context, runID string) error {
	events, err := s.engine.Store().Events(ctx, runID)
	if err != nil {
		return fmt.Errorf("read existing events for %s: %w", runID, err)
	}
	if len(events) == 0 {
		return nil
	}
	// Fold in global append order, exactly like applyTail, so a run_deleted
	// tombstone always lands before a later reused-ID run_created.
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].RowID != events[j].RowID {
			return events[i].RowID < events[j].RowID
		}
		return events[i].Sequence < events[j].Sequence
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, evt := range events {
		if uint64(evt.Sequence) <= s.engine.Watermarks().Applied(evt.RunID) ||
			s.isInflightLocked(evt.RunID, uint64(evt.Sequence)) {
			continue
		}
		if err := s.applyStoreEventLocked(ctx, evt); err != nil {
			return fmt.Errorf("apply event %s for %s: %w", evt.ID, evt.RunID, err)
		}
		// applyStoreEventLocked deletes the watermark for a tombstone, so the
		// tombstone's own sequence is restored here as the new floor: the next
		// minted sequence must sit above the surviving tombstone, and a later
		// reused-ID run_created folds after it.
		s.engine.Watermarks().SetApplied(evt.RunID, uint64(evt.Sequence))
		// Keep new event IDs from colliding with replayed ones after a
		// restart, exactly as applyTail does.
		advanceStorageEventIDCounter(parseSuffixNum(evt.ID, "se-"))
	}
	return nil
}
