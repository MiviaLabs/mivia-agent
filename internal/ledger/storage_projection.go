package ledger

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// Incremental projection catch-up.
//
// A StorageLedgerRepository does not own the store: another instance, in this
// process or another, may append to it at any time. Reads therefore refresh
// the projection before serving, bounded by two watermarks:
//
//   - cursor: the store append position already probed, so an instance that is
//     up to date pays one constant-time probe and reads no events.
//   - applied[runID]: the highest sequence folded into the projection for a
//     run, so a run that moved is tail-read from there, never replayed whole.
//
// Application is idempotent and monotone, so overlapping catch-ups from
// concurrent readers cannot apply an event twice or out of order.

// catchUp folds every event newer than this instance's per-run watermark into
// the projection. Store I/O happens without s.mu held; only the application of
// an already-read tail takes the write lock.
func (s *StorageLedgerRepository) catchUp(ctx context.Context) error {
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
		s.mu.RLock()
		from := s.applied[runID]
		s.mu.RUnlock()

		events, err := s.store.EventsSince(ctx, runID, int(from))
		if err != nil {
			return fmt.Errorf("read events for %s: %w", runID, err)
		}
		if len(events) == 0 {
			continue
		}
		if err := s.applyTail(ctx, runID, events); err != nil {
			return err
		}
	}
	// Only advance once every run the probe reported has been applied; a
	// failure leaves the cursor where it was so the next call retries.
	s.advanceCursor(newCursor)
	return nil
}

// advanceCursor moves the store cursor forward. It never rewinds, so a slow
// concurrent catch-up cannot undo a newer one.
func (s *StorageLedgerRepository) advanceCursor(cursor uint64) {
	s.mu.Lock()
	if cursor > s.cursor {
		s.cursor = cursor
	}
	s.mu.Unlock()
}

// applyTail applies an ordered tail of store events to the projection under
// the write lock. Application is idempotent and monotone: an event at or below
// the current watermark is skipped, so a tail read concurrently with another
// catch-up can never apply anything twice or out of order. Because the tail
// was read starting at a watermark snapshot, skipping already-applied prefixes
// cannot open a gap.
func (s *StorageLedgerRepository) applyTail(ctx context.Context, runID string, events []storage.Event) error {
	sort.SliceStable(events, func(i, j int) bool { return events[i].Sequence < events[j].Sequence })

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, evt := range events {
		if uint64(evt.Sequence) <= s.applied[runID] ||
			s.isInflightLocked(runID, uint64(evt.Sequence)) {
			continue
		}
		if err := s.applyStoreEventLocked(ctx, evt); err != nil {
			return fmt.Errorf("apply event %s for %s: %w", evt.ID, runID, err)
		}
		if evt.Kind == storageKindRunDeleted {
			continue
		}
		s.applied[runID] = uint64(evt.Sequence)
		// Keep new event IDs from colliding with replayed ones after a restart.
		advanceStorageEventIDCounter(parseSuffixNum(evt.ID, "se-"))
	}
	return nil
}

// applyStoreEventLocked folds a single store event into the in-memory
// projection. It mirrors RebuildProjection's semantics one event at a time.
// Must be called with s.mu write-locked.
// applyStoreEventLocked folds one stored event into the projection. Split by
// subject so neither half outgrows the structure budget: run-level events here,
// task-level in applyTaskEventLocked.
func (s *StorageLedgerRepository) applyStoreEventLocked(ctx context.Context, evt storage.Event) error {
	switch evt.Kind {
	case storageKindRunDeleted:
		if err := s.mem.DeleteRun(ctx, evt.RunID); err != nil && err != ErrNotFound {
			return err
		}
		delete(s.applied, evt.RunID)
		delete(s.allocated, evt.RunID)
		for key := range s.inflight {
			if key.runID == evt.RunID {
				delete(s.inflight, key)
			}
		}

	case storageKindRunCreated:
		snap, err := unmarshalRunSnapshot(evt.Payload)
		if err != nil {
			return err
		}
		if snap.RunID == "" {
			snap.RunID = evt.RunID
		}
		if err := s.mem.CreateRun(ctx, "", snap); err != nil && err != ErrDuplicate {
			return err
		}

	case storageKindRunStatusChanged:
		status, completedAt, err := unmarshalRunStatusChange(evt.Payload)
		if err != nil {
			return err
		}
		s.mem.mu.Lock()
		if rec, ok := s.mem.runs[evt.RunID]; ok {
			rec.snapshot.Status = RunStatus(status)
			if completedAt != nil {
				t := *completedAt
				rec.snapshot.CompletedAt = &t
			}
		}
		s.mem.mu.Unlock()

	case storageKindLifecycleEvent:
		lifecycleEvent, err := fromStorageEvent(evt)
		if err != nil {
			return err
		}
		if err := s.mem.AppendEvent(ctx, lifecycleEvent); err != nil &&
			err != ErrDuplicate && err != ErrNotFound {
			return err
		}

	case storageKindRunClosed:
		s.mem.mu.Lock()
		if rec, ok := s.mem.runs[evt.RunID]; ok {
			rec.closed = true
			closeRebuiltRun(&rec.snapshot)
		}
		s.mem.mu.Unlock()

	default:
		return s.applyTaskEventLocked(ctx, evt)
	}
	return nil
}

// applyTaskEventLocked folds one task-level stored event into the projection.
// An unknown kind is ignored rather than failing: a newer writer may emit kinds
// this build does not know, and refusing them would wedge catch-up entirely.
func (s *StorageLedgerRepository) applyTaskEventLocked(ctx context.Context, evt storage.Event) error {
	switch evt.Kind {
	case storageKindTaskCreated:
		snap, err := unmarshalTaskSnapshot(evt.Payload)
		if err != nil {
			return err
		}
		if snap.RunID == "" {
			snap.RunID = evt.RunID
		}
		s.mem.mu.Lock()
		if rec, ok := s.mem.runs[snap.RunID]; ok {
			if _, exists := rec.tasks[snap.TaskID]; !exists {
				rec.tasks[snap.TaskID] = &taskRecord{snapshot: snap.Clone()}
			}
		}
		s.mem.mu.Unlock()

	case storageKindTaskStatusChanged:
		taskID, status, version, completedAt, err := unmarshalStatusChange(evt.Payload)
		if err != nil {
			return err
		}
		s.mem.mu.Lock()
		if trec := s.memTaskLocked(evt.RunID, taskID); trec != nil {
			trec.snapshot.Status = status
			trec.snapshot.Version = version
			if completedAt != nil {
				t := *completedAt
				trec.snapshot.CompletedAt = &t
			}
		}
		s.mem.mu.Unlock()

	case storageKindTaskOutputSet:
		taskID, outputRef, errorRef, err := unmarshalOutputRefs(evt.Payload)
		if err != nil {
			return err
		}
		s.mem.mu.Lock()
		if trec := s.memTaskLocked(evt.RunID, taskID); trec != nil {
			trec.snapshot.OutputRef = normalizeReference(outputRef)
			trec.snapshot.ErrorRef = normalizeReference(errorRef)
		}
		s.mem.mu.Unlock()

	case storageKindTaskAttempt:
		taskID, attemptID, status, finishedAt, err := unmarshalAttemptEntry(evt.Payload)
		if err != nil {
			return err
		}
		s.mem.mu.Lock()
		if trec := s.memTaskLocked(evt.RunID, taskID); trec != nil {
			applyAttempt(trec, evt.RunID, taskID, attemptID, status, finishedAt)
		}
		s.mem.mu.Unlock()

	}
	return nil
}

// memTaskLocked returns the projection record for a task, creating a
// placeholder when an event references a task the projection has not seen
// created yet (mirroring RebuildProjection). Returns nil when the run itself
// is unknown — events for a run with no run_created event are dropped, as
// they were before. Must be called with s.mem.mu write-locked.
func (s *StorageLedgerRepository) memTaskLocked(runID, taskID string) *taskRecord {
	rec, ok := s.mem.runs[runID]
	if !ok {
		return nil
	}
	trec, ok := rec.tasks[taskID]
	if !ok {
		trec = &taskRecord{snapshot: TaskSnapshot{RunID: runID, TaskID: taskID}}
		rec.tasks[taskID] = trec
	}
	return trec
}

// applyAttempt upserts an attempt entry on a task record.
func applyAttempt(trec *taskRecord, runID, taskID, attemptID, status string, finishedAt *time.Time) {
	for i := range trec.snapshot.Attempts {
		if trec.snapshot.Attempts[i].AttemptID != attemptID {
			continue
		}
		trec.snapshot.Attempts[i].Status = status
		if finishedAt != nil {
			t := *finishedAt
			trec.snapshot.Attempts[i].FinishedAt = &t
		}
		return
	}
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
}

// ---------------------------------------------------------------------------
// Sequence allocation and append publication
// ---------------------------------------------------------------------------

// nextSequence returns the next sequence number for a run and records the
// allocation. It starts from the higher of the applied watermark (what the
// store is known to hold) and this instance's own previous allocations.
// Must NOT be called under s.mu (to avoid deadlock).
func (s *StorageLedgerRepository) nextSequence(runID string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.applied[runID]
	if s.allocated[runID] > next {
		next = s.allocated[runID]
	}
	next++
	s.allocated[runID] = next
	// Claim the sequence for this writer until the append resolves. Catch-up
	// must not apply an event the writer is still publishing itself, or the
	// writer's own projection update comes back as a spurious duplicate.
	s.inflight[inflightKey{runID: runID, sequence: next}] = struct{}{}
	return next
}

// inflightKey identifies one sequence claimed by a writer on this instance.
type inflightKey struct {
	runID    string
	sequence uint64
}

// releaseInflight drops a writer's claim on a sequence. Must be called with
// s.mu write-locked.
func (s *StorageLedgerRepository) releaseInflightLocked(runID string, sequence uint64) {
	delete(s.inflight, inflightKey{runID: runID, sequence: sequence})
}

// isInflightLocked reports whether a sequence is claimed by a writer on this
// instance. Must be called with at least s.mu read-locked.
func (s *StorageLedgerRepository) isInflightLocked(runID string, sequence uint64) bool {
	_, ok := s.inflight[inflightKey{runID: runID, sequence: sequence}]
	return ok
}

// appendStoreEvent writes an event to the store and, on success, advances the
// applied watermark past it so catch-up never re-reads this instance's own
// writes. The caller updates the in-memory projection itself.
//
// The watermark advance and the release of the writer's in-flight claim happen
// in one critical section, which is the same lock catch-up holds while it
// applies events. Between the store append and that section the claim keeps
// catch-up off the event; after it, the watermark does. There is no instant at
// which both this writer and a catch-up would apply the same event.
func (s *StorageLedgerRepository) appendStoreEvent(ctx context.Context, evt storage.Event) error {
	err := s.store.Append(ctx, evt)

	s.mu.Lock()
	if err == nil && uint64(evt.Sequence) > s.applied[evt.RunID] {
		s.applied[evt.RunID] = uint64(evt.Sequence)
	}
	// On failure the claim is released without advancing the watermark, so if
	// the sequence was lost to another writer, catch-up will still apply it.
	s.releaseInflightLocked(evt.RunID, uint64(evt.Sequence))
	s.mu.Unlock()

	return err
}
