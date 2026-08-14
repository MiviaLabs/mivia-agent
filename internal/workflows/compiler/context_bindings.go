package compiler

import (
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

// validateContextBindings checks that context source references are structurally valid.
// skipCycleValidation is true only for resume of an admitted snapshot; the
// evidence-cap check is admission-only, matching the cycle-check precedent,
// so an in-flight run admitted under an earlier policy is never stranded.
func validateContextBindings(wf *definition.WorkflowFile, stepIDs map[string]bool, skipCycleValidation bool) error {
	for _, s := range wf.Steps {
		for _, cb := range s.Context {
			if strings.TrimSpace(cb.From) == "" {
				return fmt.Errorf("step %q: context from is empty", s.ID)
			}
			// Reject path traversal
			if strings.Contains(cb.From, "..") {
				return fmt.Errorf("step %q: context from %q contains path traversal", s.ID, cb.From)
			}
			// Validate source format: inputs.<name>, steps.<id>.output,
			// delivery.failure, or run.salvage
			parts := strings.Split(cb.From, ".")
			switch parts[0] {
			case "inputs":
				if len(parts) != 2 {
					return fmt.Errorf("step %q: context from %q invalid (expected inputs.<name>)", s.ID, cb.From)
				}
				inputName := parts[1]
				if _, ok := wf.Inputs[inputName]; !ok {
					// chunk_plan is the engine-injected reserved input carrying
					// the chunk's decompose plan slice; only chunk-mode
					// admissions of a stacking workflow have it, so the binding
					// must be optional and stacking must be on.
					if inputName == "chunk_plan" && wf.Stacking != nil && wf.Stacking.StackingEnabled() {
						if !cb.Optional {
							return fmt.Errorf("step %q: context from %q must be optional (only chunk-mode runs carry chunk_plan)", s.ID, cb.From)
						}
						continue
					}
					return fmt.Errorf("step %q: context from %q references unknown input %q", s.ID, cb.From, inputName)
				}
			case "steps":
				if len(parts) != 3 || parts[2] != "output" {
					return fmt.Errorf("step %q: context from %q invalid (expected steps.<id>.output)", s.ID, cb.From)
				}
				stepID := parts[1]
				if !stepIDs[stepID] {
					return fmt.Errorf("step %q: context from %q references unknown step %q", s.ID, cb.From, stepID)
				}
				if !skipCycleValidation && stepID == s.ID && !cb.Optional {
					return fmt.Errorf("step %q: mandatory self-output context binding %q is impossible on its first attempt", s.ID, cb.From)
				}
				// The executor caps a prior step output bound into context at
				// MaxEvidenceBindingBytes, so reject larger requests at admission.
				// Admission-only: a run admitted under an earlier policy whose
				// snapshot carries a larger max_bytes binding must still resume
				// (the runtime cap on actual output bytes stays the safety bound).
				if !skipCycleValidation && cb.MaxBytes > definition.MaxEvidenceBindingBytes {
					return fmt.Errorf("step %q: context binding %q max_bytes %d exceeds maximum of %d for prior step evidence", s.ID, cb.From, cb.MaxBytes, definition.MaxEvidenceBindingBytes)
				}
			case "delivery":
				if len(parts) != 2 || parts[1] != "failure" {
					return fmt.Errorf("step %q: context from %q invalid (expected delivery.failure)", s.ID, cb.From)
				}
			case "run":
				if len(parts) != 2 || parts[1] != "salvage" {
					return fmt.Errorf("step %q: context from %q invalid (expected run.salvage)", s.ID, cb.From)
				}
			case "implement":
				if len(parts) != 2 || parts[1] != "touched_files" {
					return fmt.Errorf("step %q: context from %q invalid (expected implement.touched_files)", s.ID, cb.From)
				}
			default:
				return fmt.Errorf("step %q: context from %q invalid (must start with inputs. or steps.)", s.ID, cb.From)
			}
			if strings.TrimSpace(cb.As) == "" {
				return fmt.Errorf("step %q: context as is empty", s.ID)
			}
			if cb.MaxBytes < 0 {
				return fmt.Errorf("step %q: context binding %q max_bytes must be >= 0 (got %d)", s.ID, cb.From, cb.MaxBytes)
			}
			if cb.MaxBytes > definition.MaxInputBytes {
				return fmt.Errorf("step %q: context binding %q max_bytes %d exceeds maximum of %d", s.ID, cb.From, cb.MaxBytes, definition.MaxInputBytes)
			}
		}
	}
	return nil
}
