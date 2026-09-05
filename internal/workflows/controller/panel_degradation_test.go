package controller

import (
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// envelopeReportedBy builds a synthesis envelope in which exactly these
// members contributed a report.
func envelopeReportedBy(ids ...string) PanelSynthesisEnvelope {
	env := PanelSynthesisEnvelope{StepID: "review_panel"}
	for _, id := range ids {
		env.Members = append(env.Members, PanelSynthesisMemberEnvelope{
			Provenance: PanelMemberProvenance{StepID: "review_panel", MemberID: id},
		})
	}
	return env
}

func attemptWithMembers(ids ...string) workflowledger.StepAttempt {
	members := make([]workflowledger.PanelMemberExecution, 0, len(ids))
	for i, id := range ids {
		members = append(members, workflowledger.PanelMemberExecution{MemberID: id, Order: i})
	}
	return workflowledger.StepAttempt{PanelExecution: &workflowledger.PanelExecution{Members: members}}
}

// TestPanelDegradationRecordsMissingMembers is the durable half of the
// allow_partial fix. A three-lens review_panel whose correctness and security
// members died still approved on the surviving lens, and the persisted
// PanelFinalReport looked identical to a full-strength pass - the only trace
// was a transient progress event nobody reads after the fact.
func TestPanelDegradationRecordsMissingMembers(t *testing.T) {
	attempt := attemptWithMembers("correctness", "security", "architecture")
	envelope := envelopeReportedBy("architecture")

	got := panelDegradationFromEnvelope(attempt, envelope)
	if got == nil {
		t.Fatal("a panel that lost two of three members recorded no degradation")
	}
	if got.AdmittedMembers != 3 || got.ReportedMembers != 1 {
		t.Fatalf("degradation = %+v, want 3 admitted and 1 reported", got)
	}
	if len(got.MissingMembers) != 2 {
		t.Fatalf("missing = %v, want the two absent members", got.MissingMembers)
	}
}

// TestPanelDegradationIsNilAtFullStrength keeps the field off the common case,
// so its presence always means something.
func TestPanelDegradationIsNilAtFullStrength(t *testing.T) {
	attempt := attemptWithMembers("correctness", "security")
	envelope := envelopeReportedBy("correctness", "security")
	if got := panelDegradationFromEnvelope(attempt, envelope); got != nil {
		t.Fatalf("degradation = %+v, want nil when every admitted member reported", got)
	}
}

// TestCleanMemberWithNoFindingsIsNotMissing is the trap this derivation must
// avoid: a member that reviewed and raised NOTHING contributes no canonical
// source keys, so counting keys instead of members would invent a degradation
// on a panel where every lens actually ran.
func TestCleanMemberWithNoFindingsIsNotMissing(t *testing.T) {
	attempt := attemptWithMembers("correctness", "security")
	// Both reported; neither raised a finding, so there are no source keys.
	envelope := envelopeReportedBy("correctness", "security")
	if len(AllCanonicalSourceKeys(envelope)) != 0 {
		t.Fatal("fixture unexpectedly carries source keys; it must have none")
	}
	if got := panelDegradationFromEnvelope(attempt, envelope); got != nil {
		t.Fatalf("degradation = %+v, want nil: both members reported, they just found nothing", got)
	}
}

// TestPanelDegradationIgnoresAttemptsWithoutPanelState guards the non-panel
// and not-yet-admitted shapes.
func TestPanelDegradationIgnoresAttemptsWithoutPanelState(t *testing.T) {
	if got := panelDegradationFromEnvelope(workflowledger.StepAttempt{}, PanelSynthesisEnvelope{}); got != nil {
		t.Fatalf("degradation = %+v, want nil for an attempt with no panel execution", got)
	}
	if got := panelDegradationFromEnvelope(attemptWithMembers(), PanelSynthesisEnvelope{}); got != nil {
		t.Fatalf("degradation = %+v, want nil for an attempt with no admitted members", got)
	}
}
