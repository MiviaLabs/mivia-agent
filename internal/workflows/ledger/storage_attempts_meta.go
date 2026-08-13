package ledger

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// SetStepAttemptPrompt records the content-addressed prompt reference for one
// attempt (the prompt body lives in content-addressed storage; only the ref is
// persisted). Unlike CompleteStepAttempt it does NOT require a terminal status:
// the prompt is written at dispatch time, while the attempt is Running, and the
// attempt's status/version are never changed. Setting the same promptRef twice
// is an idempotent no-op; setting a DIFFERENT promptRef on an attempt that
// already has one returns ErrConflict (attempts are immutable after dispatch).
// Returns ErrNotFound if the run or attempt is absent.
func (s *StorageRepository) SetStepAttemptPrompt(ctx context.Context, runID, attemptID, promptRef string) error {
	if promptRef == "" {
		return fmt.Errorf("prompt ref is empty")
	}
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
	if p.Attempts[idx].PromptRef != "" {
		if p.Attempts[idx].PromptRef == promptRef {
			s.mu.Unlock()
			return nil // idempotent retry of the same ref
		}
		s.mu.Unlock()
		return ErrConflict // attempts are immutable after dispatch
	}
	now := s.now()
	stepID := p.Attempts[idx].StepID
	attemptNo := p.Attempts[idx].AttemptNo
	payload, rollback, err := s.applyAttemptPromptLocked(&p, idx, promptRef, now, runID, attemptID)
	s.proj[runID] = p
	s.mu.Unlock()
	if err != nil {
		s.rollbackAndRebuild(ctx, runID, rollback)
		return fmt.Errorf("marshal %s payload: %w", eventKindAttemptPrompt, err)
	}

	evt := storage.Event{
		ID:       EventID(runID, eventKindAttemptPrompt, stepID, strconv.Itoa(attemptNo)),
		RunID:    runID,
		Sequence: int(s.nextSequence(runID)),
		Kind:     eventKindAttemptPrompt,
		Payload:  payload,
	}
	return s.appendEvent(ctx, evt, rollback)
}

// SetStepAttemptExecution records the active child identity before dispatch,
// together with the reason for the re-dispatch when one exists (a transient
// retry records the provider error text that triggered it; an initial dispatch
// records none). This closes the crash window where a transient retry has a
// new child in memory but the ledger still points at the old child.
func (s *StorageRepository) SetStepAttemptExecution(ctx context.Context, runID, attemptID, coordinatorRunID, taskID, reason string) error {
	if coordinatorRunID == "" || taskID == "" {
		return fmt.Errorf("execution identity is incomplete")
	}
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
	cur := p.Attempts[idx]
	if len(cur.Executions) > 0 {
		latest := cur.Executions[len(cur.Executions)-1]
		if latest.CoordinatorRunID == coordinatorRunID && latest.TaskID == taskID {
			s.mu.Unlock()
			return nil
		}
	} else if cur.CoordinatorRunID == coordinatorRunID && cur.TaskID == taskID {
		s.mu.Unlock()
		return nil
	}
	prev := cur.Clone()
	if len(cur.Executions) == 0 && cur.CoordinatorRunID != "" && cur.TaskID != "" {
		cur.Executions = append(cur.Executions, StepExecution{
			ExecutionNo: 1, CoordinatorRunID: cur.CoordinatorRunID, TaskID: cur.TaskID, StartedAt: cur.StartedAt,
		})
	}
	executionNo := len(cur.Executions) + 1
	cur.Executions = append(cur.Executions, StepExecution{
		ExecutionNo: executionNo, CoordinatorRunID: coordinatorRunID, TaskID: taskID, StartedAt: s.now(),
	})
	cur.CoordinatorRunID = coordinatorRunID
	cur.TaskID = taskID
	p.Attempts[idx] = cur
	s.proj[runID] = p
	s.mu.Unlock()
	now := cur.Executions[len(cur.Executions)-1].StartedAt
	payload, err := marshalAttemptExecution(attemptExecutionPayload{AttemptID: attemptID, ExecutionNo: executionNo, CoordinatorRunID: coordinatorRunID, TaskID: taskID, Reason: reason, CreatedAt: now})
	if err != nil {
		s.rollbackAndRebuild(ctx, runID, func() {
			q := s.proj[runID]
			q.Attempts[idx] = prev
			s.proj[runID] = q
		})
		return err
	}
	evt := storage.Event{ID: EventID(runID, eventKindAttemptExecution, attemptID, strconv.Itoa(executionNo), coordinatorRunID, taskID), RunID: runID, Sequence: int(s.nextSequence(runID)), Kind: eventKindAttemptExecution, Payload: payload}
	rollback := func() {
		q := s.proj[runID]
		q.Attempts[idx] = prev
		s.proj[runID] = q
	}
	return s.appendEvent(ctx, evt, rollback)
}

// applyAttemptPromptLocked records the prompt ref on the cached projection and
// builds the wf_attempt_prompt payload. It must be called with the run's
// per-run mutex held (the caller holds s.mu while mutating); the returned
// rollback restores the projection on marshal/append failure (mirroring
// applyAttemptCompletionLocked). The prompt contributes no step candidate and
// never changes the derived active step, exactly like the replay.
func (s *StorageRepository) applyAttemptPromptLocked(p *Projection, idx int, promptRef string, now time.Time, runID, attemptID string) ([]byte, func(), error) {
	cur := &p.Attempts[idx]
	prevPrompt := cur.PromptRef
	cur.PromptRef = promptRef
	rollback := func() {
		q := s.proj[runID]
		if idx < len(q.Attempts) {
			q.Attempts[idx].PromptRef = prevPrompt
		}
		s.proj[runID] = q
	}
	payload, err := marshalAttemptPrompt(attemptPromptPayload{
		AttemptID: attemptID,
		PromptRef: promptRef,
		CreatedAt: now,
	})
	if err != nil {
		return nil, rollback, err
	}
	return payload, rollback, nil
}

// ListTransitions returns the route decisions derived from completed
// attempts, ordered by event sequence.
func (s *StorageRepository) ListTransitions(ctx context.Context, runID string) ([]TransitionRecord, error) {
	if err := s.ensureBuilt(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.proj[runID]
	if !ok || !p.HasRun {
		return nil, ErrNotFound
	}
	out := make([]TransitionRecord, 0, len(p.Transitions))
	for i := range p.Transitions {
		out = append(out, p.Transitions[i].Clone())
	}
	return out, nil
}
