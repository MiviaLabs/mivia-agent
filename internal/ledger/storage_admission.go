package ledger

import (
	"context"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

func (s *StorageLedgerRepository) AdmitSingleTask(ctx context.Context, a SingleTaskAdmission) error {
	if err := s.ensureBuilt(ctx); err != nil {
		return err
	}
	if err := validateSingleTaskAdmission(a); err != nil {
		return err
	}
	batch, ok := s.store.(storage.NewRunBatchAppender)
	if !ok {
		return fmt.Errorf("store does not support atomic new-run admission")
	}
	// Mirror CreateRun: rebase the run's sequence watermark before allocating
	// event sequences. Without this, re-admitting a run ID after DeleteRun
	// would mint sequence 1 again, below the surviving run_deleted tombstone -
	// colliding with UNIQUE(run_id, sequence) on SQLite and maxSeq monotonicity
	// on Memory, and putting run_created before the tombstone in replay order.
	if err := s.rebaseRunSequence(ctx, a.Run.RunID); err != nil {
		return err
	}
	a.Run.IdempotencyKey = a.IdempotencyKey
	rawRun, err := marshalRunSnapshot(a.Run)
	if err != nil {
		return err
	}
	rawTask, err := marshalTaskSnapshot(a.Task)
	if err != nil {
		return err
	}
	runEvent := s.newStoreEvent(a.Run.RunID, storageKindRunCreated, rawRun)
	runEvent.ID = "single-admit-run:" + a.IdempotencyKey
	taskEvent := s.newStoreEvent(a.Run.RunID, storageKindTaskCreated, rawTask)
	taskEvent.ID = "single-admit-task:" + a.IdempotencyKey
	defer s.clearInflight(a.Run.RunID, uint64(runEvent.Sequence))
	defer s.clearInflight(a.Run.RunID, uint64(taskEvent.Sequence))
	if err := batch.AppendBatchForNewRun(ctx, a.Run.RunID, []storage.Event{runEvent, taskEvent}); err != nil {
		if err == storage.ErrDuplicate {
			return ErrDuplicate
		}
		if err == storage.ErrClaimHeld {
			return ErrClaimHeld
		}
		return err
	}
	if err := s.mem.CreateRun(ctx, a.IdempotencyKey, a.Run); err != nil {
		return err
	}
	if err := s.mem.CreateTask(ctx, a.Task); err != nil {
		return err
	}
	s.mu.Lock()
	if uint64(taskEvent.Sequence) > s.applied[a.Run.RunID] {
		s.applied[a.Run.RunID] = uint64(taskEvent.Sequence)
	}
	s.mu.Unlock()
	return nil
}
