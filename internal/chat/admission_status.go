package chat

import (
	"fmt"
	"slices"
)

// Deferral reasons complete the phrase "deferred because <reason>" in the
// model-facing denial. Each names the real cause, not a generic excuse.
const (
	deferralReasonActiveTurn    = "another turn is active"
	deferralReasonSwitching     = "the agent surface is switching"
	deferralReasonOrchestration = "background orchestration is active"
	deferralReasonWidener       = "the surface publisher refused the update"
	deferralReasonStagingTurn   = "the staging turn has not completed"
)

// stageOwnedAtOrAfter reports whether any staged name is owned by a turn at or
// after turnID (ignoring owner 0, which means "no owning turn").
func stageOwnedAtOrAfter(stage *AdmissionStage, turnID uint64) bool {
	for _, owners := range stage.nameOwners {
		for owner := range owners {
			if owner != 0 && owner >= turnID {
				return true
			}
		}
	}
	return false
}

// deferAdmissionLocked keeps a stage pending and, up to a bounded count, tells
// the user why their tools have not appeared yet. reason is recorded on the
// session so the model-facing denial can announce it mid-turn.
func (s *Session) deferAdmissionLocked(reason string) {
	s.admissionDeferralReason = reason
	s.admissionDeferrals++
	if s.admissionDeferrals > maxAdmissionDeferralNotes || s.pendingAdmission == nil {
		return
	}
	s.admissionNotes = append(s.admissionNotes,
		fmt.Sprintf("tool loading deferred: %s could not be added to the tool surface yet (%s); it will be retried at the next turn boundary",
			boundedNames(s.pendingAdmission.Names, maxAdmissionNoteNames), reason))
}

// PendingAdmissionStatus reports the names awaiting publication and why the
// last boundary deferred. ok is false when no stage is pending. The staged-tool
// denial and the load_tools result announce the reason, so the model learns the
// cause mid-turn instead of probing one turn at a time.
func (s *Session) PendingAdmissionStatus() (names []string, reason string, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.pendingAdmission == nil {
		return nil, "", false
	}
	return slices.Clone(s.pendingAdmission.Names), s.admissionDeferralReason, true
}

// PublishPendingAdmission attempts the turn-boundary surface publication for a
// stage recorded during turn. It is called after the turn's history is durably
// committed, so the generation bump can never fence that turn out of its own
// persistence (plan tools/05 D6 ordering).
//
// A stage that cannot publish now stays pending for the next qualifying
// boundary; a stage whose binding has been replaced is dropped.
func (s *Session) PublishPendingAdmission() { s.publishPendingAdmission(true) }

// PublishPendingAdmissionAtTurnStart attempts publication at the start of a
// turn, before the loop runs. A stage whose owning turns have all finished may
// publish here - the earliest safe point, so the load_tools "next turn"
// promise holds (DC-9). A stage still owned by the current, not yet run turn
// stays deferred: its own boundary, after its durable commit, is the first
// allowed point (D7).
func (s *Session) PublishPendingAdmissionAtTurnStart() { s.publishPendingAdmission(false) }

// publishPendingAdmission is the shared publication core. allowCurrentTurn
// admits a stage owned by the current turn (the boundary case, where that turn
// has durably committed); false refuses it (the turn-start case, where the
// staging turn has not run yet).
func (s *Session) publishPendingAdmission(allowCurrentTurn bool) {
	s.mu.Lock()
	stage := s.pendingAdmission
	widener := s.surfaceWidener
	if stage == nil {
		s.admissionDeferralReason = ""
		s.mu.Unlock()
		return
	}
	if widener == nil || stage.SurfaceGeneration != s.agentSurfaceGeneration {
		// The binding this stage was authored against is gone (/agent switch),
		// or no host publisher exists. Either way the stage is void.
		s.pendingAdmission = nil
		s.admissionDeferralReason = ""
		s.mu.Unlock()
		return
	}
	if !allowCurrentTurn && stageOwnedAtOrAfter(stage, s.turnID) {
		// The staging turn has not run yet: publication must wait for that
		// turn's own boundary, after its durable commit (D7).
		s.deferAdmissionLocked(deferralReasonStagingTurn)
		s.mu.Unlock()
		return
	}
	// Sole-active-turn is what makes closing the old dispatcher safe: no
	// sibling turn and no background run can still be executing on it (R2-1).
	// It is also what makes publishing another turn's stage safe: the owning
	// turn is either still active - in which case this boundary defers - or it
	// has already reached its own boundary, where a superseded or errored turn
	// drops its stage. So a stage that survives to a quiet boundary is
	// publishable even when that boundary belongs to a later turn.
	if s.switching {
		s.deferAdmissionLocked(deferralReasonSwitching)
		s.mu.Unlock()
		return
	}
	if s.activeTurns != 1 {
		s.deferAdmissionLocked(deferralReasonActiveTurn)
		s.mu.Unlock()
		return
	}
	admitted := append(slices.Clone(s.admittedTools), stage.Names...)
	generation := s.agentSurfaceGeneration
	// Require the CURRENT turn, not the staging one: the atomic re-check in
	// TryPublishAgentSurface exists to catch a turn starting between this
	// check and the swap, which is the R2-1 hazard.
	turnID := s.turnID
	prompt, maxSteps := s.SystemPrompt, s.MaxSteps
	s.mu.Unlock()

	// Background orchestration that outlives the turn still owns this
	// session's dispatcher; widening would close it underneath (R2-2).
	if err := s.CheckSwitchAllowed(); err != nil {
		s.mu.Lock()
		s.deferAdmissionLocked(deferralReasonOrchestration)
		s.mu.Unlock()
		return
	}
	published, err := widener(admitted, AgentSurfacePublication{
		Prompt: prompt, MaxSteps: maxSteps,
		RequireTurnID: turnID, RequireSurfaceGeneration: generation,
		RequireSoleActiveTurn: true,
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil || !published {
		s.deferAdmissionLocked(deferralReasonWidener)
		return
	}
	s.admittedTools = admitted
	s.pendingAdmission = nil
	s.admissionPublications++
	s.admissionDeferrals = 0
	s.admissionDeferralReason = ""
}
