package ledger

import (
	"context"
	"errors"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// DeleteRun removes a settled run and its derived in-memory data.
func (s *StorageRepository) DeleteRun(ctx context.Context, runID string) error {
	if err := s.ensureBuilt(ctx); err != nil {
		return err
	}
	lock := s.runLock(runID)
	lock.Lock()
	defer lock.Unlock()

	s.mu.Lock()
	prev, ok := s.proj[runID]
	if !ok || !prev.HasRun || prev.Run == nil {
		s.mu.Unlock()
		return ErrNotFound
	}
	delete(s.proj, runID)
	s.mu.Unlock()

	if _, err := s.store.Events(ctx, runID); err != nil {
		s.mu.Lock()
		s.proj[runID] = prev
		s.mu.Unlock()
		return err
	}
	seq := s.nextSequence(runID)
	payload, err := marshalRunDeleted(runDeletedPayload{RunID: runID, DeletedAt: s.now()})
	if err != nil {
		s.mu.Lock()
		s.proj[runID] = prev
		s.mu.Unlock()
		return err
	}
	tombstone := storage.Event{ID: EventID(runID, eventKindRunDeleted, fmt.Sprintf("%d", seq)), RunID: runID, Sequence: int(seq), Kind: eventKindRunDeleted, Payload: payload}
	holder, bound := claimHolderFromContext(ctx)
	if !bound {
		s.mu.RLock()
		holder = s.claimedRuns[runID]
		s.mu.RUnlock()
	}
	if err := s.store.AppendAndDeleteRun(ctx, tombstone, storage.Claim{RunID: runID, Holder: holder}); err != nil {
		s.rollbackAndRebuild(ctx, runID, func() { s.proj[runID] = prev })
		if errors.Is(err, storage.ErrClaimHeld) {
			return ErrClaimHeld
		}
		return fmt.Errorf("store delete run %q: %w", runID, err)
	}
	s.mu.Lock()
	delete(s.proj, runID)
	delete(s.applied, runID)
	delete(s.allocated, runID)
	delete(s.claimedRuns, runID)
	for key := range s.deliverySeqs {
		if key.runID == runID {
			delete(s.deliverySeqs, key)
		}
	}
	s.mu.Unlock()
	return nil
}
