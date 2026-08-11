package ledger

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// CreateStepAttempt records a fresh numbered attempt for a step. The
// (runID, stepID, attemptNo) triple is unique: a second create for the
// same triple never appends a second event (ErrDuplicate in-process, or
// ErrConflict when a concurrent writer took the deterministic event ID).
func (s *StorageRepository) CreateStepAttempt(ctx context.Context, attempt StepAttempt) error {
	if err := s.ensureBuilt(ctx); err != nil {
		return err
	}
	if err := s.validateInitialPanelAttempt(ctx, attempt); err != nil {
		return err
	}
	lock := s.runLock(attempt.RunID)
	lock.Lock()
	defer lock.Unlock()

	s.mu.Lock()
	p, ok := s.proj[attempt.RunID]
	if !ok || !p.HasRun {
		s.mu.Unlock()
		return ErrNotFound
	}
	for _, a := range p.Attempts {
		if a.AttemptID == attempt.AttemptID || (a.StepID == attempt.StepID && a.AttemptNo == attempt.AttemptNo) {
			s.mu.Unlock()
			return ErrDuplicate
		}
	}
	now := s.now()
	rec := attempt.Clone()
	rec.Status = AttemptStatusRunning
	rec.Version = 1
	rec.StartedAt = now
	if len(rec.Executions) == 0 && rec.CoordinatorRunID != "" && rec.TaskID != "" {
		rec.Executions = []StepExecution{{
			ExecutionNo: 1, CoordinatorRunID: rec.CoordinatorRunID, TaskID: rec.TaskID, StartedAt: now,
		}}
	}
	prevActive := p.ActiveStepID
	p.Attempts = append(p.Attempts, rec)
	// Mirror RebuildProjection's step-candidate rule exactly: an attempt
	// start contributes its step to the derived active step only when the
	// step is non-empty. An empty step carries no candidate in the replay,
	// so the in-place mutation must not wipe the derived step either.
	if attempt.StepID != "" {
		p.ActiveStepID = attempt.StepID
	}
	s.proj[attempt.RunID] = p
	s.mu.Unlock()

	payload, err := marshalAttemptStarted(attemptStartedPayload{Attempt: rec, CreatedAt: now})
	if err != nil {
		s.rollbackAndRebuild(ctx, attempt.RunID, s.removeAttemptRollback(attempt.RunID, attempt.AttemptID, prevActive))
		return fmt.Errorf("marshal %s payload: %w", eventKindAttemptStarted, err)
	}

	evt := storage.Event{
		ID:       EventID(attempt.RunID, eventKindAttemptStarted, attempt.StepID, strconv.Itoa(attempt.AttemptNo)),
		RunID:    attempt.RunID,
		Sequence: int(s.nextSequence(attempt.RunID)),
		Kind:     eventKindAttemptStarted,
		Payload:  payload,
	}
	return s.appendEvent(ctx, evt, s.removeAttemptRollback(attempt.RunID, attempt.AttemptID, prevActive))
}

// removeAttemptRollback returns a rollback closure that removes one attempt
// from the cached projection by ID and restores the pre-mutation active step
// (for failed appends).
func (s *StorageRepository) removeAttemptRollback(runID, attemptID string, prevActive string) func() {
	return func() {
		q := s.proj[runID]
		for i := range q.Attempts {
			if q.Attempts[i].AttemptID == attemptID {
				q.Attempts = append(q.Attempts[:i], q.Attempts[i+1:]...)
				break
			}
		}
		q.ActiveStepID = prevActive
		s.proj[runID] = q
	}
}

// RecordStepAttemptOutcome records a fresh numbered attempt and its TERMINAL
// outcome in ONE wf_attempt_completed event: the attempt is never observable
// in a non-terminal state. Uniqueness mirrors CreateStepAttempt (the
// (runID, stepID, attemptNo) triple and the AttemptID are both unique —
// ErrDuplicate), and the outcome rules mirror CompleteStepAttempt
// (ErrInvalidTransition for a non-terminal outcome or an illegal edge,
// including the no-route-on-interrupted/canceled/timed_out rule and the
// MaxEvidenceBytes cap). The recorded attempt carries Version 1 with
// StartedAt == FinishedAt == now. The completed payload carries the fresh
// attempt's StepID/AttemptNo/StartedAt identity, which the replay restores so
// LatestFailureText and the repair budget keep working after a rebuild.
func (s *StorageRepository) RecordStepAttemptOutcome(ctx context.Context, attempt StepAttempt, outcome AttemptOutcome) error {
	if err := s.ensureBuilt(ctx); err != nil {
		return err
	}
	lock := s.runLock(attempt.RunID)
	lock.Lock()
	defer lock.Unlock()

	s.mu.Lock()
	p, ok := s.proj[attempt.RunID]
	if !ok || !p.HasRun {
		s.mu.Unlock()
		return ErrNotFound
	}
	if err := s.validateRecordOutcomeLocked(&p, attempt, outcome); err != nil {
		s.mu.Unlock()
		return err
	}
	now := s.now()
	rec, rollback := s.applyRecordOutcomeLocked(&p, attempt, outcome, now)
	s.proj[attempt.RunID] = p
	s.mu.Unlock()

	payload, err := marshalAttemptCompleted(attemptCompletedPayload{
		AttemptID:        attempt.AttemptID,
		Version:          rec.Version,
		Status:           outcome.Status,
		StepID:           attempt.StepID,
		AttemptNo:        attempt.AttemptNo,
		StartedAt:        now,
		CoordinatorRunID: outcome.CoordinatorRunID,
		TaskID:           outcome.TaskID,
		OutputRef:        outcome.OutputRef,
		OutputDigest:     outcome.OutputDigest,
		ErrorRef:         outcome.ErrorRef,
		ToStepID:         outcome.ToStepID,
		TransitionIndex:  outcome.TransitionIndex,
		MatchDigest:      outcome.MatchDigest,
		DecisionJSON:     append([]byte(nil), outcome.DecisionJSON...),
		EvidenceJSON:     append([]byte(nil), outcome.EvidenceJSON...),
		FinishedAt:       now,
		CreatedAt:        now,
	})
	if err != nil {
		s.rollbackAndRebuild(ctx, attempt.RunID, rollback)
		return fmt.Errorf("marshal %s payload: %w", eventKindAttemptCompleted, err)
	}
	evt := storage.Event{
		ID:       EventID(attempt.RunID, eventKindAttemptCompleted, attempt.StepID, strconv.Itoa(attempt.AttemptNo)),
		RunID:    attempt.RunID,
		Sequence: int(s.nextSequence(attempt.RunID)),
		Kind:     eventKindAttemptCompleted,
		Payload:  payload,
	}
	return s.appendEvent(ctx, evt, rollback)
}

// validateRecordOutcomeLocked checks the one-event terminal write's guards:
// uniqueness of the AttemptID and the (runID, stepID, attemptNo) triple, a
// terminal outcome with a legal running->terminal edge, the
// no-route-on-interrupted/canceled/timed_out rule and the MaxEvidenceBytes
// cap. Caller holds s.mu and the per-run lock.
func (s *StorageRepository) validateRecordOutcomeLocked(p *Projection, attempt StepAttempt, outcome AttemptOutcome) error {
	for _, a := range p.Attempts {
		if a.AttemptID == attempt.AttemptID || (a.StepID == attempt.StepID && a.AttemptNo == attempt.AttemptNo) {
			return ErrDuplicate
		}
	}
	if !IsTerminalAttemptStatus(outcome.Status) || !ValidAttemptTransition(AttemptStatusRunning, outcome.Status) {
		return ErrInvalidTransition
	}
	if outcomeHasRoute(outcome) && (outcome.Status == AttemptStatusInterrupted || outcome.Status == AttemptStatusCanceled || outcome.Status == AttemptStatusTimedOut) {
		return ErrInvalidTransition
	}
	if len(outcome.EvidenceJSON) > MaxEvidenceBytes {
		return fmt.Errorf("evidence exceeds %d bytes", MaxEvidenceBytes)
	}
	return nil
}

// applyRecordOutcomeLocked builds the fresh terminal attempt from the outcome,
// appends it to the cached projection and applies the route/derived-step rules
// exactly like applyAttemptCompletionLocked. It returns the recorded attempt
// and a rollback closure restoring the pre-mutation projection on marshal or
// append failure. Caller holds s.mu and the per-run lock.
func (s *StorageRepository) applyRecordOutcomeLocked(p *Projection, attempt StepAttempt, outcome AttemptOutcome, now time.Time) (StepAttempt, func()) {
	rec := attempt.Clone()
	rec.Status = outcome.Status
	rec.Version = 1
	rec.StartedAt = now
	rec.FinishedAt = &now
	rec.CoordinatorRunID = outcome.CoordinatorRunID
	rec.TaskID = outcome.TaskID
	rec.OutputRef = outcome.OutputRef
	rec.OutputDigest = outcome.OutputDigest
	rec.ErrorRef = outcome.ErrorRef
	rec.ToStepID = outcome.ToStepID
	rec.TransitionIndex = outcome.TransitionIndex
	rec.MatchDigest = outcome.MatchDigest
	rec.DecisionJSON = append([]byte(nil), outcome.DecisionJSON...)
	rec.EvidenceJSON = append([]byte(nil), outcome.EvidenceJSON...)
	prevActive := p.ActiveStepID
	prevTransitions := append([]TransitionRecord(nil), p.Transitions...)
	p.Attempts = append(p.Attempts, rec)
	// Mirror applyAttemptCompletionLocked's route/derived-step rules: a route
	// appends one transition record and moves the derived active step to the
	// route target; a route-less completion carries no step candidate.
	if outcome.ToStepID != "" {
		p.Transitions = append(p.Transitions, TransitionRecord{
			RunID:           attempt.RunID,
			FromAttemptID:   attempt.AttemptID,
			ToStepID:        outcome.ToStepID,
			TransitionIndex: outcome.TransitionIndex,
			MatchDigest:     outcome.MatchDigest,
			DecisionJSON:    append([]byte(nil), outcome.DecisionJSON...),
			CreatedAt:       now,
		})
		p.ActiveStepID = outcome.ToStepID
	}
	rollback := func() {
		q := s.proj[attempt.RunID]
		for i := range q.Attempts {
			if q.Attempts[i].AttemptID == attempt.AttemptID {
				q.Attempts = append(q.Attempts[:i], q.Attempts[i+1:]...)
				break
			}
		}
		q.Transitions = prevTransitions
		q.ActiveStepID = prevActive
		s.proj[attempt.RunID] = q
	}
	return rec, rollback
}

// GetStepAttempt returns one attempt. Returns ErrNotFound if absent.
func (s *StorageRepository) GetStepAttempt(ctx context.Context, runID, attemptID string) (StepAttempt, error) {
	if err := s.ensureBuilt(ctx); err != nil {
		return StepAttempt{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.proj[runID]
	if !ok || !p.HasRun {
		return StepAttempt{}, ErrNotFound
	}
	for i := range p.Attempts {
		if p.Attempts[i].AttemptID == attemptID {
			return p.Attempts[i].Clone(), nil
		}
	}
	return StepAttempt{}, ErrNotFound
}

// ListStepAttempts returns the run's attempts ordered by event sequence.
func (s *StorageRepository) ListStepAttempts(ctx context.Context, runID string) ([]StepAttempt, error) {
	if err := s.ensureBuilt(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.proj[runID]
	if !ok || !p.HasRun {
		return nil, ErrNotFound
	}
	out := make([]StepAttempt, 0, len(p.Attempts))
	for i := range p.Attempts {
		out = append(out, p.Attempts[i].Clone())
	}
	return out, nil
}

// CompleteStepAttempt atomically records an attempt's terminal outcome
// (status + optional route/output evidence in ONE event) under CAS on the
// attempt version. Returns ErrConflict on version mismatch, ErrInvalidTransition
// for a non-terminal outcome status or an illegal status edge.
func (s *StorageRepository) CompleteStepAttempt(ctx context.Context, runID, attemptID string, expectedVersion uint64, outcome AttemptOutcome) error {
	if err := s.ensureBuilt(ctx); err != nil {
		return err
	}
	lock := s.runLock(runID)
	lock.Lock()
	defer lock.Unlock()

	s.mu.Lock()
	p, ok := s.proj[runID]
	if !ok || !p.HasRun {
		s.mu.Unlock()
		return ErrNotFound
	}
	idx := -1
	for i := range p.Attempts {
		if p.Attempts[i].AttemptID == attemptID {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.mu.Unlock()
		return ErrNotFound
	}
	if p.Attempts[idx].Version != expectedVersion {
		s.mu.Unlock()
		return ErrConflict
	}
	if !IsTerminalAttemptStatus(outcome.Status) || !ValidAttemptTransition(p.Attempts[idx].Status, outcome.Status) {
		s.mu.Unlock()
		return ErrInvalidTransition
	}
	if outcomeHasRoute(outcome) && (outcome.Status == AttemptStatusInterrupted || outcome.Status == AttemptStatusCanceled || outcome.Status == AttemptStatusTimedOut) {
		s.mu.Unlock()
		return ErrInvalidTransition
	}
	if len(outcome.EvidenceJSON) > MaxEvidenceBytes {
		s.mu.Unlock()
		return fmt.Errorf("evidence exceeds %d bytes", MaxEvidenceBytes)
	}
	now := s.now()
	payload, rollback, err := s.applyAttemptCompletionLocked(&p, idx, outcome, now, runID, attemptID)
	stepID := p.Attempts[idx].StepID
	attemptNo := p.Attempts[idx].AttemptNo
	s.proj[runID] = p
	s.mu.Unlock()
	if err != nil {
		s.rollbackAndRebuild(ctx, runID, rollback)
		return fmt.Errorf("marshal %s payload: %w", eventKindAttemptCompleted, err)
	}

	evt := storage.Event{
		ID:       EventID(runID, eventKindAttemptCompleted, stepID, strconv.Itoa(attemptNo)),
		RunID:    runID,
		Sequence: int(s.nextSequence(runID)),
		Kind:     eventKindAttemptCompleted,
		Payload:  payload,
	}
	return s.appendEvent(ctx, evt, rollback)
}

func outcomeHasRoute(outcome AttemptOutcome) bool {
	return outcome.ToStepID != "" || outcome.TransitionIndex != 0 || outcome.MatchDigest != "" || len(outcome.DecisionJSON) != 0
}

// applyAttemptCompletionLocked applies a terminal outcome to the cached
// projection and builds the wf_attempt_completed payload. It must be called
// with the run's per-run mutex held (the caller holds s.mu while mutating);
// the returned rollback restores the projection on marshal/append failure.
func (s *StorageRepository) applyAttemptCompletionLocked(p *Projection, idx int, outcome AttemptOutcome, now time.Time, runID, attemptID string) ([]byte, func(), error) {
	cur := &p.Attempts[idx]
	prevAttempt := cur.Clone()
	prevTransitions := append([]TransitionRecord(nil), p.Transitions...)
	prevActive := p.ActiveStepID
	coordinatorRunID, taskID := outcome.CoordinatorRunID, outcome.TaskID
	if coordinatorRunID == "" {
		coordinatorRunID = cur.CoordinatorRunID
	}
	if taskID == "" {
		taskID = cur.TaskID
	}
	cur.Status = outcome.Status
	cur.CoordinatorRunID = coordinatorRunID
	cur.TaskID = taskID
	cur.OutputRef = outcome.OutputRef
	cur.OutputDigest = outcome.OutputDigest
	cur.ErrorRef = outcome.ErrorRef
	cur.ToStepID = outcome.ToStepID
	cur.TransitionIndex = outcome.TransitionIndex
	cur.MatchDigest = outcome.MatchDigest
	cur.DecisionJSON = append([]byte(nil), outcome.DecisionJSON...)
	cur.EvidenceJSON = append([]byte(nil), outcome.EvidenceJSON...)
	cur.FinishedAt = &now
	cur.Version++
	if outcome.ToStepID != "" {
		p.Transitions = append(p.Transitions, TransitionRecord{
			RunID:           runID,
			FromAttemptID:   attemptID,
			ToStepID:        outcome.ToStepID,
			TransitionIndex: outcome.TransitionIndex,
			MatchDigest:     outcome.MatchDigest,
			DecisionJSON:    append([]byte(nil), outcome.DecisionJSON...),
			CreatedAt:       now,
		})
	}
	// Refresh the derived active step exactly like RebuildProjection's
	// newest-step-bearing-event rule: a completion with a route moves to
	// to_step_id; one without a route (interrupted/canceled/timed_out, or a
	// failed completion without a route) carries no step, so the replay
	// keeps the previous candidate — the in-place mutation must NOT rewind
	// the derived step to the completed attempt's step.
	if outcome.ToStepID != "" {
		p.ActiveStepID = outcome.ToStepID
	}
	rollback := func() {
		q := s.proj[runID]
		if idx < len(q.Attempts) {
			q.Attempts[idx] = prevAttempt
		}
		q.Transitions = prevTransitions
		q.ActiveStepID = prevActive
		s.proj[runID] = q
	}
	payload, err := marshalAttemptCompleted(attemptCompletedPayload{
		AttemptID:        attemptID,
		Version:          cur.Version,
		Status:           outcome.Status,
		CoordinatorRunID: coordinatorRunID,
		TaskID:           taskID,
		OutputRef:        outcome.OutputRef,
		OutputDigest:     outcome.OutputDigest,
		ErrorRef:         outcome.ErrorRef,
		ToStepID:         outcome.ToStepID,
		TransitionIndex:  outcome.TransitionIndex,
		MatchDigest:      outcome.MatchDigest,
		DecisionJSON:     append([]byte(nil), outcome.DecisionJSON...),
		EvidenceJSON:     append([]byte(nil), outcome.EvidenceJSON...),
		FinishedAt:       now,
		CreatedAt:        now,
	})
	if err != nil {
		return nil, rollback, err
	}
	return payload, rollback, nil
}
