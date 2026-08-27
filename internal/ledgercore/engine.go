package ledgercore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// Engine coordinates event-sourced ledger storage operations: claim tracking,
// watermark progression, sequence allocation, concurrency serialization, and
// durable claim-fenced appends.
type Engine struct {
	store      storage.Store
	ownsStore  bool
	claims     *ClaimsTracker
	watermarks *WatermarkTracker
	runLocks   map[string]*sync.Mutex
	mu         sync.RWMutex
	closed     bool
	now        func() time.Time
}

// NewEngine initializes a new ledger Engine over store.
func NewEngine(store storage.Store, ownsStore bool, holder string) *Engine {
	return &Engine{
		store:      store,
		ownsStore:  ownsStore,
		claims:     NewClaimsTracker(holder),
		watermarks: NewWatermarkTracker(),
		runLocks:   make(map[string]*sync.Mutex),
		now:        time.Now,
	}
}

// Store returns the underlying storage.Store.
func (e *Engine) Store() storage.Store {
	return e.store
}

// SetTimeSource configures a custom clock for deterministic testing.
func (e *Engine) SetTimeSource(now func() time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if now != nil {
		e.now = now
	}
}

// Now returns the current time using the configured time source.
func (e *Engine) Now() time.Time {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.now()
}

// CheckOpen returns ErrClosed if the engine has been closed.
func (e *Engine) CheckOpen() error {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.closed {
		return ErrClosed
	}
	return nil
}

// IsClosed reports whether the engine has been closed.
func (e *Engine) IsClosed() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.closed
}

// Claims returns the associated ClaimsTracker.
func (e *Engine) Claims() *ClaimsTracker {
	return e.claims
}

// Watermarks returns the associated WatermarkTracker.
func (e *Engine) Watermarks() *WatermarkTracker {
	return e.watermarks
}

// RunLock returns the mutex that serializes operations on runID.
func (e *Engine) RunLock(runID string) *sync.Mutex {
	e.mu.Lock()
	defer e.mu.Unlock()
	lock, ok := e.runLocks[runID]
	if !ok {
		lock = &sync.Mutex{}
		e.runLocks[runID] = lock
	}
	return lock
}

// NextSequence generates and records the next monotonic sequence for runID.
func (e *Engine) NextSequence(runID string) uint64 {
	return e.watermarks.NextSequence(runID)
}

// RebaseRunSequence reads existing events for runID from store and updates watermarks.
func (e *Engine) RebaseRunSequence(ctx context.Context, runID string) error {
	return e.watermarks.RebaseRunSequence(ctx, e.store, runID)
}

// Close closes the engine, releases active claims, and closes the store if owned.
func (e *Engine) Close(ctx context.Context) error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	e.mu.Unlock()

	relCtx := ctx
	var cancel context.CancelFunc
	if relCtx == nil {
		relCtx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
	}

	_ = e.claims.Close(relCtx, e.store)

	if e.ownsStore {
		return e.store.Close()
	}
	return nil
}

// FilterDecision indicates how CatchUp should handle a behind run.
type FilterDecision int

const (
	// FilterApply indicates the run should be locked, fetched, and rebuilt.
	FilterApply FilterDecision = iota
	// FilterSkip indicates the run should be skipped without updating its watermark.
	FilterSkip
	// FilterAdvanceOnly indicates the watermark should advance without reading events.
	FilterAdvanceOnly
)

// CatchUp performs incremental catch-up by probing store changes and invoking rebuildRun.
func (e *Engine) CatchUp(
	ctx context.Context,
	filterRun func(runID string, maxSeq int) FilterDecision,
	rebuildRun func(ctx context.Context, runID string, events []storage.Event) error,
) error {
	if err := e.CheckOpen(); err != nil {
		return err
	}

	cursor := e.watermarks.Cursor()
	maxSequences, newCursor, err := e.store.Changes(ctx, cursor)
	if err != nil {
		return fmt.Errorf("read store changes: %w", err)
	}

	behind := e.watermarks.CheckBehind(maxSequences)
	if len(behind) == 0 {
		e.watermarks.AdvanceCursor(newCursor)
		return nil
	}

	for _, runID := range behind {
		maxSeq := maxSequences[runID]
		if filterRun != nil {
			switch filterRun(runID, maxSeq) {
			case FilterSkip:
				continue
			case FilterAdvanceOnly:
				e.watermarks.SetApplied(runID, uint64(maxSeq))
				continue
			}
		}

		lock := e.RunLock(runID)
		lock.Lock()
		events, err := e.store.Events(ctx, runID)
		if err != nil {
			lock.Unlock()
			return fmt.Errorf("read events for %s: %w", runID, err)
		}
		if rebuildRun != nil {
			if err := rebuildRun(ctx, runID, events); err != nil {
				lock.Unlock()
				return err
			}
		}
		e.watermarks.SetApplied(runID, uint64(maxSeq))
		lock.Unlock()
	}

	e.watermarks.AdvanceCursor(newCursor)
	return nil
}

// AppendOptions controls error handling and duplicate resolution during event append.
type AppendOptions struct {
	BoundHolder string
	Rollback    func()
	RebuildRun  func(ctx context.Context, runID string) error
	OnDuplicate func(ctx context.Context, evt storage.Event) error
}

// AppendEvent writes evt to store with claim fencing, updating watermarks on success.
func (e *Engine) AppendEvent(ctx context.Context, evt storage.Event, opts AppendOptions) error {
	holder := opts.BoundHolder
	claim, _ := e.claims.GetClaim(evt.RunID)
	if holder == "" {
		holder = claim.Holder
	}

	var err error
	if fenced, ok := e.store.(storage.FencedLeaseStore); ok && claim.Fence != 0 && (opts.BoundHolder == "" || claim.Holder == opts.BoundHolder) {
		err = fenced.AppendClaimedFenced(ctx, evt, claim)
	} else {
		err = e.store.AppendClaimed(ctx, evt, holder)
	}
	if errors.Is(err, storage.ErrClaimHeld) {
		err = ErrClaimHeld
	}

	if err == nil {
		e.watermarks.SetApplied(evt.RunID, uint64(evt.Sequence))
		return nil
	}

	if errors.Is(err, storage.ErrDuplicate) {
		if opts.OnDuplicate != nil {
			return opts.OnDuplicate(ctx, evt)
		}
		if opts.RebuildRun != nil {
			_ = opts.RebuildRun(ctx, evt.RunID)
		}
		return ErrDuplicate
	}

	if opts.Rollback != nil {
		opts.Rollback()
	}
	if opts.RebuildRun != nil {
		if rerr := opts.RebuildRun(ctx, evt.RunID); rerr != nil {
			return fmt.Errorf("store append: %v; rebuild projection: %w", err, rerr)
		}
	}
	return fmt.Errorf("store append: %w", err)
}

// CheckDuplicatePayload verifies whether a duplicate event has identical payload.
func (e *Engine) CheckDuplicatePayload(ctx context.Context, evt storage.Event) error {
	events, err := e.store.Events(ctx, evt.RunID)
	if err != nil {
		return fmt.Errorf("read events after duplicate: %w", err)
	}
	for _, stored := range events {
		if stored.ID == evt.ID {
			if bytes.Equal(stored.Payload, evt.Payload) {
				return nil
			}
			return ErrConflict
		}
	}
	return ErrConflict
}

// CatchUpSince probes store changes and fetches new events using EventsSince
// for all runs behind their applied watermark, passing the merged tail to applyTail.
func (e *Engine) CatchUpSince(
	ctx context.Context,
	filterRun func(runID string, maxSeq int) FilterDecision,
	applyTail func(ctx context.Context, events []storage.Event) error,
) error {
	if err := e.CheckOpen(); err != nil {
		return err
	}

	cursor := e.watermarks.Cursor()
	maxSequences, newCursor, err := e.store.Changes(ctx, cursor)
	if err != nil {
		return fmt.Errorf("read store changes: %w", err)
	}

	behind := e.watermarks.CheckBehind(maxSequences)
	if len(behind) == 0 {
		e.watermarks.AdvanceCursor(newCursor)
		return nil
	}

	var pending []storage.Event
	for _, runID := range behind {
		maxSeq := maxSequences[runID]
		if filterRun != nil {
			switch filterRun(runID, maxSeq) {
			case FilterSkip:
				continue
			case FilterAdvanceOnly:
				e.watermarks.SetApplied(runID, uint64(maxSeq))
				continue
			}
		}
		from := e.watermarks.Applied(runID)
		events, err := e.store.EventsSince(ctx, runID, int(from))
		if err != nil {
			return fmt.Errorf("read events for %s: %w", runID, err)
		}
		pending = append(pending, events...)
	}
	if len(pending) == 0 {
		e.watermarks.AdvanceCursor(newCursor)
		return nil
	}
	if applyTail != nil {
		if err := applyTail(ctx, pending); err != nil {
			return err
		}
	}
	e.watermarks.AdvanceCursor(newCursor)
	return nil
}

// SortEvents sorts events in-place into global store order (RowID ascending, then Sequence ascending).
func SortEvents(events []storage.Event) {
	sort.Slice(events, func(i, j int) bool {
		if events[i].RowID != events[j].RowID {
			return events[i].RowID < events[j].RowID
		}
		return events[i].Sequence < events[j].Sequence
	})
}

// SortEventsStable stably sorts events in-place into global store order (RowID ascending, then Sequence ascending).
func SortEventsStable(events []storage.Event) {
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].RowID != events[j].RowID {
			return events[i].RowID < events[j].RowID
		}
		return events[i].Sequence < events[j].Sequence
	})
}

// ClaimRun acquires an exclusive run execution claim on store.
func (e *Engine) ClaimRun(ctx context.Context, runID, holder string) error {
	if err := e.CheckOpen(); err != nil {
		return err
	}
	return e.claims.ClaimRun(ctx, e.store, runID, holder)
}

// RefreshRunClaim refreshes the claim's acquired_at only if already held.
func (e *Engine) RefreshRunClaim(ctx context.Context, runID, holder string) error {
	if err := e.CheckOpen(); err != nil {
		return err
	}
	return e.claims.RefreshRunClaim(ctx, e.store, runID, holder)
}

// TakeoverRunClaim atomically replaces any existing claim on store.
func (e *Engine) TakeoverRunClaim(ctx context.Context, runID, holder string) error {
	if err := e.CheckOpen(); err != nil {
		return err
	}
	return e.claims.TakeoverRunClaim(ctx, e.store, runID, holder)
}

// TakeoverExpiredRunClaim replaces a claim only when its age exceeds maxAge.
func (e *Engine) TakeoverExpiredRunClaim(ctx context.Context, runID, holder string, maxAge time.Duration) error {
	if err := e.CheckOpen(); err != nil {
		return err
	}
	return e.claims.TakeoverExpiredRunClaim(ctx, e.store, runID, holder, maxAge)
}

// ReleaseRun releases the claim on runID. Only the current holder may release it.
func (e *Engine) ReleaseRun(ctx context.Context, runID, holder string) error {
	if err := e.CheckOpen(); err != nil {
		return err
	}
	return e.claims.ReleaseRun(ctx, e.store, runID, holder)
}

// ClearRunClaim removes a run claim (force release).
func (e *Engine) ClearRunClaim(ctx context.Context, runID string) error {
	if err := e.CheckOpen(); err != nil {
		return err
	}
	return e.claims.ClearRunClaim(ctx, e.store, runID)
}

// GetRunClaim reads the run's current execution claim without modifying it.
func (e *Engine) GetRunClaim(ctx context.Context, runID string) (holder string, acquiredAt time.Time, ok bool, err error) {
	if err := e.CheckOpen(); err != nil {
		return "", time.Time{}, false, err
	}
	return e.claims.GetRunClaim(ctx, e.store, runID)
}

// IsRunHeld reports whether runID currently has an active claim.
func (e *Engine) IsRunHeld(ctx context.Context, runID string) (bool, error) {
	if err := e.CheckOpen(); err != nil {
		return false, err
	}
	return e.claims.IsRunHeld(ctx, e.store, runID)
}

// IsRunTokenFenced reports whether token has been fenced out of runID.
func (e *Engine) IsRunTokenFenced(ctx context.Context, runID, token string) (bool, error) {
	if err := e.CheckOpen(); err != nil {
		return false, err
	}
	return e.claims.IsRunTokenFenced(ctx, e.store, runID, token)
}

// StoreContent persists bytes under a content-addressed reference.
func (e *Engine) StoreContent(ctx context.Context, ref string, data []byte) error {
	if err := e.CheckOpen(); err != nil {
		return err
	}
	return StoreContent(ctx, e.store, ref, data)
}

// LoadContent retrieves stored bytes under ref.
func (e *Engine) LoadContent(ctx context.Context, ref string) ([]byte, error) {
	if err := e.CheckOpen(); err != nil {
		return nil, err
	}
	return LoadContent(ctx, e.store, ref)
}
