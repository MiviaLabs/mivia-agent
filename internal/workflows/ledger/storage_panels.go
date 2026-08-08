package ledger

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// CompareAndSetPanelPhase stores one panel phase intent under the workflow claim.
func (s *StorageRepository) CompareAndSetPanelPhase(ctx context.Context, runID string, attemptID string, expectedVersion uint64, from PanelPhase, to PanelPhase, synthesis *PanelSynthesisExecution) error {
	holder, ok := claimHolderFromContext(ctx)
	if !ok {
		return ErrClaimNotHeld
	}
	if err := s.ensureBuilt(ctx); err != nil {
		return err
	}
	lock := s.runLock(runID)
	lock.Lock()
	defer lock.Unlock()

	s.mu.Lock()
	p, exists := s.proj[runID]
	if !exists || !p.HasRun || p.Run == nil {
		s.mu.Unlock()
		return ErrNotFound
	}
	if IsTerminalRunStatus(p.Run.Status) {
		s.mu.Unlock()
		return ErrConflict
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
	current := p.Attempts[idx]
	if IsTerminalAttemptStatus(current.Status) || current.Version != expectedVersion || current.PanelExecution == nil || current.PanelExecution.Phase != from {
		s.mu.Unlock()
		return ErrConflict
	}
	if !validPanelTransition(from, to, synthesis) {
		s.mu.Unlock()
		return ErrInvalidTransition
	}
	if synthesis != nil {
		if err := s.validatePanelTaskContent(ctx, current.PanelExecution.SynthesisTaskID, synthesis.Work); err != nil {
			s.mu.Unlock()
			return err
		}
	}
	next := current.Clone()
	next.Version++
	next.PanelExecution.Phase = to
	if to == PanelPhaseSynthesisAdmitted {
		next.PanelExecution.Synthesis = synthesis.clone()
	}
	s.mu.Unlock()

	return s.appendPanelPhase(ctx, runID, attemptID, holder, to, synthesis, next, idx)
}

func (s *StorageRepository) appendPanelPhase(ctx context.Context, runID, attemptID, holder string, to PanelPhase, synthesis *PanelSynthesisExecution, next StepAttempt, idx int) error {
	payload, err := marshalPanelPhase(panelPhasePayload{AttemptID: attemptID, Version: next.Version, Phase: to, Synthesis: synthesis.clone()})
	if err != nil {
		return fmt.Errorf("marshal %s payload: %w", eventKindPanelPhaseSet, err)
	}
	evt := storage.Event{ID: EventID(runID, eventKindPanelPhaseSet, attemptID, strconv.FormatUint(next.Version, 10)), RunID: runID, Sequence: int(s.nextSequence(runID)), Kind: eventKindPanelPhaseSet, Payload: payload}
	appender, ok := s.store.(storage.ExistingClaimAppender)
	if !ok {
		return fmt.Errorf("store does not support existing claim append")
	}
	err = appender.AppendWithExistingClaim(ctx, evt, holder)
	if errors.Is(err, storage.ErrClaimHeld) {
		err = ErrClaimHeld
	}
	if err != nil {
		if errors.Is(err, storage.ErrDuplicate) {
			return ErrConflict
		}
		return err
	}
	s.mu.Lock()
	if p, ok := s.proj[runID]; ok && idx < len(p.Attempts) {
		p.Attempts[idx] = next
		s.proj[runID] = p
	}
	if uint64(evt.Sequence) > s.applied[runID] {
		s.applied[runID] = uint64(evt.Sequence)
	}
	s.mu.Unlock()
	return nil
}

func validPanelTransition(from, to PanelPhase, synthesis *PanelSynthesisExecution) bool {
	return validPanelTransitionWithWork(from, to, synthesis, false)
}

func validPanelTransitionWithWork(from, to PanelPhase, synthesis *PanelSynthesisExecution, allowMissingWorkFingerprint bool) bool {
	switch {
	case from == PanelPhaseMembersAdmitted && to == PanelPhaseSynthesisAdmitted:
		if synthesis == nil {
			return false
		}
		if allowMissingWorkFingerprint {
			return synthesis.Work.validateLegacy() == nil
		}
		return synthesis.Work.Validate() == nil
	case from == PanelPhaseMembersAdmitted && to == PanelPhaseCancelPending:
		return synthesis == nil
	case from == PanelPhaseSynthesisAdmitted && to == PanelPhaseCancelPending:
		return synthesis == nil
	default:
		return false
	}
}
