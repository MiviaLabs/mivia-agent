package ledger

import (
	"context"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledgercore"
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
	return s.engine.CatchUpSince(ctx, nil, s.applyTail)
}

// applyTail folds an ordered set of store events into the projection under the
// write lock. The set is the merged tail of every run that moved, sorted into
// GLOBAL append order (the store's rowid) before this call; within one run that
// order equals ascending sequence, so per-run sequence ordering is preserved
// while a run_deleted tombstone still lands before any later reused-key
// run_created. Application is idempotent and monotone: an event at or below the
// current watermark is skipped, so a tail read concurrently with another
// catch-up can never apply anything twice or out of order. Because each tail
// was read starting at a watermark snapshot, skipping already-applied prefixes
// cannot open a gap.
func (s *StorageLedgerRepository) applyTail(ctx context.Context, events []storage.Event) error {
	ledgercore.SortEventsStable(events)

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, evt := range events {
		if uint64(evt.Sequence) <= s.engine.Watermarks().Applied(evt.RunID) ||
			s.isInflightLocked(evt.RunID, uint64(evt.Sequence)) {
			continue
		}
		if err := s.applyStoreEventLocked(ctx, evt); err != nil {
			return fmt.Errorf("apply event %s for %s: %w", evt.ID, evt.RunID, err)
		}
		if evt.Kind == storageKindRunDeleted {
			continue
		}
		s.engine.Watermarks().SetApplied(evt.RunID, uint64(evt.Sequence))
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
		s.engine.Watermarks().DeleteRun(evt.RunID)
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
		// Carry the persisted idempotency key into the projection so replay
		// re-registers it and a second CreateRun with the same key is refused
		// (finding F6). Payloads written before the field existed decode to ""
		// and register no key, exactly as before.
		if err := s.mem.CreateRun(ctx, snap.IdempotencyKey, snap); err != nil && err != ErrDuplicate {
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
		status, completedAt := unmarshalRunClosed(evt.Payload)
		s.mem.mu.Lock()
		if rec, ok := s.mem.runs[evt.RunID]; ok {
			rec.closed = true
			closeRebuiltRun(&rec.snapshot)
			if status != "" {
				rec.snapshot.Status = RunStatus(status)
			}
			if completedAt != nil {
				t := *completedAt
				rec.snapshot.CompletedAt = &t
			}
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
		taskID, outputRef, errorRef, toolCallsRef, err := unmarshalOutputRefs(evt.Payload)
		if err != nil {
			return err
		}
		s.mem.mu.Lock()
		if trec := s.memTaskLocked(evt.RunID, taskID); trec != nil {
			trec.snapshot.OutputRef = normalizeReference(outputRef)
			trec.snapshot.ErrorRef = normalizeReference(errorRef)
			trec.snapshot.ToolCallsRef = normalizeReference(toolCallsRef)
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
// is unknown - events for a run with no run_created event are dropped, as
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

// rebuildRunProjection rebuilds one run's in-memory projection from the
// durable store. It is the append-failure recovery path shared by the mutation
// methods: a write that reached the store must win, and one that did not must
// leave no trace in the projection. The run's whole store history is replayed
// under the write lock, so the projection agrees with the store before the
// caller returns the append error.
//
// The per-run watermarks and in-flight claims are reset first so the replay
// re-applies every durable row. The store cursor is left untouched, so a later
// catch-up still sees rows appended after this read. Lock order matches
// applyTail: s.mu, then mem.mu inside applyStoreEventLocked.
func (s *StorageLedgerRepository) rebuildRunProjection(ctx context.Context, runID string) error {
	events, err := s.engine.Store().Events(ctx, runID)
	if err != nil {
		return fmt.Errorf("read events for %s: %w", runID, err)
	}
	ledgercore.SortEventsStable(events)

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.mem.DeleteRun(ctx, runID); err != nil && err != ErrNotFound {
		return err
	}
	s.engine.Watermarks().DeleteRun(runID)
	for key := range s.inflight {
		if key.runID == runID {
			delete(s.inflight, key)
		}
	}
	for _, evt := range events {
		if err := s.applyStoreEventLocked(ctx, evt); err != nil {
			return fmt.Errorf("rebuild projection for %s: %w", runID, err)
		}
		if evt.Kind == storageKindRunDeleted {
			continue
		}
		s.engine.Watermarks().SetApplied(evt.RunID, uint64(evt.Sequence))
		// Keep new event IDs from colliding with replayed ones after a
		// restart, exactly as applyTail does.
		advanceStorageEventIDCounter(parseSuffixNum(evt.ID, "se-"))
	}
	return nil
}

// appendStoreEventOrRebuild appends the event and, on failure, rebuilds the
// run's projection from the store so reads report only what is durable. The
// append error is returned unchanged when the rebuild succeeds, so each caller
// keeps its own error translation (for example storage.ErrDuplicate).
func (s *StorageLedgerRepository) appendStoreEventOrRebuild(ctx context.Context, evt storage.Event) error {
	err := s.appendStoreEvent(ctx, evt)
	if err != nil {
		if rerr := s.rebuildRunProjection(ctx, evt.RunID); rerr != nil {
			return fmt.Errorf("store append: %v; rebuild projection: %w", err, rerr)
		}
	}
	return err
}

// ---------------------------------------------------------------------------
// Sequence allocation and append publication
// ---------------------------------------------------------------------------

// nextSequence returns the next sequence number for a run and records the
// allocation. It starts from the higher of the applied watermark (what the
// store is known to hold) and this instance's own previous allocations.
// Must NOT be called under s.mu (to avoid deadlock).
func (s *StorageLedgerRepository) nextSequence(runID string) uint64 {
	next := s.engine.NextSequence(runID)
	s.mu.Lock()
	s.inflight[inflightKey{runID: runID, sequence: next}] = struct{}{}
	s.mu.Unlock()
	return next
}

func (s *StorageLedgerRepository) clearInflight(runID string, sequence uint64) {
	s.mu.Lock()
	delete(s.inflight, inflightKey{runID: runID, sequence: sequence})
	s.mu.Unlock()
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
func (s *StorageLedgerRepository) appendStoreEvent(ctx context.Context, evt storage.Event) error {
	defer func() {
		s.mu.Lock()
		s.releaseInflightLocked(evt.RunID, uint64(evt.Sequence))
		s.mu.Unlock()
	}()
	return s.engine.AppendEvent(ctx, evt, ledgercore.AppendOptions{})
}
