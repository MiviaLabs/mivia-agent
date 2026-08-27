package ledgercore

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// WatermarkTracker tracks applied sequence watermarks, allocated sequence numbers,
// and probe cursors for an event-sourced repository over a storage.Store.
type WatermarkTracker struct {
	mu        sync.RWMutex
	applied   map[string]uint64
	allocated map[string]uint64
	cursor    uint64
}

// NewWatermarkTracker initializes a new WatermarkTracker.
func NewWatermarkTracker() *WatermarkTracker {
	return &WatermarkTracker{
		applied:   make(map[string]uint64),
		allocated: make(map[string]uint64),
	}
}

// Cursor returns the highest store append position probed so far.
func (w *WatermarkTracker) Cursor() uint64 {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.cursor
}

// AdvanceCursor moves the store probe cursor forward. It never rewinds.
func (w *WatermarkTracker) AdvanceCursor(cursor uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if cursor > w.cursor {
		w.cursor = cursor
	}
}

// Applied returns the highest store sequence folded into the projection for runID.
func (w *WatermarkTracker) Applied(runID string) uint64 {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.applied[runID]
}

// SetApplied sets the applied watermark for runID.
func (w *WatermarkTracker) SetApplied(runID string, seq uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if seq > w.applied[runID] {
		w.applied[runID] = seq
	}
}

// Allocated returns the highest sequence allocated for runID.
func (w *WatermarkTracker) Allocated(runID string) uint64 {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.allocated[runID]
}

// SetAllocated sets the allocated sequence for runID.
func (w *WatermarkTracker) SetAllocated(runID string, seq uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if seq > w.allocated[runID] {
		w.allocated[runID] = seq
	}
}

// NextSequence computes and reserves the next sequence number for runID.
func (w *WatermarkTracker) NextSequence(runID string) uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	next := w.applied[runID]
	if w.allocated[runID] > next {
		next = w.allocated[runID]
	}
	next++
	w.allocated[runID] = next
	return next
}

// RebaseRunSequence reads existing events for runID from store and ensures
// applied and allocated are at least the highest sequence already in the store.
func (w *WatermarkTracker) RebaseRunSequence(ctx context.Context, store storage.Store, runID string) error {
	events, err := store.Events(ctx, runID)
	if err != nil {
		return fmt.Errorf("read existing events for %s: %w", runID, err)
	}
	var maxSeq uint64
	for _, ev := range events {
		if uint64(ev.Sequence) > maxSeq {
			maxSeq = uint64(ev.Sequence)
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if maxSeq > w.applied[runID] {
		w.applied[runID] = maxSeq
	}
	if maxSeq > w.allocated[runID] {
		w.allocated[runID] = maxSeq
	}
	return nil
}

// CheckBehind compares the store's maxSequences against applied watermarks
// and returns a sorted list of run IDs that have unapplied changes.
func (w *WatermarkTracker) CheckBehind(maxSequences map[string]int) []string {
	w.mu.RLock()
	var behind []string
	for runID, maxSeq := range maxSequences {
		if uint64(maxSeq) > w.applied[runID] {
			behind = append(behind, runID)
		}
	}
	w.mu.RUnlock()
	if len(behind) > 1 {
		sort.Strings(behind)
	}
	return behind
}

// DeleteRun clears all sequence and watermark tracking for runID.
func (w *WatermarkTracker) DeleteRun(runID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.applied, runID)
	delete(w.allocated, runID)
}
