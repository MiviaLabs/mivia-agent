package ledger

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledgercore"
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
	store     storage.Store
	mem       *MemoryLedgerRepository
	engine    *ledgercore.Engine
	claims    *ledgercore.ClaimsTracker
	ownsStore bool
	mu        sync.RWMutex
	inflight  map[inflightKey]struct{}
}

// SetTimeSource replaces the clock for deterministic tests.
func (s *StorageLedgerRepository) SetTimeSource(now func() time.Time) {
	s.engine.SetTimeSource(now)
	s.mem.SetTimeSource(now)
}

// checkOpen returns ErrClosed if the repository has been closed.
func (s *StorageLedgerRepository) checkOpen() error {
	return s.engine.CheckOpen()
}

// nowLocked returns the current time using the repository's time source.
// Caller must hold at least s.mu read lock.
func (s *StorageLedgerRepository) nowLocked() time.Time {
	return s.engine.Now()
}

// Close closes the underlying store and marks the repository as closed.
// All claims held by this instance are released before closing the store.
// Subsequent method calls will return ErrClosed.
func (s *StorageLedgerRepository) Close() error {
	return s.engine.Close(context.Background())
}

// ensureBuilt brings the in-memory projection up to date with the store,
// applying any events appended since this instance last caught up - including
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
	if err := s.rebaseRunSequence(ctx, snapshot.RunID); err != nil {
		return err
	}

	// Persist the idempotency key with the run_created payload so a fresh
	// repository replaying the store re-registers it (finding F6). snapshot is
	// a value parameter: the write goes to the local copy, never the caller's
	// struct, and mem.CreateRun below receives the same keyed snapshot.
	snapshot.IdempotencyKey = key

	payload, err := marshalRunSnapshot(snapshot)
	if err != nil {
		return fmt.Errorf("marshal run snapshot: %w", err)
	}

	// Admission-time re-probe (PC-1): the deterministic "create-run:"+key ID
	// below is a durable same-key backstop, but it only collides with rows
	// written by this fix. A legacy run_created row written by a pre-fix
	// binary carries a RANDOM event ID with the key inside the payload, so it
	// slips past that backstop. mem.CreateRun below only reflects the store as
	// of the catch-up at the top of this method; an instance that committed
	// such a row since then would register the key unseen and this admission
	// would duplicate keyed work. Re-probe the store here so the projection
	// folds any newly committed keyed row, and mem.CreateRun then refuses on
	// the key. A post-fix writer racing in after this probe is still caught by
	// the deterministic ID collision at append time; a pre-fix writer racing
	// in then is the residual mixed-version window no read-side probe can
	// close.
	if key != "" {
		if err := s.ensureBuilt(ctx); err != nil {
			return err
		}
	}

	// Register in the in-memory projection BEFORE the durable append (D4) so a
	// durable row for a key can only exist after mem.CreateRun succeeded.
	if err := s.mem.CreateRun(ctx, key, snapshot); err != nil {
		return err
	}

	storeEvt := s.newStoreEvent(snapshot.RunID, storageKindRunCreated, payload)
	// Mint the run_created event ID deterministically from the idempotency key
	// when one is supplied (LEDGER-1). The per-instance mem.CreateRun check
	// above only dedups within one repository: two instances over the same
	// store (the documented shared-workspace deployment) both pass their
	// private projection and both commit. With a key-derived ID the store's
	// events.id PRIMARY KEY (SQLite) / global ID map (Memory) becomes the
	// durable same-key backstop: the second concurrent CreateRun collides on
	// the INSERT and surfaces as storage.ErrDuplicate, which the append-failure
	// path below already maps to ErrDuplicate and rolls back. Empty keys keep
	// the random newStorageEventID() so keyless runs coexist.
	if snapshot.IdempotencyKey != "" {
		storeEvt.ID = "create-run:" + snapshot.IdempotencyKey
	}
	if err := s.appendStoreEvent(ctx, storeEvt); err != nil {
		// Roll back the registration so a failed CreateRun leaves the key free
		// (mem.DeleteRun removes the run and its idemLookup entries).
		_ = s.mem.DeleteRun(ctx, snapshot.RunID)
		if errors.Is(err, storage.ErrDuplicate) || errors.Is(err, ErrDuplicate) {
			return ErrDuplicate
		}
		return fmt.Errorf("store append: %w", err)
	}

	return nil
}

func (s *StorageLedgerRepository) CreateTask(ctx context.Context, snap TaskSnapshot) error {
	if err := s.ensureBuilt(ctx); err != nil {
		return err
	}
	// Validate in the projection first: a rejected task (duplicate ID, unknown
	// or closed run) never reaches the store; a failed append rebuilds it.
	if err := s.mem.CreateTask(ctx, snap); err != nil {
		return err
	}
	payload, err := marshalTaskSnapshot(snap)
	if err != nil {
		_ = s.rebuildRunProjection(ctx, snap.RunID)
		return fmt.Errorf("marshal task snapshot: %w", err)
	}
	if err := s.appendStoreEventOrRebuild(ctx, s.newStoreEvent(snap.RunID, storageKindTaskCreated, payload)); err != nil {
		if errors.Is(err, storage.ErrDuplicate) || errors.Is(err, ErrDuplicate) {
			return ErrDuplicate
		}
		return fmt.Errorf("store append: %w", err)
	}
	return nil
}

func (s *StorageLedgerRepository) AppendEvent(ctx context.Context, event LifecycleEvent) error {
	if err := s.ensureBuilt(ctx); err != nil {
		return err
	}

	// Stamp before marshalling, not after. event is a value parameter, so this
	// mutates the local copy that BOTH marshalLifecycleEvent and s.mem.AppendEvent
	// receive - the durable payload and the live projection carry the same instant
	// by construction rather than by coincidence. Stamping afterwards (which is
	// what mem.AppendEvent used to do alone) left the stored copy holding a zero
	// timestamp forever, so every replayed event reported the replay instant.
	if event.CreatedAt.IsZero() {
		s.mu.RLock()
		event.CreatedAt = s.nowLocked()
		s.mu.RUnlock()
	}

	// Validate in the projection first: a rejected event (duplicate ID,
	// oversized payload, unknown run) never reaches the store; a failed
	// append rebuilds it.
	if err := s.mem.AppendEvent(ctx, event); err != nil {
		return err
	}
	payload, err := marshalLifecycleEvent(event)
	if err != nil {
		_ = s.rebuildRunProjection(ctx, event.RunID)
		return fmt.Errorf("marshal lifecycle event: %w", err)
	}
	if err := s.appendStoreEventOrRebuild(ctx, s.newStoreEvent(event.RunID, storageKindLifecycleEvent, payload)); err != nil {
		if err == storage.ErrDuplicate {
			return ErrDuplicate
		}
		return fmt.Errorf("store append: %w", err)
	}
	return nil
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
		_ = s.rebuildRunProjection(ctx, runID)
		return fmt.Errorf("marshal status change: %w", err)
	}
	// The fenced append failed (claim taken by another holder, or a transient
	// store error): the projection applied a status the durable history never
	// recorded, so reads must report only what the store holds.
	if err := s.appendStoreEventOrRebuild(ctx, s.newStoreEvent(runID, storageKindTaskStatusChanged, payload)); err != nil {
		return fmt.Errorf("store append: %w", err)
	}
	return nil
}

func (s *StorageLedgerRepository) SetTaskOutput(ctx context.Context, runID, taskID string, outputRef, errorRef, toolCallsRef string) error {
	if err := s.ensureBuilt(ctx); err != nil {
		return err
	}
	// Validate in the projection first: an unknown run or task never reaches
	// the store; a failed append rebuilds it.
	if err := s.mem.SetTaskOutput(ctx, runID, taskID, outputRef, errorRef, toolCallsRef); err != nil {
		return err
	}
	payload, err := marshalOutputRefs(taskID, outputRef, errorRef, toolCallsRef)
	if err != nil {
		_ = s.rebuildRunProjection(ctx, runID)
		return fmt.Errorf("marshal output refs: %w", err)
	}
	if err := s.appendStoreEventOrRebuild(ctx, s.newStoreEvent(runID, storageKindTaskOutputSet, payload)); err != nil {
		return fmt.Errorf("store append: %w", err)
	}
	return nil
}

func (s *StorageLedgerRepository) SetTaskAttempt(ctx context.Context, runID, taskID, attemptID, status string, finishedAt *time.Time) error {
	if err := s.ensureBuilt(ctx); err != nil {
		return err
	}
	// Validate and apply in the projection first via mem.SetTaskAttempt, which
	// enforces the closed-run guard (ErrClosed) and the missing run/task
	// checks (ErrNotFound) exactly as the memory repository does: a rejected
	// attempt never reaches the store, and a failed append rebuilds the
	// projection.
	if err := s.mem.SetTaskAttempt(ctx, runID, taskID, attemptID, status, finishedAt); err != nil {
		return err
	}
	payload, err := marshalAttemptEntry(taskID, attemptID, status, finishedAt)
	if err != nil {
		_ = s.rebuildRunProjection(ctx, runID)
		return fmt.Errorf("marshal attempt entry: %w", err)
	}
	if err := s.appendStoreEventOrRebuild(ctx, s.newStoreEvent(runID, storageKindTaskAttempt, payload)); err != nil {
		return fmt.Errorf("store append: %w", err)
	}
	return nil
}

// CloseRun marks a run as closed. The terminal transition (status canceled,
// completed_at) is marshalled INTO the single run_closed event payload, so the
// closure and the terminal status land in ONE fenced append: a store failure
// cannot leave a durable closure row beside a projection that still reports
// the run open, and no retry or concurrent writer can commit task transitions
// after the durable closure (DC-4). On append failure the projection is
// rebuilt from the store, so reads report only what is durable. Legacy
// run_closed rows without the optional fields decode empty and close through
// closeRebuiltRun, exactly as they always did.
func (s *StorageLedgerRepository) CloseRun(ctx context.Context, runID string) error {
	if err := s.ensureBuilt(ctx); err != nil {
		return err
	}

	// Validate and apply in the projection first (D2 pattern): an
	// already-closed run is refused with ErrInvalidTransition before any row
	// reaches the store (a double close writes nothing), and a failed append
	// rebuilds the projection from the store, rolling the closed flag and the
	// terminal status back to what the durable history actually holds.
	if err := s.mem.CloseRun(ctx, runID); err != nil {
		return err
	}
	s.mu.RLock()
	now := s.nowLocked()
	s.mu.RUnlock()
	s.mem.mu.Lock()
	if rec, ok := s.mem.runs[runID]; ok {
		rec.snapshot.Status = RunStatusCanceled
		rec.snapshot.CompletedAt = &now
	}
	s.mem.mu.Unlock()

	payload, err := marshalRunClosed(string(RunStatusCanceled), &now)
	if err != nil {
		_ = s.rebuildRunProjection(ctx, runID)
		return fmt.Errorf("marshal run closed: %w", err)
	}
	if err := s.appendStoreEventOrRebuild(ctx, s.newStoreEvent(runID, storageKindRunClosed, payload)); err != nil {
		return fmt.Errorf("store append: %w", err)
	}
	return nil
}

func (s *StorageLedgerRepository) DeleteRun(ctx context.Context, runID string) error {
	if err := s.ensureBuilt(ctx); err != nil {
		return err
	}
	if _, err := s.mem.GetRun(ctx, runID); err != nil {
		return err
	}
	tombstone := s.newStoreEvent(runID, storageKindRunDeleted, []byte(`{"run_id":"`+runID+`"}`))
	claim, _ := s.claims.GetClaim(runID)
	if err := s.store.AppendAndDeleteRun(ctx, tombstone, claim); err != nil {
		s.clearInflight(runID, uint64(tombstone.Sequence))
		if rebuildErr := s.rebuildRunProjection(ctx, runID); rebuildErr != nil {
			return fmt.Errorf("store delete run %q: %v; rebuild projection: %w", runID, err, rebuildErr)
		}
		if errors.Is(err, storage.ErrClaimHeld) {
			return ErrClaimHeld
		}
		return fmt.Errorf("store delete run %q: %w", runID, err)
	}
	// A concurrent catch-up (applyTail -> applyStoreEventLocked) may already
	// have folded the run_deleted tombstone into the projection and dropped
	// the run, so mem.DeleteRun can report ErrNotFound even though the store
	// commit succeeded. Tolerate it exactly like applyStoreEventLocked and
	// rebuildRunProjection already tolerate ErrNotFound from mem.DeleteRun:
	// the mandatory claimedRuns/applied/allocated/inflight cleanup below must
	// still run, or the stale fenced claim blocks a same-ID recreation with
	// ErrClaimHeld forever on this instance.
	if err := s.mem.DeleteRun(ctx, runID); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	s.claims.DropClaim(runID)
	s.mu.Lock()
	s.engine.Watermarks().DeleteRun(runID)
	for key := range s.inflight {
		if key.runID == runID {
			delete(s.inflight, key)
		}
	}
	s.mu.Unlock()
	return nil
}

// Ensure StorageLedgerRepository implements LedgerRepository at compile time.
var _ LedgerRepository = (*StorageLedgerRepository)(nil)
