package ledger

import (
	"context"
	"fmt"
)

// rebaseRunSequence preserves sequence monotonicity when a deleted run ID is
// recreated. DeleteRun leaves a tombstone but clears projection watermarks, so
// creating the ID again must continue after the tombstone rather than minting
// sequence one. This is only needed on creation, not on the hot append path.
func (s *StorageLedgerRepository) rebaseRunSequence(ctx context.Context, runID string) error {
	events, err := s.store.Events(ctx, runID)
	if err != nil {
		return fmt.Errorf("read existing events for %s: %w", runID, err)
	}
	var maxSequence uint64
	for _, event := range events {
		if uint64(event.Sequence) > maxSequence {
			maxSequence = uint64(event.Sequence)
		}
	}
	s.mu.Lock()
	if maxSequence > s.applied[runID] {
		s.applied[runID] = maxSequence
	}
	s.mu.Unlock()
	return nil
}
