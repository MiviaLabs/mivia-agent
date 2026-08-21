package definition

import (
	"strings"
	"testing"
)

// TestCompile_InactiveDeliveryDoesNotSeedReentryStep is the negative control
// for the O3 admission fix: delivery re-entry seeding applies only to an
// ACTIVE delivery policy. A mode "none" (inactive) block that names an
// existing step in on_failure must not make that step reachable. The step is
// reachable only through the inactive delivery block, so Compile reports it
// unreachable.
func TestCompile_InactiveDeliveryDoesNotSeedReentryStep(t *testing.T) {
	wf := &WorkflowFile{
		Name:        "delivery-inactive-reentry",
		Version:     1,
		InitialStep: "plan",
		Steps: []Step{
			{ID: "plan", Kind: "agent", Agent: "planner"},
			{ID: "repair", Kind: "agent", Agent: "engineer"},
		},
		Transitions: []Transition{
			{From: "plan", To: "success", Match: MatchCriteria{Status: "succeeded"}},
			{From: "repair", To: "plan", Match: MatchCriteria{Status: "succeeded"}},
		},
		Delivery: &Delivery{
			Kind:      "pull_request",
			Mode:      "none", // inactive: no delivery runs, so no re-entry
			Provider:  "github",
			Base:      "main",
			OnFailure: "repair",
		},
	}
	_, err := Compile(wf)
	if err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("step named only in an inactive delivery block must stay unreachable, got %v", err)
	}
}

// TestCompile_OnPRMetadataFailureUnknownStepRejectedRegardlessOfActivity pins
// the typo rule for on_pr_metadata_failure: when the field is non-empty it
// must name a declared step, for an active policy and for an inactive one
// (kind "" or mode "none") alike.
func TestCompile_OnPRMetadataFailureUnknownStepRejectedRegardlessOfActivity(t *testing.T) {
	for _, tc := range []struct {
		name  string
		deliv *Delivery
	}{
		{"active draft", &Delivery{Kind: "pull_request", Mode: "draft", Provider: "github", Base: "main"}},
		{"inactive mode none", &Delivery{Kind: "pull_request", Mode: "none", Provider: "github", Base: "main"}},
		{"inactive empty kind", &Delivery{Kind: ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wf := newMinimalWorkflow("delivery-unknown-on-pr-metadata-failure")
			wf.Delivery = tc.deliv
			wf.Delivery.OnPRMetadataFailure = "ghost"
			assertCompileError(t, wf, "unknown on_pr_metadata_failure step", "names no step")
		})
	}
}

// TestCompile_OnFailureUnknownStepRejectedInactiveDelivery pins the same typo
// rule for delivery.on_failure when the policy is inactive. Before the O3
// fix, kind "" skipped the on_failure existence check entirely, so a bogus
// name passed admission. A non-empty field names a declared step or the
// workflow fails admission, active or not.
func TestCompile_OnFailureUnknownStepRejectedInactiveDelivery(t *testing.T) {
	for _, tc := range []struct {
		name  string
		deliv *Delivery
	}{
		{"inactive mode none", &Delivery{Kind: "pull_request", Mode: "none", Provider: "github", Base: "main"}},
		{"inactive empty kind", &Delivery{Kind: ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wf := newMinimalWorkflow("delivery-unknown-on-failure")
			wf.Delivery = tc.deliv
			wf.Delivery.OnFailure = "ghost"
			assertCompileError(t, wf, "unknown on_failure step", "names no step")
		})
	}
}
