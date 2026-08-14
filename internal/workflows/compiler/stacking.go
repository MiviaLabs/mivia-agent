package compiler

import (
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

// validateLimitsAndStacking runs the [limits] and [stacking] config
// validators and joins their errors, so the aggregator reports every broken
// section at once.
func validateLimitsAndStacking(wf *definition.WorkflowFile, stepIDs map[string]bool) error {
	var errs []string
	if err := validateLimits(wf.Limits); err != nil {
		errs = append(errs, err.Error())
	}
	if err := validateStacking(wf, stepIDs); err != nil {
		errs = append(errs, err.Error())
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// validateStacking checks the [stacking] section. Explicit configuration is
// fail-closed: unknown step references, out-of-range thresholds, and invalid
// merge policies are errors. When the section is absent (stacking on by
// default), inference is best-effort and never errors, so existing workflows
// keep compiling unchanged.
func validateStacking(wf *definition.WorkflowFile, stepIDs map[string]bool) error {
	if wf.Stacking == nil {
		return nil
	}
	s := wf.Stacking
	if !s.StackingEnabled() {
		// Deliberate opt-out: shape is already enforced by the strict TOML
		// decoder, and no semantics apply.
		return nil
	}
	if s.PlanStep != "" && !stepIDs[s.PlanStep] {
		return fmt.Errorf("stacking: plan_step %q is not a declared step", s.PlanStep)
	}
	if s.ImplementStep != "" && !stepIDs[s.ImplementStep] {
		return fmt.Errorf("stacking: implement_step %q is not a declared step", s.ImplementStep)
	}
	if s.MaxChunks < 0 || s.MaxChunks > 100 {
		return fmt.Errorf("stacking: max_chunks must be in range [0, 100] (got %d)", s.MaxChunks)
	}
	if s.SoftLines < 0 || s.SoftLines > 100000 {
		return fmt.Errorf("stacking: soft_lines must be in range [0, 100000] (got %d)", s.SoftLines)
	}
	if s.HardLines < 0 || s.HardLines > 100000 {
		return fmt.Errorf("stacking: hard_lines must be in range [0, 100000] (got %d)", s.HardLines)
	}
	if s.MaxFiles < 0 || s.MaxFiles > 1000 {
		return fmt.Errorf("stacking: max_files must be in range [0, 1000] (got %d)", s.MaxFiles)
	}
	if s.SoftLines > 0 && s.HardLines > 0 && s.SoftLines > s.HardLines {
		return fmt.Errorf("stacking: soft_lines %d exceeds hard_lines %d", s.SoftLines, s.HardLines)
	}
	if s.MergePolicy != "" && !definition.ValidStackingMergePolicies[s.MergePolicy] {
		return fmt.Errorf("stacking: merge_policy %q must be one of approve, auto", s.MergePolicy)
	}
	if s.MaxTotalChunks < 0 || s.MaxTotalChunks > 2000 {
		return fmt.Errorf("stacking: max_total_chunks must be in range [0, 2000] (got %d)", s.MaxTotalChunks)
	}
	if s.MaxWaveChunks < 0 || s.MaxWaveChunks > 100 {
		return fmt.Errorf("stacking: max_wave_chunks must be in range [0, 100] (got %d)", s.MaxWaveChunks)
	}
	if s.MaxConcurrentChunks < 0 || s.MaxConcurrentChunks > 64 {
		return fmt.Errorf("stacking: max_concurrent_chunks must be in range [0, 64] (got %d)", s.MaxConcurrentChunks)
	}
	if s.MaxWaveChunks > 0 && s.MaxTotalChunks > 0 && s.MaxWaveChunks > s.MaxTotalChunks {
		return fmt.Errorf("stacking: max_wave_chunks %d exceeds max_total_chunks %d", s.MaxWaveChunks, s.MaxTotalChunks)
	}
	if s.SplitMaxChunks < 0 || s.SplitMaxChunks > 50 {
		return fmt.Errorf("stacking: split_max_chunks must be in range [0, 50] (got %d)", s.SplitMaxChunks)
	}
	if s.SplitMinLines < 0 || s.SplitMinLines > 10000 {
		return fmt.Errorf("stacking: split_min_lines must be in range [0, 10000] (got %d)", s.SplitMinLines)
	}
	return nil
}

// InferImplementStep identifies the workflow's implement step for stacking:
// the step whose output schema is the change-summary schema, else a step
// whose id is "implement". Returns "" when neither matches.
func InferImplementStep(wf *definition.WorkflowFile) string {
	for _, s := range wf.Steps {
		if s.OutputSchema == "schemas/change-summary-v1.json" {
			return s.ID
		}
	}
	for _, s := range wf.Steps {
		if s.ID == "implement" {
			return s.ID
		}
	}
	return ""
}

// InferPlanStep identifies the workflow's planning step for stacking: the
// step whose output implementStep binds into context as "plan". Returns ""
// when no such binding exists.
func InferPlanStep(wf *definition.WorkflowFile, implementStep string) string {
	if implementStep == "" {
		return ""
	}
	for _, s := range wf.Steps {
		if s.ID != implementStep {
			continue
		}
		for _, cb := range s.Context {
			if cb.As != "plan" {
				continue
			}
			parts := strings.Split(cb.From, ".")
			if len(parts) == 3 && parts[0] == "steps" && parts[2] == "output" {
				return parts[1]
			}
		}
	}
	return ""
}

// ResolveStackingSteps returns the effective plan and implement step ids for
// a workflow: explicit [stacking] values win (already validated by
// validateStacking), otherwise best-effort inference from the step graph.
func ResolveStackingSteps(wf *definition.WorkflowFile) (planStep, implementStep string) {
	if wf.Stacking != nil {
		planStep = wf.Stacking.PlanStep
		implementStep = wf.Stacking.ImplementStep
	}
	if implementStep == "" {
		implementStep = InferImplementStep(wf)
	}
	if planStep == "" {
		planStep = InferPlanStep(wf, implementStep)
	}
	return planStep, implementStep
}
