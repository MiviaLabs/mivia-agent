package compiler

import (
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

// deliveryActive reports whether d declares an active pull_request delivery
// policy: kind "pull_request" with an explicit mode other than "none". Runs
// with an active policy settle at delivery_pending on their success route
// instead of moving directly to succeeded. It is the single predicate every
// layer uses to decide whether delivery runs at all: re-entry seeding in
// validateGraph and the policy-shape checks in validateDelivery both key off
// it.
func deliveryActive(d *definition.Delivery) bool {
	return d != nil &&
		d.Kind == "pull_request" &&
		d.Mode != "" && d.Mode != "none"
}

// validateDelivery checks that the delivery configuration is structurally valid.
func validateDelivery(wf *definition.WorkflowFile) error {
	if wf.Delivery == nil {
		return nil
	}
	// pr_title_policy is a workflow-relative path. Reject an absolute path
	// and any path with a parent-directory segment (".."). Split on "/" and
	// compare each segment, so a name that merely contains ".." stays valid.
	if p := wf.Delivery.PRTitlePolicy; p != "" {
		if strings.HasPrefix(p, "/") {
			return fmt.Errorf("delivery: pr_title_policy %q must be a relative path", p)
		}
		for _, segment := range strings.Split(p, "/") {
			if segment == ".." {
				return fmt.Errorf("delivery: pr_title_policy %q must not contain a parent-directory segment", p)
			}
		}
	}
	// on_pr_metadata_failure names the step that repairs PR-metadata delivery
	// failures. A name that no step carries fails admission, like on_failure.
	if m := wf.Delivery.OnPRMetadataFailure; m != "" && !stepExists(wf, m) {
		return fmt.Errorf("delivery: on_pr_metadata_failure %q names no step", m)
	}
	// on_diff_size_failure names the step that repairs an over-limit delivered
	// diff (a stacking hard_lines rejection). A name that no step carries
	// fails admission, like on_failure.
	if d := wf.Delivery.OnDiffSizeFailure; d != "" && !stepExists(wf, d) {
		return fmt.Errorf("delivery: on_diff_size_failure %q names no step", d)
	}
	// on_failure names the step the run returns to when delivery fails for a
	// repairable reason. The existence checks for both targets run whether the
	// policy is active or not: a non-empty name that no step carries is a
	// typo, and a typo stays a typo even in an inactive block (kind "" or
	// mode "none"). Reachability is a separate concern: validateGraph seeds
	// re-entry targets only for an ACTIVE policy, so a step named only in an
	// inactive block stays flagged unreachable.
	if f := wf.Delivery.OnFailure; f != "" && !stepExists(wf, f) {
		return fmt.Errorf("delivery: on_failure %q names no step", f)
	}
	// max_repairs is a non-negative repair-cycle budget. Zero selects the
	// delivery package default; a negative value is a config error and cannot
	// mean "unbounded" (unbounded repair cycles burn the run deadline).
	if wf.Delivery.MaxRepairs < 0 {
		return fmt.Errorf("delivery: max_repairs must be >= 0 (zero selects the default); got %d", wf.Delivery.MaxRepairs)
	}
	switch wf.Delivery.Kind {
	case "":
		if wf.Delivery.Mode != "" && wf.Delivery.Mode != "none" {
			return fmt.Errorf("delivery: kind is empty but mode %q is set; use kind = \"pull_request\" or mode = \"none\"", wf.Delivery.Mode)
		}
		return nil
	case "pull_request":
		switch wf.Delivery.Mode {
		case "none", "draft", "ready":
			// valid
		default:
			return fmt.Errorf("delivery: mode %q is not valid (must be one of: none, draft, ready)", wf.Delivery.Mode)
		}
		if wf.Delivery.Provider == "" {
			return fmt.Errorf("delivery: provider must be non-empty")
		}
		if wf.Delivery.Base == "" {
			return fmt.Errorf("delivery: base must be non-empty")
		}
		return nil
	default:
		return fmt.Errorf("delivery: kind %q is not recognized (must be \"pull_request\" or empty)", wf.Delivery.Kind)
	}
}

// validateDeliveryProviderSupport refuses a delivery provider the engine
// cannot publish with: only definition.ProviderGitHub is supported, and the
// refusal states that boundary instead of implying a provider seam that does
// not exist. The check is admission-only, like the re-entry-hint requirement:
// a run admitted under an earlier policy that permitted the provider value
// must still resume. Delivery keeps its own authoritative check
// (delivery.Policy.Validate).
func validateDeliveryProviderSupport(wf *definition.WorkflowFile) error {
	if wf.Delivery == nil || wf.Delivery.Kind != "pull_request" {
		return nil
	}
	if wf.Delivery.Provider != definition.ProviderGitHub {
		return fmt.Errorf("delivery: provider %q is not supported (only %q is currently supported)", wf.Delivery.Provider, definition.ProviderGitHub)
	}
	return nil
}

// validateDeliveryReentryHints requires every step a delivery failure can
// re-enter (delivery.on_failure, delivery.on_pr_metadata_failure and
// delivery.on_diff_size_failure) to bind delivery.failure, so the repair
// agent deterministically receives the rejection that routed it. Without the
// hint, the repair step sees only the gates that passed and delivery re-fails
// identically forever - the blind loop observed when a commit-hook rejection
// routed a step whose context carried only the passing structure-gate output.
// Inactive delivery blocks never run, so they impose no binding requirement.
func validateDeliveryReentryHints(wf *definition.WorkflowFile) error {
	if !deliveryActive(wf.Delivery) {
		return nil
	}
	for _, target := range []string{wf.Delivery.OnFailure, wf.Delivery.OnPRMetadataFailure, wf.Delivery.OnDiffSizeFailure} {
		if target == "" || !stepExists(wf, target) {
			continue
		}
		if !stepBindsDeliveryFailure(wf, target) {
			return fmt.Errorf("delivery: re-entry step %q must bind delivery.failure (optional) so the repair agent sees the rejection that routed it", target)
		}
	}
	return nil
}

// stepBindsDeliveryFailure reports whether the named step declares a
// delivery.failure context binding.
func stepBindsDeliveryFailure(wf *definition.WorkflowFile, id string) bool {
	for _, s := range wf.Steps {
		if s.ID != id {
			continue
		}
		for _, cb := range s.Context {
			if cb.From == "delivery.failure" {
				return true
			}
		}
	}
	return false
}
