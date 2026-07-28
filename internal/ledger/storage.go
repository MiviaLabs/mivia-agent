package ledger

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// StorageLedgerRepository wraps a storage.Store as a durable LedgerRepository.
// Every mutation writes an event to the append-only store AND updates an
// in-memory projection for fast reads. On construction, the projection is
// lazily rebuilt from stored events for crash recovery.
type StorageLedgerRepository struct {
	store     storage.Store
	mem       *MemoryLedgerRepository
	mu        sync.RWMutex
	built     bool // true once projection has been rebuilt from store
	sequences map[string]uint64 // runID → next event sequence
	now       func() time.Time
}

// NewStorageLedgerRepository creates a StorageLedgerRepository backed by the
// given store. The in-memory projection is lazily rebuilt on first access.
func NewStorageLedgerRepository(store storage.Store) *StorageLedgerRepository {
	return &StorageLedgerRepository{
		store:     store,
		mem:       NewMemoryLedgerRepository(),
		sequences: make(map[string]uint64),
		now:       time.Now,
	}
}

// SetTimeSource replaces the clock for deterministic tests.
func (s *StorageLedgerRepository) SetTimeSource(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
	s.mem.SetTimeSource(now)
}

// Close closes the underlying store.
func (s *StorageLedgerRepository) Close() error {
	return s.store.Close()
}

// ensureBuilt rebuilds the in-memory projection from stored events if not
// already done. Safe for concurrent calls.
func (s *StorageLedgerRepository) ensureBuilt(ctx context.Context) error {
	s.mu.RLock()
	if s.built {
		s.mu.RUnlock()
		return nil
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.built {
		return nil
	}
	return s.rebuildLocked(ctx)
}

// rebuildLocked reads ALL events from the store and rebuilds the in-memory
// projection. Must be called with s.mu write-locked.
func (s *StorageLedgerRepository) rebuildLocked(ctx context.Context) error {
	// Read all events by iterating known run IDs from the store.
	runIDs, err := s.store.ListRunIDs(ctx)
	if err != nil {
		return fmt.Errorf("list run IDs: %w", err)
	}
	for _, runID := range runIDs {
		events, err := s.store.Events(ctx, runID)
		if err != nil {
			return fmt.Errorf("read events for %s: %w", runID, err)
		}
		runSnap, tasks, lifecycleEvts, err := RebuildProjection(events)
		if err != nil {
			return fmt.Errorf("rebuild projection for %s: %w", runID, err)
		}
		if runSnap.RunID != "" {
			_ = s.mem.CreateRun(ctx, "", runSnap)
			for _, t := range tasks {
				_ = s.mem.CreateTask(ctx, t)
			}
			for _, levt := range lifecycleEvts {
				_ = s.mem.AppendEvent(ctx, levt)
			}
		}
		// Track max sequence for each run.
		for _, evt := range events {
			if evt.Sequence > int(s.sequences[runID]) {
				s.sequences[runID] = uint64(evt.Sequence)
			}
		}
	}
	s.built = true
	return nil
}

// newStoreEvent creates a storage.Event with the next sequence number for the run.
// Safe for concurrent use.
func (s *StorageLedgerRepository) newStoreEvent(runID, kind string, payload []byte) storage.Event {
	// Use atomic per-run sequence via the sequences map under lock.
	id := newStorageEventID()

	// RebuildLock already set sequences from stored events. For new runs,
	// we start at 1.
	return storage.Event{
		ID:       id,
		RunID:    runID,
		Sequence: int(s.nextSequence(runID)),
		Kind:     kind,
		Payload:  payload,
	}
}

// nextSequence returns the next sequence number for a run and increments
// the counter. Must NOT be called under s.mu (to avoid deadlock).
func (s *StorageLedgerRepository) nextSequence(runID string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sequences[runID]++
	return s.sequences[runID]
}

// ---------------------------------------------------------------------------
// LedgerRepository implementation
// ---------------------------------------------------------------------------

func (s *StorageLedgerRepository) CreateRun(ctx context.Context, key string, snapshot RunSnapshot) error {
	if err := s.ensureBuilt(ctx); err != nil {
		return err
	}

	payload, err := marshalRunSnapshot(snapshot)
	if err != nil {
		return fmt.Errorf("marshal run snapshot: %w", err)
	}

	storeEvt := s.newStoreEvent(snapshot.RunID, storageKindRunCreated, payload)
	if err := s.store.Append(ctx, storeEvt); err != nil {
		if err == storage.ErrDuplicate {
			return ErrDuplicate
		}
		return fmt.Errorf("store append: %w", err)
	}

	return s.mem.CreateRun(ctx, key, snapshot)
}

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

func (s *StorageLedgerRepository) CreateTask(ctx context.Context, snap TaskSnapshot) error {
	if err := s.ensureBuilt(ctx); err != nil {
		return err
	}

	payload, err := marshalTaskSnapshot(snap)
	if err != nil {
		return fmt.Errorf("marshal task snapshot: %w", err)
	}

	storeEvt := s.newStoreEvent(snap.RunID, storageKindTaskCreated, payload)

	if err := s.store.Append(ctx, storeEvt); err != nil {
		if err == storage.ErrDuplicate {
			return ErrDuplicate
		}
		return fmt.Errorf("store append: %w", err)
	}

	return s.mem.CreateTask(ctx, snap)
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

func (s *StorageLedgerRepository) AppendEvent(ctx context.Context, event LifecycleEvent) error {
	if err := s.ensureBuilt(ctx); err != nil {
		return err
	}

	payload, err := marshalLifecycleEvent(event)
	if err != nil {
		return fmt.Errorf("marshal lifecycle event: %w", err)
	}

	storeEvt := s.newStoreEvent(event.RunID, storageKindLifecycleEvent, payload)
	if err := s.store.Append(ctx, storeEvt); err != nil {
		if err == storage.ErrDuplicate {
			return ErrDuplicate
		}
		return fmt.Errorf("store append: %w", err)
	}

	return s.mem.AppendEvent(ctx, event)
}

func (s *StorageLedgerRepository) ListEvents(ctx context.Context, runID string) ([]LifecycleEvent, error) {
	if err := s.ensureBuilt(ctx); err != nil {
		return nil, err
	}
	return s.mem.ListEvents(ctx, runID)
}

func (s *StorageLedgerRepository) CompareAndSetTaskStatus(ctx context.Context, runID, taskID string, expectedVersion uint64, newStatus string) error {
	if err := s.ensureBuilt(ctx); err != nil {
		return err
	}

	// Validate and apply via mem first.
	if err := s.mem.CompareAndSetTaskStatus(ctx, runID, taskID, expectedVersion, newStatus); err != nil {
		return err
	}

	// After successful CAS, the task version is exactly expectedVersion+1.
	// Compute completedAt directly (no TOCTOU from reading back from mem).
	var completedAt *time.Time
	if isTerminalTaskStatus(newStatus) {
		t := s.now()
		completedAt = &t
	}

	payload, err := marshalStatusChange(taskID, newStatus, expectedVersion+1, completedAt)
	if err != nil {
		return fmt.Errorf("marshal status change: %w", err)
	}

	storeEvt := s.newStoreEvent(runID, storageKindTaskStatusChanged, payload)

	if err := s.store.Append(ctx, storeEvt); err != nil {
		return fmt.Errorf("store append: %w", err)
	}

	return nil
}

func (s *StorageLedgerRepository) SetTaskOutput(ctx context.Context, runID, taskID string, outputRef, errorRef string) error {
	if err := s.ensureBuilt(ctx); err != nil {
		return err
	}

	payload, err := marshalOutputRefs(taskID, outputRef, errorRef)
	if err != nil {
		return fmt.Errorf("marshal output refs: %w", err)
	}

	storeEvt := s.newStoreEvent(runID, storageKindTaskOutputSet, payload)

	if err := s.store.Append(ctx, storeEvt); err != nil {
		return fmt.Errorf("store append: %w", err)
	}

	return s.mem.SetTaskOutput(ctx, runID, taskID, outputRef, errorRef)
}

func (s *StorageLedgerRepository) SetTaskAttempt(ctx context.Context, runID, taskID, attemptID, status string, finishedAt *time.Time) error {
	if err := s.ensureBuilt(ctx); err != nil {
		return err
	}

	payload, err := marshalAttemptEntry(taskID, attemptID, status, finishedAt)
	if err != nil {
		return fmt.Errorf("marshal attempt entry: %w", err)
	}

	storeEvt := s.newStoreEvent(runID, storageKindTaskAttempt, payload)

	if err := s.store.Append(ctx, storeEvt); err != nil {
		return fmt.Errorf("store append: %w", err)
	}

	// Update mem's internal state directly (same package) — append or update attempt.
	s.mem.mu.Lock()
	defer s.mem.mu.Unlock()
	rec, ok := s.mem.runs[runID]
	if !ok {
		return ErrNotFound
	}
	trec, ok := rec.tasks[taskID]
	if !ok {
		return ErrNotFound
	}
	for i := range trec.snapshot.Attempts {
		if trec.snapshot.Attempts[i].AttemptID == attemptID {
			trec.snapshot.Attempts[i].Status = status
			if finishedAt != nil {
				t := *finishedAt
				trec.snapshot.Attempts[i].FinishedAt = &t
			}
			return nil
		}
	}
	// Not found — append new attempt
	att := AttemptSnapshot{
		AttemptID: attemptID,
		Status:    status,
	}
	if finishedAt != nil {
		t := *finishedAt
		att.FinishedAt = &t
	}
	trec.snapshot.Attempts = append(trec.snapshot.Attempts, att)
	return nil
}

func (s *StorageLedgerRepository) CloseRun(ctx context.Context, runID string) error {
	if err := s.ensureBuilt(ctx); err != nil {
		return err
	}

	payload, err := marshalRunClosed()
	if err != nil {
		return fmt.Errorf("marshal run closed: %w", err)
	}

	storeEvt := s.newStoreEvent(runID, storageKindRunClosed, payload)

	if err := s.store.Append(ctx, storeEvt); err != nil {
		return fmt.Errorf("store append: %w", err)
	}

	// Also write a run_status_changed event for the status transition.
	now := s.now()
	statusPayload, err := marshalRunStatusChange(string(RunStatusCanceled), &now)
	if err != nil {
		return fmt.Errorf("marshal run status change: %w", err)
	}
	statusEvt := s.newStoreEvent(runID, storageKindRunStatusChanged, statusPayload)
	if err := s.store.Append(ctx, statusEvt); err != nil {
		return fmt.Errorf("store append status change: %w", err)
	}

	// Update mem's internal state directly (same package).
	if err := s.mem.CloseRun(ctx, runID); err != nil {
		return err
	}
	// Also update the run's status to canceled in the in-memory projection.
	s.mem.mu.Lock()
	if rec, ok := s.mem.runs[runID]; ok {
		rec.snapshot.Status = RunStatusCanceled
		rec.snapshot.CompletedAt = &now
	}
	s.mem.mu.Unlock()
	return nil
}

func (s *StorageLedgerRepository) DeleteRun(ctx context.Context, runID string) error {
	if err := s.ensureBuilt(ctx); err != nil {
		return err
	}
	return s.mem.DeleteRun(ctx, runID)
}

// ---------------------------------------------------------------------------
// Recovery
// ---------------------------------------------------------------------------

// RecoveredRun describes a run that was recovered from durable storage.
type RecoveredRun struct {
	RunID          string
	DisplayName    string
	Status         RunStatus
	WasInterrupted bool
}

// Recover scans the store for all runs, rebuilds the projection, and marks
// any run with a non-terminal status as interrupted.
func (s *StorageLedgerRepository) Recover(ctx context.Context) ([]RecoveredRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Always rebuild from scratch during recovery, even if built already.
	if err := s.rebuildLocked(ctx); err != nil {
		return nil, err
	}

	// List all runs from mem
	runs, err := s.mem.ListRuns(ctx)
	if err != nil {
		return nil, err
	}

	var recovered []RecoveredRun
	for _, r := range runs {
		wasInterrupted := r.Status == RunStatusRunning || r.Status == RunStatusQueued || r.Status == RunStatusCreated
		recovered = append(recovered, RecoveredRun{
			RunID:          r.RunID,
			DisplayName:    r.DisplayName,
			Status:         r.Status,
			WasInterrupted: wasInterrupted,
		})
	}

	return recovered, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

var storageEventIDCounter atomic.Uint64

func newStorageEventID() string {
	n := storageEventIDCounter.Add(1)
	return fmt.Sprintf("se-%d", n)
}

// Ensure StorageLedgerRepository implements LedgerRepository at compile time.
var _ LedgerRepository = (*StorageLedgerRepository)(nil)
