package ledger

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// StorageLedgerRepository wraps a storage.Store as a durable LedgerRepository.
// Every mutation writes an event to the append-only store AND updates an
// in-memory projection for fast reads.
//
// The projection is not a one-shot build. Each operation first catches up on
// events appended by *other* repository instances over the same store, so two
// processes sharing one workspace observe each other's writes. Catch-up is
// incremental: a per-run applied-sequence watermark bounds the tail read to
// the events that arrived since this instance last looked.
type StorageLedgerRepository struct {
	store  storage.Store
	mem    *MemoryLedgerRepository
	mu     sync.RWMutex
	closed bool
	// applied is the highest store sequence per run that has been folded into
	// the in-memory projection. It replaces the old one-shot `built` flag.
	applied map[string]uint64
	// allocated is the highest sequence per run this instance has handed out
	// for its own appends. Kept separate from applied so that a failed append
	// (for example a sequence lost to a concurrent writer) cannot mark a
	// foreign event as already applied.
	allocated map[string]uint64
	// inflight holds sequences minted by this instance whose append has not
	// resolved yet. Catch-up skips them: the writer publishes its own events.
	inflight map[inflightKey]struct{}
	// cursor is the store append position this instance has already probed.
	// It makes the freshness check constant-time when nothing has changed.
	cursor uint64
	now    func() time.Time
}

// NewStorageLedgerRepository creates a StorageLedgerRepository backed by the
// given store. The in-memory projection is built lazily on first access and
// refreshed incrementally afterwards.
func NewStorageLedgerRepository(store storage.Store) *StorageLedgerRepository {
	return &StorageLedgerRepository{
		store:     store,
		mem:       NewMemoryLedgerRepository(),
		applied:   make(map[string]uint64),
		allocated: make(map[string]uint64),
		inflight:  make(map[inflightKey]struct{}),
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

// checkOpen returns ErrClosed if the repository has been closed.
func (s *StorageLedgerRepository) checkOpen() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return ErrClosed
	}
	return nil
}

// nowLocked returns the current time using the repository's time source.
// Caller must hold at least s.mu read lock.
func (s *StorageLedgerRepository) nowLocked() time.Time {
	return s.now()
}

// Close closes the underlying store and marks the repository as closed.
// Subsequent method calls will return ErrClosed.
func (s *StorageLedgerRepository) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return s.store.Close()
}

// ensureBuilt brings the in-memory projection up to date with the store,
// applying any events appended since this instance last caught up — including
// events written by another repository instance over the same store. It also
// checks that the repository has not been closed. Safe for concurrent calls.
//
// Cost: one constant-time store probe per call, plus a bounded tail read per
// run that actually moved. When nothing has changed (the single-process steady
// state) no event rows are read at all.
func (s *StorageLedgerRepository) ensureBuilt(ctx context.Context) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	return s.catchUp(ctx)
}

// parseSuffixNum extracts a numeric suffix from s after prefix.
// E.g. parseSuffixNum("se-42", "se-") returns 42.
// Returns 0 if the suffix is not a valid number.
func parseSuffixNum(s, prefix string) uint64 {
	if !strings.HasPrefix(s, prefix) {
		return 0
	}
	n, err := strconv.ParseUint(s[len(prefix):], 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// newStoreEvent creates a storage.Event with the next sequence number for the run.
// Safe for concurrent use.
func (s *StorageLedgerRepository) newStoreEvent(runID, kind string, payload []byte) storage.Event {
	return storage.Event{
		ID:       newStorageEventID(),
		RunID:    runID,
		Sequence: int(s.nextSequence(runID)),
		Kind:     kind,
		Payload:  payload,
	}
}

// ---------------------------------------------------------------------------
// LedgerRepository implementation
// ---------------------------------------------------------------------------

func (s *StorageLedgerRepository) CreateRun(ctx context.Context, key string, snapshot RunSnapshot) error {
	if snapshot.RunID == "" {
		return fmt.Errorf("storage ledger: empty run ID")
	}
	if err := s.ensureBuilt(ctx); err != nil {
		return err
	}

	payload, err := marshalRunSnapshot(snapshot)
	if err != nil {
		return fmt.Errorf("marshal run snapshot: %w", err)
	}

	storeEvt := s.newStoreEvent(snapshot.RunID, storageKindRunCreated, payload)
	if err := s.appendStoreEvent(ctx, storeEvt); err != nil {
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

	if err := s.appendStoreEvent(ctx, storeEvt); err != nil {
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
	if err := s.appendStoreEvent(ctx, storeEvt); err != nil {
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
		s.mu.RLock()
		t := s.nowLocked()
		s.mu.RUnlock()
		completedAt = &t
	}

	payload, err := marshalStatusChange(taskID, newStatus, expectedVersion+1, completedAt)
	if err != nil {
		return fmt.Errorf("marshal status change: %w", err)
	}

	storeEvt := s.newStoreEvent(runID, storageKindTaskStatusChanged, payload)

	if err := s.appendStoreEvent(ctx, storeEvt); err != nil {
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

	if err := s.appendStoreEvent(ctx, storeEvt); err != nil {
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

	if err := s.appendStoreEvent(ctx, storeEvt); err != nil {
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
		AttemptID:  attemptID,
		TaskID:     taskID,
		RunID:      runID,
		AttemptNum: len(trec.snapshot.Attempts) + 1,
		Status:     status,
	}
	if finishedAt != nil {
		t := *finishedAt
		att.FinishedAt = &t
	}
	trec.snapshot.Attempts = append(trec.snapshot.Attempts, att)
	return nil
}

// CloseRun marks a run as closed, writing both a run_closed event and a
// run_status_changed (→canceled) event. The dual events simplify recovery:
// the run_closed event signals storage-level closure, while the status
// change allows the projection rebuild to derive the correct terminal state.
// Callers that want cancel-only semantics should use CancelRun (if added)
// or transition task statuses before calling CloseRun.
func (s *StorageLedgerRepository) CloseRun(ctx context.Context, runID string) error {
	if err := s.ensureBuilt(ctx); err != nil {
		return err
	}

	payload, err := marshalRunClosed()
	if err != nil {
		return fmt.Errorf("marshal run closed: %w", err)
	}

	storeEvt := s.newStoreEvent(runID, storageKindRunClosed, payload)

	if err := s.appendStoreEvent(ctx, storeEvt); err != nil {
		return fmt.Errorf("store append: %w", err)
	}

	// Also write a run_status_changed event for the status transition.
	s.mu.RLock()
	now := s.nowLocked()
	s.mu.RUnlock()
	statusPayload, err := marshalRunStatusChange(string(RunStatusCanceled), &now)
	if err != nil {
		return fmt.Errorf("marshal run status change: %w", err)
	}
	statusEvt := s.newStoreEvent(runID, storageKindRunStatusChanged, statusPayload)
	if err := s.appendStoreEvent(ctx, statusEvt); err != nil {
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
// Helpers
// ---------------------------------------------------------------------------

var storageEventIDCounter atomic.Uint64

func newStorageEventID() string {
	n := storageEventIDCounter.Add(1)
	return fmt.Sprintf("se-%d", n)
}

// advanceStorageEventIDCounter raises the process-local event ID counter to at
// least n, so IDs minted after a restart cannot collide with replayed ones.
func advanceStorageEventIDCounter(n uint64) {
	for {
		cur := storageEventIDCounter.Load()
		if n <= cur {
			return
		}
		if storageEventIDCounter.CompareAndSwap(cur, n) {
			return
		}
	}
}

// Ensure StorageLedgerRepository implements LedgerRepository at compile time.
var _ LedgerRepository = (*StorageLedgerRepository)(nil)
