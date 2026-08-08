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
)

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
