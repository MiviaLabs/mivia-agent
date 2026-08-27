package definition

import (
	"testing"
)

// TestCompile_DeliveryReentryStepMustBindDeliveryFailure pins the guard that
// prevents the blind delivery-repair loop: a step a delivery failure can
// re-enter must bind delivery.failure so the repair agent deterministically
// receives the rejection text that routed it. Before this rule, a commit-hook
// rejection (e.g. "new blank line at EOF") routed to a repair step whose
// context carried only the PASSING gate output; the agent could not see the
// rejection, delivery re-failed identically, and the run spun to its deadline.
func TestCompile_DeliveryReentryStepMustBindDeliveryFailure(t *testing.T) {
	t.Run("on_failure step without the binding is rejected", func(t *testing.T) {
		wf := deliveryReentryWorkflow()
		assertCompileError(t, wf, "delivery on_failure re-entry step", "must bind delivery.failure")
	})

	t.Run("on_pr_metadata_failure step without the binding is rejected", func(t *testing.T) {
		wf := deliveryReentryWorkflow()
		wf.Delivery.OnFailure = ""
		wf.Delivery.OnPRMetadataFailure = "repair"
		assertCompileError(t, wf, "delivery on_pr_metadata_failure re-entry step", "must bind delivery.failure")
	})

	t.Run("binding on the re-entry step compiles", func(t *testing.T) {
		wf := deliveryReentryWorkflow()
		wf.Steps[1].Context = []ContextBinding{
			{From: "delivery.failure", As: "delivery_hint", MaxBytes: 8192, Optional: true},
		}
		if _, err := Compile(wf); err != nil {
			t.Fatalf("re-entry step with delivery.failure binding must compile: %v", err)
		}
	})
}

// deliveryReentryWorkflow is a minimal active-policy workflow whose
// delivery.on_failure names a repair step that carries no context at all.
func deliveryReentryWorkflow() *WorkflowFile {
	wf := newMinimalWorkflow("delivery-reentry-hint")
	wf.Steps = append(wf.Steps, Step{ID: "repair", Kind: "agent", Agent: "engineer"})
	wf.Transitions = append(wf.Transitions, Transition{
		From: "repair", To: "plan", Match: MatchCriteria{Status: "succeeded"},
	})
	wf.Delivery = &Delivery{
		Kind: "pull_request", Mode: "draft", Provider: "github", Base: "main",
		OnFailure: "repair",
	}
	return wf
}
