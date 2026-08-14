package compiler

import (
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

// validateLimitsAndStacking runs the [limits] and [stacking] config
// validators and joins their errors, so the aggregator reports every broken
// section at once.
func validateLimitsAndStacking(wf *definition.WorkflowFile, stepIDs map[string]bool, resume bool) error {
	var errs []string
	if err := validateLimits(wf.Limits); err != nil {
		errs = append(errs, err.Error())
	}
	if err := validateStacking(wf, stepIDs, resume); err != nil {
		errs = append(errs, err.Error())
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// validateStacking checks the [stacking] section. Stacking is opt-in and
// fail-closed: a workflow participates only when it declares the table, and
// an enabled table must name its plan_step and implement_step explicitly.
// Unknown step references, out-of-range thresholds, and invalid merge
// policies are errors. On resume of an admitted snapshot the explicit-steps
// requirement is waived: a step-less enabled table compiles with stacking
// inactive, so no admitted run strands.
func validateStacking(wf *definition.WorkflowFile, stepIDs map[string]bool, resume bool) error {
	if wf.Stacking == nil {
		// No [stacking] table: the workflow does not participate in stacking
		// and no semantics apply.
		return nil
	}
	s := wf.Stacking
	if !s.StackingEnabled() {
		// Deliberate opt-out: shape is already enforced by the strict TOML
		// decoder, and no semantics apply.
		return nil
	}
	if !resume && (s.PlanStep == "" || s.ImplementStep == "") {
		return fmt.Errorf("stacking: plan_step and implement_step are required when stacking is enabled")
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
