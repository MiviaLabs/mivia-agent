package ledger

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// StorageRepository is the durable Repository implementation, event-sourced
// over a shared storage.Store (the same instance the coordinator uses — same
// SQLite file, same content-addressed content table, same run_claims table).
// It is a NON-OWNING user of the store: Close() releases only the claims this
// instance holds and never closes the borrowed store.
//
// Namespace rules (HARD): workflow run IDs are prefixed wfr- (never colliding
// with the coordinator's run- IDs, so the coordinator's DeleteRun can never
// touch a workflow run); event IDs are deterministic wfe:<hex(run)>:... ; event
// kinds are wf_* (the coordinator's projection ignores them and advances its
// watermark). During catch-up this repository reads foreign runs' events as
// no-ops and advances its own watermark for them.
//
// Concurrency: every mutation is serialized per run (per-run mutex) and is
// performed under the run's execution claim (caller-held; ClaimRun fences
// cross-process writers). Mutations apply to the in-memory projection first,
// append the event, and on append failure ROLL BACK the projection mutation
// then catch up before returning.
//
// Recovery semantics: an interrupted synthetic workflow resumes with the same
// snapshot and one complete audit trail. Recover classifies runs and clears
// stale claims only on terminal runs (a run whose derived active step is a
// reserved terminal step is classified terminal even without a status CAS).
type StorageRepository struct {
	store storage.Store
	mu    sync.RWMutex
	// closed gates every public method: after Close, all calls return ErrClosed.
	closed bool
	// holder is a random per-process identifier for run execution claims.
	// It identifies which process (repository instance) holds a run claim.
	holder string
	// claimedRuns tracks the runs this instance has claimed (runID → holder),
	// so Close() can release them all (simulating crash cleanup).
	claimedRuns map[string]string
	// runLocks serializes mutations per run (intra-process). Cross-process
	// writers are fenced by the execution claim (ClaimRun).
	runLocks map[string]*sync.Mutex
	// now is the swappable clock (SetTimeSource) used for every stamped
	// timestamp; persisted in event payloads, never derived at read time.
	now func() time.Time

	// applied is the highest store sequence per run folded into the cached
	// projection. It bounds catch-up to the runs that actually moved.
	applied map[string]uint64
	// allocated is the highest sequence per run this instance has handed out
	// for its own appends, kept separate from applied so a failed append
	// cannot mark a foreign event as already applied.
	allocated map[string]uint64
	// cursor is the store append position this instance has already probed.
	// It makes the freshness check constant-time when nothing has changed.
	cursor uint64
	// proj is the in-memory projection cache, one entry per run that has wf
	// events. Entries are rebuilt from the store during catch-up and mutated
	// in place by successful mutations.
	proj map[string]Projection
	// deliverySeqs tracks, per (run, idempotency key), how many
	// wf_delivery_upserted events the store holds, so each upsert mints a
	// deterministic event ID (the store's id PRIMARY KEY is then the backstop
	// for the same logical upsert retried with the same payload).
	deliverySeqs map[deliveryKey]int
}

// NewStorageRepository wraps a shared storage.Store (non-owning).
func NewStorageRepository(store storage.Store) *StorageRepository {
	return &StorageRepository{
		store:        store,
		holder:       newHolderID(),
		claimedRuns:  make(map[string]string),
		runLocks:     make(map[string]*sync.Mutex),
		now:          time.Now,
		applied:      make(map[string]uint64),
		allocated:    make(map[string]uint64),
		proj:         make(map[string]Projection),
		deliverySeqs: make(map[deliveryKey]int),
	}
}

// NewMemoryRepository returns a repository over a fresh in-memory store.
func NewMemoryRepository() *StorageRepository {
	return NewStorageRepository(storage.NewMemory())
}

// SetTimeSource replaces the clock for deterministic tests.
func (s *StorageRepository) SetTimeSource(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if now != nil {
		s.now = now
	}
}

// Close releases claims held by this instance and marks the repository closed
// (subsequent calls return ErrClosed). It never closes the borrowed store.
func (s *StorageRepository) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	claims := make(map[string]string, len(s.claimedRuns))
	for runID, holder := range s.claimedRuns {
		claims[runID] = holder
	}
	s.claimedRuns = make(map[string]string)
	s.mu.Unlock()

	// Release all claims held by this instance (crash cleanup simulation).
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for runID, holder := range claims {
		_ = s.store.ReleaseClaim(ctx, runID, holder)
	}
	return nil
}

// runLock returns the per-run mutex for runID, creating it on first use.
func (s *StorageRepository) runLock(runID string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.runLocks[runID]
	if !ok {
		l = &sync.Mutex{}
		s.runLocks[runID] = l
	}
	return l
}

// ---------------------------------------------------------------------------
// Catch-up / projection refresh
// ---------------------------------------------------------------------------

// ensureBuilt brings the in-memory projection up to date with the store,
// applying any events appended since this instance last caught up — including
// events written by another repository instance over the same store. It also
// checks that the repository has not been closed. Safe for concurrent calls.
func (s *StorageRepository) ensureBuilt(ctx context.Context) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	return s.catchUp(ctx)
}

// catchUp probes the store for runs that moved since this instance's cursor
// and rebuilds their projections from the full event log. Store I/O happens
// without s.mu held; the projection commit takes the write lock. Each moved
// run is rebuilt under its per-run mutex so a rebuild can never interleave
// with an in-process mutation of the same run (which would lose the mutation
// or apply a stale projection).
func (s *StorageRepository) catchUp(ctx context.Context) error {
	s.mu.RLock()
	cursor := s.cursor
	s.mu.RUnlock()

	maxSequences, newCursor, err := s.store.Changes(ctx, cursor)
	if err != nil {
		return fmt.Errorf("read store changes: %w", err)
	}

	s.mu.RLock()
	var behind []string
	for runID, maxSeq := range maxSequences {
		if uint64(maxSeq) > s.applied[runID] {
			behind = append(behind, runID)
		}
	}
	s.mu.RUnlock()

	if len(behind) == 0 {
		// Nothing to apply: the changes the probe covered were this instance's
		// own writes, already in the projection.
		s.advanceCursor(newCursor)
		return nil
	}
	sort.Strings(behind)

	for _, runID := range behind {
		lock := s.runLock(runID)
		lock.Lock()
		// A run this instance has never read that does NOT carry the wfr-
		// prefix (the coordinator's run-* runs sharing the same SQLite file)
		// can never hold wf events under the HARD namespace rule, so reading
		// and discarding its log on every op is pure waste. Skip it: advance
		// the applied watermark to the reported max sequence and move on.
		// wfr- runs are ALWAYS read (their first read rebuilds the run), and
		// already-read runs keep the full-read path below.
		s.mu.Lock()
		skip := s.applied[runID] == 0 && !strings.HasPrefix(runID, "wfr-")
		if skip {
			if uint64(maxSequences[runID]) > s.applied[runID] {
				s.applied[runID] = uint64(maxSequences[runID])
			}
		}
		s.mu.Unlock()
		if skip {
			lock.Unlock()
			continue
		}
		err := s.catchUpRunLocked(ctx, runID)
		lock.Unlock()
		if err != nil {
			return err
		}
	}
	s.advanceCursor(newCursor)
	return nil
}

// advanceCursor moves the store cursor forward. It never rewinds, so a slow
// concurrent catch-up cannot undo a newer one.
func (s *StorageRepository) advanceCursor(cursor uint64) {
	s.mu.Lock()
	if cursor > s.cursor {
		s.cursor = cursor
	}
	s.mu.Unlock()
}

// catchUpRunLocked rebuilds one run's cached projection from the store's full
// event log. It must be called with the run's per-run mutex held (either from
// catchUp or from a mutation's error path, which holds the lock already).
func (s *StorageRepository) catchUpRunLocked(ctx context.Context, runID string) error {
	events, err := s.store.Events(ctx, runID)
	if err != nil {
		return fmt.Errorf("read events for %s: %w", runID, err)
	}
	return s.applyRunEventsLocked(ctx, runID, events)
}

// applyRunEventsLocked folds a run's full event log into the cached
// projection: rebuild via RebuildProjection (deterministic, ordered by
// RowID/Sequence), advance the applied watermark past EVERY event read
// (including foreign runs with unknown kinds, so their logs are never
// re-read), and recompute the run's delivery upsert ordinals.
func (s *StorageRepository) applyRunEventsLocked(ctx context.Context, runID string, events []storage.Event) error {
	var maxSeq uint64
	for _, ev := range events {
		if uint64(ev.Sequence) > maxSeq {
			maxSeq = uint64(ev.Sequence)
		}
	}

	proj, err := RebuildProjection(events)
	if err != nil {
		return fmt.Errorf("rebuild projection for %s: %w", runID, err)
	}

	deliveryCounts := make(map[string]int)
	for _, ev := range events {
		if ev.Kind != eventKindDeliveryUpserted {
			continue
		}
		p, err := unmarshalDeliveryUpserted(ev.Payload)
		if err != nil {
			return fmt.Errorf("decode %s payload: %w", ev.Kind, err)
		}
		deliveryCounts[p.Delivery.IdempotencyKey]++
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if proj.HasRun {
		s.proj[runID] = proj
	} else {
		// Foreign run (no wf events): drop any stale entry; the watermark
		// advance below keeps us from ever re-reading its log.
		delete(s.proj, runID)
	}
	if maxSeq > s.applied[runID] {
		s.applied[runID] = maxSeq
	}
	for k := range s.deliverySeqs {
		if k.runID == runID {
			delete(s.deliverySeqs, k)
		}
	}
	for key, n := range deliveryCounts {
		s.deliverySeqs[deliveryKey{runID: runID, key: key}] = n
	}
	return nil
}

// rebaseRunSequence preserves sequence monotonicity when a run ID is
// recreated: on CreateRun, seed applied/allocated past the highest sequence
// already in the store so the first event of the new incarnation continues
// after it rather than minting sequence one.
func (s *StorageRepository) rebaseRunSequence(ctx context.Context, runID string) error {
	events, err := s.store.Events(ctx, runID)
	if err != nil {
		return fmt.Errorf("read existing events for %s: %w", runID, err)
	}
	var maxSeq uint64
	for _, ev := range events {
		if uint64(ev.Sequence) > maxSeq {
			maxSeq = uint64(ev.Sequence)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if maxSeq > s.applied[runID] {
		s.applied[runID] = maxSeq
	}
	if maxSeq > s.allocated[runID] {
		s.allocated[runID] = maxSeq
	}
	return nil
}

// nextSequence returns the next sequence number for a run and records the
// allocation. It starts from the higher of the applied watermark (what the
// store is known to hold) and this instance's own previous allocations, and
// is called under the run's per-run mutex. The allocation guards sequence
// reuse in-process: the per-run mutex plus the full-log rebuild on catch-up
// already make double-apply impossible, and allocated keeps a failed append
// from ever reusing a sequence that was already handed out.
func (s *StorageRepository) nextSequence(runID string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.applied[runID]
	if s.allocated[runID] > next {
		next = s.allocated[runID]
	}
	next++
	s.allocated[runID] = next
	return next
}

// appendEvent writes the event to the store and resolves the writer's
// bookkeeping. It must be called with the run's per-run mutex held.
//
// On success it advances the applied watermark past the event's sequence so
// catch-up never re-reads this instance's own writes.
//
// On ErrDuplicate (the store's id PRIMARY KEY or the (run_id, sequence)
// UNIQUE constraint) it rebuilds this run's projection from the store, then
// compares the stored event with the SAME ID: a byte-identical payload means
// an idempotent retry (nil); a different payload means the logical key was
// taken by a concurrent writer (ErrConflict).
//
// On any other append error it rolls back the projection mutation (rollback
// runs with s.mu held) and rebuilds the run's projection from the store so
// the in-memory state matches durable state before returning.
func (s *StorageRepository) appendEvent(ctx context.Context, evt storage.Event, rollback func()) error {
	err := s.store.Append(ctx, evt)

	s.mu.Lock()
	if err == nil && uint64(evt.Sequence) > s.applied[evt.RunID] {
		s.applied[evt.RunID] = uint64(evt.Sequence)
	}
	s.mu.Unlock()

	if err == nil {
		return nil
	}

	if !errors.Is(err, storage.ErrDuplicate) {
		if rollback != nil {
			s.mu.Lock()
			rollback()
			s.mu.Unlock()
		}
		if cerr := s.catchUpRunLocked(ctx, evt.RunID); cerr != nil {
			return fmt.Errorf("store append: %v; catch up: %w", err, cerr)
		}
		return fmt.Errorf("store append: %w", err)
	}

	// Duplicate: catch up (the rebuild replaces the projection, discarding
	// this writer's in-place mutation) and compare the existing event.
	if cerr := s.catchUpRunLocked(ctx, evt.RunID); cerr != nil {
		return fmt.Errorf("catch up after duplicate: %w", cerr)
	}
	events, rerr := s.store.Events(ctx, evt.RunID)
	if rerr != nil {
		return fmt.Errorf("read events after duplicate: %w", rerr)
	}
	for _, e := range events {
		if e.ID == evt.ID {
			if bytes.Equal(e.Payload, evt.Payload) {
				return nil
			}
			return ErrConflict
		}
	}
	// The duplicate came from the (run_id, sequence) UNIQUE constraint with
	// no event carrying our ID: the sequence was lost to another writer.
	return ErrConflict
}

// rollbackAndRebuild rolls back the projection mutation and rebuilds the
// run's projection from the store. Must be called with the run's per-run
// mutex held (e.g. after a marshal failure, before any event was appended).
func (s *StorageRepository) rollbackAndRebuild(ctx context.Context, runID string, rollback func()) {
	if rollback != nil {
		s.mu.Lock()
		rollback()
		s.mu.Unlock()
	}
	_ = s.catchUpRunLocked(ctx, runID)
}

// ClaimRun acquires the exclusive execution claim on a run. Returns
// ErrClaimHeld if another holder owns it. Same-holder refresh succeeds.
func (s *StorageRepository) ClaimRun(ctx context.Context, runID, holder string) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	if err := s.store.ClaimRun(ctx, runID, holder); err != nil {
		if errors.Is(err, storage.ErrClaimHeld) {
			return ErrClaimHeld
		}
		return err
	}
	s.mu.Lock()
	s.claimedRuns[runID] = holder
	s.mu.Unlock()
	return nil
}

// ReleaseRun releases the claim; only the current holder may. Returns
// ErrClaimNotHeld otherwise.
func (s *StorageRepository) ReleaseRun(ctx context.Context, runID, holder string) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	if err := s.store.ReleaseClaim(ctx, runID, holder); err != nil {
		if errors.Is(err, storage.ErrClaimNotHeld) {
			return ErrClaimNotHeld
		}
		return err
	}
	s.mu.Lock()
	delete(s.claimedRuns, runID)
	s.mu.Unlock()
	return nil
}

// ClearRunClaim force-releases any claim regardless of holder (explicit
// operator force-release for stale claims; Recover clears claims only on
// terminal runs).
func (s *StorageRepository) ClearRunClaim(ctx context.Context, runID string) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	if err := s.store.ClearClaim(ctx, runID); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.claimedRuns, runID)
	s.mu.Unlock()
	return nil
}

// StoreContent persists bytes under a content-addressed reference
// (shared content store; idempotent).
func (s *StorageRepository) StoreContent(ctx context.Context, ref string, data []byte) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	return s.store.PutContent(ctx, ref, data)
}

// LoadContent retrieves stored bytes. Returns ErrContentNotFound if absent.
func (s *StorageRepository) LoadContent(ctx context.Context, ref string) ([]byte, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	data, err := s.store.GetContent(ctx, ref)
	if err != nil {
		if errors.Is(err, storage.ErrContentNotFound) {
			return nil, ErrContentNotFound
		}
		return nil, err
	}
	return data, nil
}

// Ensure StorageRepository implements Repository at compile time.
var _ Repository = (*StorageRepository)(nil)
