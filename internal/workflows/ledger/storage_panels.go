package ledger

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"time"

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
	synthesisTaskID := current.PanelExecution.SynthesisTaskID
	next := current.Clone()
	next.Version++
	next.PanelExecution.Phase = to
	if to == PanelPhaseSynthesisAdmitted {
		next.PanelExecution.Synthesis = synthesis.clone()
	}
	s.mu.Unlock()
	if synthesis != nil {
		if err := s.validatePanelTaskContent(ctx, runID, synthesisTaskID, synthesis.Work); err != nil {
			return err
		}
	}

	return s.appendPanelPhase(ctx, runID, attemptID, holder, to, synthesis, next, idx)
}

func (s *StorageRepository) appendPanelPhase(ctx context.Context, runID, attemptID, holder string, to PanelPhase, synthesis *PanelSynthesisExecution, next StepAttempt, idx int) error {
	payload, err := marshalPanelPhase(panelPhasePayload{AttemptID: attemptID, Version: next.Version, Phase: to, Synthesis: synthesis.clone(), CreatedAt: s.engine.Now()})
	if err != nil {
		return fmt.Errorf("marshal %s payload: %w", eventKindPanelPhaseSet, err)
	}
	evt := storage.Event{ID: EventID(runID, eventKindPanelPhaseSet, attemptID, strconv.FormatUint(next.Version, 10)), RunID: runID, Sequence: int(s.nextSequence(runID)), Kind: eventKindPanelPhaseSet, Payload: payload}
	appender, ok := s.engine.Store().(storage.ExistingClaimAppender)
	if !ok {
		return fmt.Errorf("store does not support existing claim append")
	}
	err = appender.AppendWithExistingClaim(ctx, evt, holder)
	if errors.Is(err, storage.ErrClaimHeld) {
		err = ErrClaimHeld
	}
	if err != nil {
		if !errors.Is(err, storage.ErrDuplicate) {
			if cerr := s.catchUpRunLocked(ctx, runID); cerr != nil {
				return fmt.Errorf("store append: %v; catch up: %w", err, cerr)
			}
			return err
		}
		// Duplicate: catch up (the rebuild replaces the projection with the
		// store's durable state) and compare the existing event's payload,
		// EXCLUDING CreatedAt (stamped fresh on every attempt), against the
		// payload this call intended to append. A matching retry is
		// idempotent (nil); a genuinely different transition is a conflict.
		if cerr := s.catchUpRunLocked(ctx, runID); cerr != nil {
			return fmt.Errorf("catch up after duplicate: %w", cerr)
		}
		events, rerr := s.engine.Store().Events(ctx, runID)
		if rerr != nil {
			return fmt.Errorf("read events after duplicate: %w", rerr)
		}
		for _, e := range events {
			if e.ID != evt.ID {
				continue
			}
			stored, uerr := unmarshalPanelPhase(e.Payload)
			if uerr != nil {
				return fmt.Errorf("decode %s payload: %w", eventKindPanelPhaseSet, uerr)
			}
			if panelPhasePayloadEqualIgnoringCreatedAt(stored, panelPhasePayload{AttemptID: attemptID, Version: next.Version, Phase: to, Synthesis: synthesis.clone()}) {
				return nil
			}
			return ErrConflict
		}
		// The duplicate came from the (run_id, sequence) UNIQUE constraint
		// with no event carrying our ID: the sequence was lost to another
		// writer.
		return ErrConflict
	}
	s.mu.Lock()
	if p, ok := s.proj[runID]; ok && idx < len(p.Attempts) {
		p.Attempts[idx] = next
		s.proj[runID] = p
	}
	s.engine.Watermarks().SetApplied(runID, uint64(evt.Sequence))
	s.mu.Unlock()
	return nil
}

// panelPhasePayloadEqualIgnoringCreatedAt reports whether two panel-phase
// payloads describe the same logical transition, ignoring CreatedAt (which
// is stamped fresh on every write and would otherwise always differ between
// a genuine retry and the original write).
func panelPhasePayloadEqualIgnoringCreatedAt(a, b panelPhasePayload) bool {
	a.CreatedAt = time.Time{}
	b.CreatedAt = time.Time{}
	return reflect.DeepEqual(a, b)
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
