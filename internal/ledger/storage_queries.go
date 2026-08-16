package ledger

import (
	"context"
)

// Read-side queries. Every query first catches up on events appended by other
// repository instances over the same store (see ensureBuilt), then delegates
// to the in-memory projection for the result.

func (s *StorageLedgerRepository) GetRun(ctx context.Context, runID string) (RunSnapshot, error) {
	if err := s.ensureBuilt(ctx); err != nil {
		return RunSnapshot{}, err
	}
	return s.mem.GetRun(ctx, runID)
}

func (s *StorageLedgerRepository) GetRunByIdempotencyKey(ctx context.Context, key string) (RunSnapshot, error) {
	if err := s.ensureBuilt(ctx); err != nil {
		return RunSnapshot{}, err
	}
	return s.mem.GetRunByIdempotencyKey(ctx, key)
}

func (s *StorageLedgerRepository) ListRuns(ctx context.Context, status ...RunStatus) ([]RunSnapshot, error) {
	if err := s.ensureBuilt(ctx); err != nil {
		return nil, err
	}
	return s.mem.ListRuns(ctx, status...)
}

func (s *StorageLedgerRepository) GetTask(ctx context.Context, runID, taskID string) (TaskSnapshot, error) {
	if err := s.ensureBuilt(ctx); err != nil {
		return TaskSnapshot{}, err
	}
	return s.mem.GetTask(ctx, runID, taskID)
}

func (s *StorageLedgerRepository) ListTasks(ctx context.Context, runID string) ([]TaskSnapshot, error) {
	if err := s.ensureBuilt(ctx); err != nil {
		return nil, err
	}
	return s.mem.ListTasks(ctx, runID)
}

func (s *StorageLedgerRepository) ListEvents(ctx context.Context, runID string) ([]LifecycleEvent, error) {
	if err := s.ensureBuilt(ctx); err != nil {
		return nil, err
	}
	return s.mem.ListEvents(ctx, runID)
}
