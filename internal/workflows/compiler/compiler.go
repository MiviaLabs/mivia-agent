package compiler

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/verifier"
)

// CompiledWorkflow is the immutable result of successful compilation.
type CompiledWorkflow struct {
	Name        string
	Description string
	Version     int
	InitialStep string
	Inputs      map[string]definition.InputDef
	Limits      definition.Limits
	Steps       []definition.Step
	Transitions []definition.Transition
	Delivery    *definition.Delivery
	Digest      string

	// Stacking is the resolved stacking configuration when stacking is
	// enabled (explicitly or by default) and both the plan and implement
	// steps resolved (declared or inferred). Nil otherwise; the run then
	// behaves exactly as a single-PR workflow always did.
	Stacking *definition.StackingConfig

	// Derived sets for O(1) lookups
	StepIDs   map[string]bool
	LoopNames map[string]bool
}

// DeliveryActive reports whether the workflow declares an active pull_request
// delivery policy: kind "pull_request" with an explicit mode other than
// "none". Runs with an active policy settle at delivery_pending on their
// success route instead of moving directly to succeeded.
func (c *CompiledWorkflow) DeliveryActive() bool {
	return c != nil && deliveryActive(c.Delivery)
}

// Compile validates a workflow definition and returns an immutable compiled
// workflow. It applies the full admission policy, including the
// unbounded-cycle check.
func Compile(wf *definition.WorkflowFile) (*CompiledWorkflow, error) {
	return compile(wf, false)
}

// CompileForResume compiles a definition that was already admitted in a run
// snapshot. It skips the unbounded-cycle admission check so an in-flight run
// admitted under an earlier policy can still resume. All other validators
// still run.
func CompileForResume(wf *definition.WorkflowFile) (*CompiledWorkflow, error) {
	return compile(wf, true)
}

func compile(wf *definition.WorkflowFile, skipCycleValidation bool) (*CompiledWorkflow, error) {
	errs := validateWorkflow(wf, skipCycleValidation)
	if len(errs) > 0 {
		return nil, fmt.Errorf("compilation failed:\n  - %s", strings.Join(errs, "\n  - "))
	}

	// Build step ID set
	stepIDs := make(map[string]bool, len(wf.Steps))
	for _, s := range wf.Steps {
		stepIDs[s.ID] = true
	}

	// Build loop name set
	loopNames := make(map[string]bool)
	for _, t := range wf.Transitions {
		if t.Loop != "" {
			loopNames[t.Loop] = true
		}
	}

	// Compute stable definition digest
	data, _ := json.Marshal(wf)
	hash := sha256.Sum256(data)
	digest := fmt.Sprintf("%x", hash)

	// Resolve stacking configuration. Explicit [stacking] values win;
	// otherwise inference is best-effort. Only populated when stacking is
	// enabled and both steps resolved, so existing single-PR workflows keep
	// their exact compiled shape.
	var stackingCfg *definition.StackingConfig
	if planStep, implementStep := ResolveStackingSteps(wf); (wf.Stacking == nil || wf.Stacking.StackingEnabled()) && planStep != "" && implementStep != "" {
		eff := wf.Stacking.EffectiveStacking(planStep, implementStep)
		stackingCfg = &eff
	}

	return &CompiledWorkflow{
		Name:        wf.Name,
		Description: wf.Description,
		Version:     wf.Version,
		InitialStep: wf.InitialStep,
		Inputs:      wf.Inputs,
		Limits:      wf.Limits,
		Steps:       wf.Steps,
		Transitions: wf.Transitions,
		Delivery:    wf.Delivery,
		Digest:      digest,
		Stacking:    stackingCfg,
		StepIDs:     stepIDs,
		LoopNames:   loopNames,
	}, nil
}

// validateWorkflow runs every semantic validator and returns the collected
// errors. skipCycleValidation is true only for resume of an admitted snapshot.
func validateWorkflow(wf *definition.WorkflowFile, skipCycleValidation bool) []string {
	var errs []string

	// Build step ID set
	stepIDs := make(map[string]bool, len(wf.Steps))
	for _, s := range wf.Steps {
		stepIDs[s.ID] = true
	}

	// Graph checks
	if err := validateGraph(wf, stepIDs); err != nil {
		errs = append(errs, err.Error())
	}

	// Transition checks
	if err := validateTransitions(wf, stepIDs, skipCycleValidation); err != nil {
		errs = append(errs, err.Error())
	}

	// Cycle checks (admission policy; resume of an admitted snapshot skips them)
	if !skipCycleValidation {
		if err := validateCycles(wf); err != nil {
			errs = append(errs, err.Error())
		}
	}

	// Context binding checks
	if err := validateContextBindings(wf, stepIDs, skipCycleValidation); err != nil {
		errs = append(errs, err.Error())
	}

	// Verifier name checks
	if err := validateVerifierNames(wf); err != nil {
		errs = append(errs, err.Error())
	}

	// On-failure target checks
	if err := validateOnFailure(wf, stepIDs); err != nil {
		errs = append(errs, err.Error())
	}

	// Delivery config validation
	if err := validateDelivery(wf); err != nil {
		errs = append(errs, err.Error())
	}

	// Delivery re-entry steps must bind delivery.failure so the repair agent
	// deterministically sees the rejection that routed it (admission-only,
	// like the evidence-cap check: an in-flight run admitted under an earlier
	// policy is never stranded).
	if !skipCycleValidation {
		if err := validateDeliveryReentryHints(wf); err != nil {
			errs = append(errs, err.Error())
		}
	}

	// Limits and stacking config validation
	if err := validateLimitsAndStacking(wf, stepIDs); err != nil {
		errs = append(errs, err.Error())
	}

	// Per-step max_turns validation
	if err := validateStepMaxTurns(wf); err != nil {
		errs = append(errs, err.Error())
	}

	// Agent panel validation
	if err := validatePanels(wf); err != nil {
		errs = append(errs, err.Error())
	}

	// Executable step kind checks
	if err := validateExecutableStepKinds(wf); err != nil {
		errs = append(errs, err.Error())
	}

	return errs
}

// validateExecutableStepKinds verifies controller support for special steps.
func validateExecutableStepKinds(wf *definition.WorkflowFile) error {
	_ = wf
	return nil
}

// validateOnFailure checks that all on_failure targets reference existing steps or terminals.
func validateOnFailure(wf *definition.WorkflowFile, stepIDs map[string]bool) error {
	for _, s := range wf.Steps {
		target := s.OnFailure
		if strings.TrimSpace(target) == "" {
			continue // defaults to "failure" at runtime
		}
		if !stepIDs[target] && !definition.ReservedStepIDs[target] {
			return fmt.Errorf("step %q: on_failure target %q is not a declared step or terminal", s.ID, target)
		}
	}
	return nil
}

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

// validateTransitions checks for overlapping transitions and loop constraints.
func validateTransitions(wf *definition.WorkflowFile, stepIDs map[string]bool, skipCycleValidation bool) error {
	// Group transitions by source step
	fromTransitions := make(map[string][]definition.Transition)
	for _, t := range wf.Transitions {
		fromTransitions[t.From] = append(fromTransitions[t.From], t)
	}

	for from, transitions := range fromTransitions {
		// Check for ambiguous overlapping match criteria. Two criteria from the
		// same step that one outcome can satisfy are fine when one is strictly
		// more specific (status-only fallback plus a status+output special
		// case): the matcher prefers the specific one. A jointly satisfiable
		// pair where neither is strictly more specific has no safe winner and
		// fails the run with multi_match at runtime.
		for i, ti := range transitions {
			for j := range transitions {
				if j <= i {
					continue
				}
				tj := transitions[j]
				if transitionCriteriaHazard(ti.Match, tj.Match) {
					return fmt.Errorf("step %q: transitions to %q and %q have ambiguous overlapping match criteria (add a distinguishing output field)", from, ti.To, tj.To)
				}
			}
		}
	}

	// Check loop constraints
	seenLoops := make(map[string]int)
	loopsBySource := make(map[string][]string)
	for _, t := range wf.Transitions {
		if t.Loop != "" {
			seenLoops[t.Loop]++
			loopsBySource[t.From] = append(loopsBySource[t.From], t.Loop)
		}
		if t.Loop == "" && t.MaxIterations != 0 {
			return fmt.Errorf("transition %s → %s: max_iterations requires a loop name", t.From, t.To)
		}
		if t.PartialTarget != "" && t.Loop == "" {
			return fmt.Errorf("transition %s → %s: partial_target requires a loop", t.From, t.To)
		}
		if t.PartialTarget != "" && !stepIDs[t.PartialTarget] {
			return fmt.Errorf("transition %s → %s: partial_target %q is not a declared step", t.From, t.To, t.PartialTarget)
		}
		if t.From == t.To && t.Loop == "" {
			// Self-loop without a loop name is a no-op transition.
			return fmt.Errorf("transition from %q to %q is a self-loop without a loop name", t.From, t.To)
		}
		if t.Loop != "" {
			if t.MaxIterations < 0 && t.MaxIterations != definition.UnlimitedIterations {
				return fmt.Errorf("loop %q: max_iterations must be > 0, or -1 for unlimited (got %d)", t.Loop, t.MaxIterations)
			}
			if t.MaxIterations == 0 {
				return fmt.Errorf("loop %q: max_iterations must be > 0, or -1 for unlimited (got 0); omitting the field does not default to unlimited", t.Loop)
			}
			if t.MaxIterations > 1000 {
				return fmt.Errorf("loop %q: max_iterations %d exceeds maximum of 1000", t.Loop, t.MaxIterations)
			}
		}
	}

	// Check for duplicate loop names
	for name, count := range seenLoops {
		if count > 1 {
			return fmt.Errorf("loop name %q is used by multiple transitions", name)
		}
	}
	if !skipCycleValidation {
		for source, names := range loopsBySource {
			if len(names) > 1 {
				return fmt.Errorf("step %q has multiple named loops (%s); a step may have at most one named loop transition", source, strings.Join(names, ", "))
			}
		}
	}

	return nil
}

// stepExists reports whether the workflow declares a step with this ID.
func stepExists(wf *definition.WorkflowFile, id string) bool {
	for _, s := range wf.Steps {
		if s.ID == id {
			return true
		}
	}
	return false
}

// validateLimits checks that the limits configuration is within acceptable bounds.
func validateLimits(limits definition.Limits) error {
	if limits.MaxStepAttempts < 0 || limits.MaxStepAttempts > 10000 {
		return fmt.Errorf("limits: max_step_attempts must be in range [0, 10000] (got %d)", limits.MaxStepAttempts)
	}
	if limits.MaxDurationSeconds < 0 || limits.MaxDurationSeconds > 86400 {
		return fmt.Errorf("limits: max_duration_seconds must be in range [0, 86400] (got %d)", limits.MaxDurationSeconds)
	}
	if limits.MaxOnFailureReentries < 0 || limits.MaxOnFailureReentries > 1000 {
		return fmt.Errorf("limits: max_on_failure_reentries must be in range [0, 1000] (got %d)", limits.MaxOnFailureReentries)
	}
	if limits.MaxTransientStepRetries < 0 || limits.MaxTransientStepRetries > 1000 {
		return fmt.Errorf("limits: max_transient_step_retries must be in range [0, 1000] (got %d)", limits.MaxTransientStepRetries)
	}
	return nil
}

// validateStepMaxTurns checks that each step's max_turns is within bounds.
// 0 means unlimited (the default); negative values and values above the
// maximum are config errors, mirroring the [limits] knobs.
func validateStepMaxTurns(wf *definition.WorkflowFile) error {
	for _, s := range wf.Steps {
		if s.MaxTurns < 0 || s.MaxTurns > 10000 {
			return fmt.Errorf("step %q: max_turns must be in range [0, 10000] (got %d)", s.ID, s.MaxTurns)
		}
	}
	return nil
}

// verifierNameRegex matches lowercase alphanumeric characters and hyphens,
// but the name must not start with a hyphen.
var verifierNameRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// validateVerifierNames checks that evidence_gate steps have a verifier name
// or a sandboxed command, and that either is well-formed.
func validateVerifierNames(wf *definition.WorkflowFile) error {
	for _, s := range wf.Steps {
		if s.Kind != "evidence_gate" {
			continue
		}
		if s.Verifier != "" {
			if !verifierNameRegex.MatchString(s.Verifier) {
				return fmt.Errorf("step %q: verifier name %q must be lowercase alphanumeric with hyphens", s.ID, s.Verifier)
			}
			continue
		}
		if s.Command == nil {
			return fmt.Errorf("step %q: evidence_gate requires a verifier or command", s.ID)
		}
		if !verifier.IsBareProgramName(s.Command.Program) {
			return fmt.Errorf("step %q: command program %q must be a bare executable name", s.ID, s.Command.Program)
		}
	}
	return nil
}

// matchCriteriaEqual checks if two match criteria are identical.
func matchCriteriaEqual(a, b definition.MatchCriteria) bool {
	if a.Status != b.Status {
		return false
	}
	if len(a.Output) != len(b.Output) {
		return false
	}
	for k, v := range a.Output {
		if bv, ok := b.Output[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

// transitionCriteriaOverlap reports whether two match criteria from the same
// source step are jointly satisfiable: one outcome can match both. Criteria
// overlap when their statuses match and no output key carries conflicting
// values (an empty output map satisfies any output).
func transitionCriteriaOverlap(a, b definition.MatchCriteria) bool {
	if a.Status != b.Status {
		return false
	}
	for key, av := range a.Output {
		if bv, ok := b.Output[key]; ok && bv != av {
			return false
		}
	}
	return true
}

// transitionCriteriaHazard reports whether two match criteria from the same
// source step are jointly satisfiable AND neither is strictly more specific
// than the other. The matcher prefers the strictly most specific match, so a
// strict-superset pair routes correctly; a non-comparable pair fails the run
// with multi_match and must be rejected at admission.
func transitionCriteriaHazard(a, b definition.MatchCriteria) bool {
	if !transitionCriteriaOverlap(a, b) {
		return false
	}
	return !strictOutputSuperset(a.Output, b.Output) && !strictOutputSuperset(b.Output, a.Output)
}

// strictOutputSuperset reports whether x contains every key of y with the
// same value and at least one additional key.
func strictOutputSuperset(x, y map[string]string) bool {
	if len(x) <= len(y) {
		return false
	}
	for key, yv := range y {
		if xv, ok := x[key]; !ok || xv != yv {
			return false
		}
	}
	return true
}
