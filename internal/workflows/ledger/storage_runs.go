package ledger

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// checkOpen returns ErrClosed if the repository has been closed.
func (s *StorageRepository) checkOpen() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return ErrClosed
	}
	return nil
}

// ---------------------------------------------------------------------------
// Repository implementation
// ---------------------------------------------------------------------------

// CreateRun admits a run: persists the run snapshot (typed fields + the
// canonical snapshot JSON) and records the wf_run_created event. Returns
// ErrDuplicate if the run already exists, ErrInvalidTransition if the
// snapshot status is not pending.
func (s *StorageRepository) CreateRun(ctx context.Context, snap RunSnapshot, snapshotJSON []byte) error {
	if err := s.ensureBuilt(ctx); err != nil {
		return err
	}
	if !strings.HasPrefix(snap.RunID, "wfr-") {
		return ErrInvalidTransition
	}
	if err := s.rebaseRunSequence(ctx, snap.RunID); err != nil {
		return err
	}

	lock := s.runLock(snap.RunID)
	lock.Lock()
	defer lock.Unlock()

	s.mu.Lock()
	prev, prevOK := s.proj[snap.RunID]
	if prevOK && prev.HasRun {
		s.mu.Unlock()
		return ErrDuplicate
	}
	if snap.Status != RunStatusPending {
		s.mu.Unlock()
		return ErrInvalidTransition
	}
	now := s.now()
	run := snap.Clone()
	if run.StartedAt.IsZero() {
		run.StartedAt = now
	}
	run.Version = 1
	p := Projection{
		Run:          &run,
		SnapshotJSON: append([]byte(nil), snapshotJSON...),
		HasRun:       true,
		ActiveStepID: run.ActiveStepID,
	}
	s.proj[snap.RunID] = p
	s.mu.Unlock()

	payload, err := marshalRunCreated(runCreatedPayload{Run: run, SnapshotJSON: snapshotJSON, CreatedAt: now})
	if err != nil {
		s.rollbackAndRebuild(ctx, snap.RunID, func() {
			if prevOK {
				s.proj[snap.RunID] = prev
			} else {
				delete(s.proj, snap.RunID)
			}
		})
		return fmt.Errorf("marshal %s payload: %w", eventKindRunCreated, err)
	}

	evt := storage.Event{
		ID:       EventID(snap.RunID, eventKindRunCreated),
		RunID:    snap.RunID,
		Sequence: int(s.nextSequence(snap.RunID)),
		Kind:     eventKindRunCreated,
		Payload:  payload,
	}
	return s.appendEvent(ctx, evt, func() {
		if prevOK {
			s.proj[snap.RunID] = prev
		} else {
			delete(s.proj, snap.RunID)
		}
	})
}

// GetRun returns the current run snapshot with the DERIVED active step
// (see Projection.ActiveStepID). Returns ErrNotFound if absent.
func (s *StorageRepository) GetRun(ctx context.Context, runID string) (RunSnapshot, error) {
	if err := s.ensureBuilt(ctx); err != nil {
		return RunSnapshot{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.proj[runID]
	if !ok || !p.HasRun || p.Run == nil {
		return RunSnapshot{}, ErrNotFound
	}
	run := p.Run.Clone()
	run.ActiveStepID = p.ActiveStepID
	return run, nil
}

// ListRuns returns bounded snapshots, optionally filtered by status.
func (s *StorageRepository) ListRuns(ctx context.Context, status ...RunStatus) ([]RunSnapshot, error) {
	if err := s.ensureBuilt(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0, len(s.proj))
	for id := range s.proj {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out []RunSnapshot
	for _, id := range ids {
		p := s.proj[id]
		if !p.HasRun || p.Run == nil {
			continue
		}
		if len(status) > 0 {
			match := false
			for _, st := range status {
				if p.Run.Status == st {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}
		run := p.Run.Clone()
		run.ActiveStepID = p.ActiveStepID
		out = append(out, run)
	}
	return out, nil
}

// GetRunSnapshot returns the canonical snapshot JSON stored at admission.
// Returns ErrNotFound if absent.
func (s *StorageRepository) GetRunSnapshot(ctx context.Context, runID string) ([]byte, error) {
	if err := s.ensureBuilt(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.proj[runID]
	if !ok || !p.HasRun {
		return nil, ErrNotFound
	}
	return append([]byte(nil), p.SnapshotJSON...), nil
}

// CompareAndSetRunStatus atomically transitions the run status, bumping
// the run version. Returns ErrConflict on version mismatch, ErrInvalidTransition
// on an illegal edge. finishedAt is persisted when the new status is terminal.
func (s *StorageRepository) CompareAndSetRunStatus(ctx context.Context, runID string, expectedVersion uint64, status RunStatus, finishedAt *time.Time) error {
	if err := s.ensureBuilt(ctx); err != nil {
		return err
	}
	lock := s.runLock(runID)
	lock.Lock()
	defer lock.Unlock()

	s.mu.Lock()
	p, ok := s.proj[runID]
	if !ok || !p.HasRun || p.Run == nil {
		s.mu.Unlock()
		return ErrNotFound
	}
	if p.Run.Version != expectedVersion {
		s.mu.Unlock()
		return ErrConflict
	}
	if !ValidRunTransition(p.Run.Status, status) {
		s.mu.Unlock()
		return ErrInvalidTransition
	}
	now := s.now()
	newVersion := p.Run.Version + 1
	var fin *time.Time
	if IsTerminalRunStatus(status) {
		if finishedAt != nil {
			t := *finishedAt
			fin = &t
		} else {
			t := now
			fin = &t
		}
	}
	orig := p.Run.Clone()
	run := orig
	run.Status = status
	run.Version = newVersion
	run.FinishedAt = cloneTime(fin)
	p.Run = &run
	s.proj[runID] = p
	s.mu.Unlock()

	payload, err := marshalRunStatusChanged(runStatusChangedPayload{
		Status:     status,
		Version:    newVersion,
		FinishedAt: cloneTime(fin),
		CreatedAt:  now,
	})
	if err != nil {
		s.rollbackAndRebuild(ctx, runID, func() {
			q := s.proj[runID]
			q.Run = &orig
			s.proj[runID] = q
		})
		return fmt.Errorf("marshal %s payload: %w", eventKindRunStatusChanged, err)
	}

	evt := storage.Event{
		ID:       EventID(runID, eventKindRunStatusChanged, strconv.FormatUint(newVersion, 10)),
		RunID:    runID,
		Sequence: int(s.nextSequence(runID)),
		Kind:     eventKindRunStatusChanged,
		Payload:  payload,
	}
	return s.appendEvent(ctx, evt, func() {
		q := s.proj[runID]
		q.Run = &orig
		s.proj[runID] = q
	})
}

// RecordRunResumed appends the wf_run_resumed audit event for a run that a
// controller is resuming (crash recovery, operator resume, or controller
// re-entry). It mutates no run state: the event is purely observational, so
// the projection ignores it. Returns ErrNotFound when the run is absent. The
// deterministic event ID is (runID, kind) and the payload carries only the
// run id, so a retried resume under the real clock appends at most one event
// (the second write is the idempotent retry path of appendEvent).
func (s *StorageRepository) RecordRunResumed(ctx context.Context, runID string) error {
	if err := s.ensureBuilt(ctx); err != nil {
		return err
	}
	lock := s.runLock(runID)
	lock.Lock()
	defer lock.Unlock()

	s.mu.Lock()
	p, ok := s.proj[runID]
	if !ok || !p.HasRun || p.Run == nil {
		s.mu.Unlock()
		return ErrNotFound
	}
	s.mu.Unlock()

	// json.Marshal of the single-string payload cannot fail, so the error
	// branch would be dead code (diff-coverage gate).
	payload, _ := marshalRunResumed(runResumedPayload{RunID: runID})
	evt := storage.Event{
		ID:       EventID(runID, eventKindRunResumed),
		RunID:    runID,
		Sequence: int(s.nextSequence(runID)),
		Kind:     eventKindRunResumed,
		Payload:  payload,
	}
	return s.appendEvent(ctx, evt, nil)
}
