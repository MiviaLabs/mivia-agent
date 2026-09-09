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
	deferralReasonSuperseded    = "the staging turn was superseded"
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
		fmt.Sprintf("tool loading deferred: %s could not be added to the tool surface yet (%s); it will be retried at the next step boundary",
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

// hotServeEligible reports whether a call to a pending-staged name may be
// SERVED synchronously on the current surface instead of answered with the
// staged-but-not-published notice.
//
// Hot-serve executes on the dispatcher that already exists and widens
// nothing, so the publication-side fencing does not apply: a sibling turn
// (R2-1) or background orchestration (R2-2) blocks WIDENING - closing the old
// dispatcher under someone else - but never this serve. Only the two states
// in which the current surface itself is about to be replaced or closed keep
// the notice: an agent switch in flight, and a switch guard refusing on
// behalf of background work. There the wait is real, and the notice - which
// publication will make true - is the honest answer.
func (s *Session) hotServeEligible(name string) bool {
	s.mu.RLock()
	staged := s.pendingAdmission != nil && slices.Contains(s.pendingAdmission.Names, name)
	switching := s.switching
	s.mu.RUnlock()
	if !staged || switching {
		return false
	}
	return s.CheckSwitchAllowed() == nil
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
// publish here - the earliest safe point, so the load_tools "next step"
// promise holds (DC-9). A stage still owned by the current, not yet run turn
// stays deferred: its own boundary - the staging turn's first step boundary or
// its durable commit - is the first allowed point (D7).
func (s *Session) PublishPendingAdmissionAtTurnStart() { s.publishPendingAdmission(false) }

// PublishPendingAdmissionAtStepBoundary attempts the mid-turn publication of a
// stage owned by the CURRENT, executing turn at a step boundary. The loop
// invokes it through the host Surface hook before building the next step's
// request, so a tool staged by load_tools is callable from the next model
// step. skipMessageRewrite is set: s.Messages is the loop's in-flight history
// until the turn commit, so the publish must not rewrite the system message or
// memory frame (the system prompt is byte-identical across admissions anyway).
// The caller re-captures the turn's operation token so the same turn's commit
// still succeeds under the post-publication fence (the turn-start analog:
// chat-turnstart-admission-fences-own-turn). The same R2-1/R2-2/switch/generation
// checks apply as at turn boundaries; a deferred stage stays pending for the
// next qualifying boundary.
//
// It returns whether a publication occurred (a no-op when nothing is pending).
// On success it ALSO re-captures the executing turn's operation token into
// liveTurnToken: the publication bumped the operation fence
// (TryPublishAgentSurface -> invalidateLocked), which would otherwise fence
// this turn's own commit out of commitPreparedTurn. sendAgent reads the
// re-captured token via commitTurnToken, gated on the committing turn's id so
// a superseded turn can never borrow a newer turn's fence.
func (s *Session) PublishPendingAdmissionAtStepBoundary() bool {
	if !s.publishPendingAdmissionFull(true, true) {
		return false
	}
	s.mu.Lock()
	s.liveTurnToken = s.captureOperationTokenLocked(fmt.Sprintf("turn:%d", s.turnID))
	s.mu.Unlock()
	return true
}

// publishPendingAdmission is the turn-boundary publication entry point: the
// end-of-turn path (after the turn's durable commit) admits the current turn's
// own stage; the turn-start path defers it (D7).
func (s *Session) publishPendingAdmission(allowCurrentTurn bool) {
	s.publishPendingAdmissionFull(allowCurrentTurn, false)
}

// publishPendingAdmissionFull is the shared publication core. allowCurrentTurn
// admits a stage owned by the current turn (the boundary case, where that turn
// has durably committed or is executing at a step boundary); false refuses it
// (the turn-start case, where the staging turn has not run yet).
// skipMessageRewrite suppresses the system/memory message rewrites inside
// TryPublishAgentSurface for mid-turn (step-boundary) publications. It returns
// whether a publication occurred.
func (s *Session) publishPendingAdmissionFull(allowCurrentTurn, skipMessageRewrite bool) bool {
	s.mu.Lock()
	stage := s.pendingAdmission
	widener := s.surfaceWidener
	if stage == nil {
		s.admissionDeferralReason = ""
		s.mu.Unlock()
		return false
	}
	if widener == nil || stage.SurfaceGeneration != s.agentSurfaceGeneration {
		// The binding this stage was authored against is gone (/agent switch),
		// or no host publisher exists. Either way the stage is void.
		s.pendingAdmission = nil
		s.admissionDeferralReason = ""
		s.mu.Unlock()
		return false
	}
	if !allowCurrentTurn && stageOwnedAtOrAfter(stage, s.turnID) {
		// The staging turn has not run yet: publication must wait for that
		// turn's own boundary, after its durable commit (D7).
		s.deferAdmissionLocked(deferralReasonStagingTurn)
		s.mu.Unlock()
		return false
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
		return false
	}
	if s.activeTurns != 1 {
		s.deferAdmissionLocked(deferralReasonActiveTurn)
		s.mu.Unlock()
		return false
	}
	if skipMessageRewrite && !stage.Token.zero() && !s.tokenCurrentLocked(stage.Token) {
		// The step-boundary path exists for the CURRENT turn's own stage: a
		// tool staged by load_tools during this turn becomes callable from the
		// next model step. A stage whose staging turn was superseded by a
		// force-send (or whose fence moved) must not publish here - it stays
		// pending for a turn-boundary publication (turn-start/end), which DC-9
		// still allows once the owning turn is done.
		s.deferAdmissionLocked(deferralReasonSuperseded)
		s.mu.Unlock()
		return false
	}
	admitted := append(slices.Clone(s.admittedTools), stage.Names...)
	generation := s.agentSurfaceGeneration
	// Require the CURRENT turn, not the staging one: the atomic re-check in
	// TryPublishAgentSurface exists to catch a turn starting between this
	// check and the swap, which is the R2-1 hazard.
	turnID := s.turnID
	// BaseSystemPrompt, not SystemPrompt: avoids double-composing the memory
	// block on republish (plan 77, E3).
	prompt, maxSteps := s.BaseSystemPrompt, s.MaxSteps
	pub := AgentSurfacePublication{
		Prompt: prompt, MaxSteps: maxSteps,
		RequireTurnID: turnID, RequireSurfaceGeneration: generation,
		RequireSoleActiveTurn: true,
		SkipMessageRewrite:    skipMessageRewrite,
	}
	s.mu.Unlock()
	return s.widenSurface(widener, admitted, stage, pub)
}

// widenSurface is the tail of publishPendingAdmissionFull: the checks that
// must run outside the session lock, the host widener call, and the success
// bookkeeping. Background orchestration that outlives the turn still owns this
// session's dispatcher; widening would close it underneath (R2-2).
func (s *Session) widenSurface(widener SurfaceWidener, admitted []string, stage *AdmissionStage, pub AgentSurfacePublication) bool {
	if err := s.CheckSwitchAllowed(); err != nil {
		s.mu.Lock()
		s.deferAdmissionLocked(deferralReasonOrchestration)
		s.mu.Unlock()
		return false
	}
	published, err := widener(admitted, pub)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil || !published {
		s.deferAdmissionLocked(deferralReasonWidener)
		return false
	}
	s.admittedTools = admitted
	s.pendingAdmission = nil
	s.admissionPublications++
	s.admissionDeferrals = 0
	s.admissionDeferralReason = ""
	return true
}
