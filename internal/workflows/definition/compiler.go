package definition

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// CompiledWorkflow is the immutable result of successful compilation.
type CompiledWorkflow struct {
	Name        string
	Description string
	Version     int
	InitialStep string
	Inputs      map[string]InputDef
	Limits      Limits
	Steps       []Step
	Transitions []Transition
	Delivery    *Delivery
	Digest      string

	// Stacking is the resolved stacking configuration when the workflow
	// declares an enabled [stacking] table with explicit plan_step and
	// implement_step keys. Nil otherwise; the run then behaves exactly as a
	// single-PR workflow always did.
	Stacking *StackingConfig

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
func Compile(wf *WorkflowFile) (*CompiledWorkflow, error) {
	return compile(wf, false)
}

// CompileForResume compiles a definition that was already admitted in a run
// snapshot. It skips the unbounded-cycle admission check so an in-flight run
// admitted under an earlier policy can still resume. All other validators
// still run, and stacking resolves under the same opt-in rule as admission.
func CompileForResume(wf *WorkflowFile) (*CompiledWorkflow, error) {
	return compile(wf, true)
}

func compile(wf *WorkflowFile, skipCycleValidation bool) (*CompiledWorkflow, error) {
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

	// Resolve stacking configuration. Stacking is opt-in: only a workflow
	// that declares a [stacking] table (and does not set enabled = false)
	// participates, and the plan/implement steps come only from its explicit
	// plan_step/implement_step keys. There is no step inference. A workflow
	// without the table keeps the exact compiled shape of a single-PR run.
	var stackingCfg *StackingConfig
	if s := wf.Stacking; s != nil && s.StackingEnabled() && s.PlanStep != "" && s.ImplementStep != "" {
		eff := s.EffectiveStacking(s.PlanStep, s.ImplementStep)
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
func validateWorkflow(wf *WorkflowFile, skipCycleValidation bool) []string {
	var errs []string

	// Build step ID set
	stepIDs := make(map[string]bool, len(wf.Steps))
	for _, s := range wf.Steps {
		stepIDs[s.ID] = true
	}

	// Graph checks
	errs = append(errs, validateGraph(wf, stepIDs)...)

	// Transition checks
	errs = append(errs, validateTransitions(wf, stepIDs, skipCycleValidation)...)

	// Cycle checks (admission policy; resume of an admitted snapshot skips them)
	if !skipCycleValidation {
		errs = append(errs, validateCycles(wf)...)
	}

	// Context binding checks
	errs = append(errs, validateContextBindings(wf, stepIDs, skipCycleValidation)...)

	// Verifier name checks
	errs = append(errs, validateVerifierNames(wf)...)

	// On-failure target checks
	errs = append(errs, validateOnFailure(wf, stepIDs)...)

	// Delivery config validation
	errs = append(errs, validateDeliverySection(wf, skipCycleValidation)...)

	// Limits and stacking config validation
	errs = append(errs, validateLimitsAndStacking(wf, stepIDs)...)

	// Per-step max_turns validation
	errs = append(errs, validateStepMaxTurns(wf)...)

	// Agent panel validation
	errs = append(errs, validatePanels(wf)...)

	// Executable step kind checks
	errs = append(errs, validateExecutableStepKinds(wf)...)

	return errs
}

// validateDeliverySection runs the [delivery] validators: the always-on
// config check plus the admission-only provider-support and re-entry-hint
// checks. An in-flight run admitted under an earlier policy is never
// stranded by the admission-only checks, so resume skips them.
func validateDeliverySection(wf *WorkflowFile, skipCycleValidation bool) []string {
	var errs []string
	errs = append(errs, validateDelivery(wf)...)
	if skipCycleValidation {
		return errs
	}
	errs = append(errs, validateDeliveryProviderSupport(wf)...)
	// Delivery re-entry steps must bind delivery.failure so the repair agent
	// deterministically sees the rejection that routed it.
	errs = append(errs, validateDeliveryReentryHints(wf)...)
	return errs
}

// validateExecutableStepKinds verifies controller support for special steps.
func validateExecutableStepKinds(wf *WorkflowFile) []string {
	_ = wf
	return nil
}

// validateOnFailure checks that all on_failure targets reference existing steps or terminals.
func validateOnFailure(wf *WorkflowFile, stepIDs map[string]bool) []string {
	var errs []string
	for _, s := range wf.Steps {
		target := s.OnFailure
		if strings.TrimSpace(target) == "" {
			continue // defaults to "failure" at runtime
		}
		if !stepIDs[target] && !ReservedStepIDs[target] {
			errs = append(errs, fmt.Sprintf("step %q: on_failure target %q is not a declared step or terminal", s.ID, target))
		}
	}
	return errs
}

// validateTransitions checks for overlapping transitions and loop constraints.
func validateTransitions(wf *WorkflowFile, stepIDs map[string]bool, skipCycleValidation bool) []string {
	// Group transitions by source step
	fromTransitions := make(map[string][]Transition)
	for _, t := range wf.Transitions {
		fromTransitions[t.From] = append(fromTransitions[t.From], t)
	}

	var errs []string

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
					errs = append(errs, fmt.Sprintf("step %q: transitions to %q and %q have ambiguous overlapping match criteria (add a distinguishing output field)", from, ti.To, tj.To))
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
			errs = append(errs, fmt.Sprintf("transition %s → %s: max_iterations requires a loop name", t.From, t.To))
		}
		if t.PartialTarget != "" && t.Loop == "" {
			errs = append(errs, fmt.Sprintf("transition %s → %s: partial_target requires a loop", t.From, t.To))
		}
		if t.PartialTarget != "" && !stepIDs[t.PartialTarget] {
			errs = append(errs, fmt.Sprintf("transition %s → %s: partial_target %q is not a declared step", t.From, t.To, t.PartialTarget))
		}
		if t.From == t.To && t.Loop == "" {
			// Self-loop without a loop name is a no-op transition.
			errs = append(errs, fmt.Sprintf("transition from %q to %q is a self-loop without a loop name", t.From, t.To))
		}
		if t.Loop != "" {
			if t.MaxIterations < 0 && t.MaxIterations != UnlimitedIterations {
				errs = append(errs, fmt.Sprintf("loop %q: max_iterations must be > 0, or -1 for unlimited (got %d)", t.Loop, t.MaxIterations))
			}
			if t.MaxIterations == 0 {
				errs = append(errs, fmt.Sprintf("loop %q: max_iterations must be > 0, or -1 for unlimited (got 0); omitting the field does not default to unlimited", t.Loop))
			}
			if t.MaxIterations > 1000 {
				errs = append(errs, fmt.Sprintf("loop %q: max_iterations %d exceeds maximum of 1000", t.Loop, t.MaxIterations))
			}
		}
	}

	// Check for duplicate loop names
	for name, count := range seenLoops {
		if count > 1 {
			errs = append(errs, fmt.Sprintf("loop name %q is used by multiple transitions", name))
		}
	}
	if !skipCycleValidation {
		for source, names := range loopsBySource {
			if len(names) > 1 {
				errs = append(errs, fmt.Sprintf("step %q has multiple named loops (%s); a step may have at most one named loop transition", source, strings.Join(names, ", ")))
			}
		}
	}

	return errs
}

// stepExists reports whether the workflow declares a step with this ID.
func stepExists(wf *WorkflowFile, id string) bool {
	for _, s := range wf.Steps {
		if s.ID == id {
			return true
		}
	}
	return false
}

// validateLimits checks that the limits configuration is within acceptable bounds.
func validateLimits(limits Limits) []string {
	var errs []string
	if limits.MaxStepAttempts < 0 || limits.MaxStepAttempts > 10000 {
		errs = append(errs, fmt.Sprintf("limits: max_step_attempts must be in range [0, 10000] (got %d)", limits.MaxStepAttempts))
	}
	if limits.MaxDurationSeconds < 0 || limits.MaxDurationSeconds > 86400 {
		errs = append(errs, fmt.Sprintf("limits: max_duration_seconds must be in range [0, 86400] (got %d)", limits.MaxDurationSeconds))
	}
	if limits.MaxOnFailureReentries < 0 || limits.MaxOnFailureReentries > 1000 {
		errs = append(errs, fmt.Sprintf("limits: max_on_failure_reentries must be in range [0, 1000] (got %d)", limits.MaxOnFailureReentries))
	}
	if limits.MaxTransientStepRetries < 0 || limits.MaxTransientStepRetries > 1000 {
		errs = append(errs, fmt.Sprintf("limits: max_transient_step_retries must be in range [0, 1000] (got %d)", limits.MaxTransientStepRetries))
	}
	return errs
}

// validateStepMaxTurns checks that each step's max_turns is within bounds.
// 0 means unlimited (the default); negative values and values above the
// maximum are config errors, mirroring the [limits] knobs.
func validateStepMaxTurns(wf *WorkflowFile) []string {
	var errs []string
	for _, s := range wf.Steps {
		if s.MaxTurns < 0 || s.MaxTurns > 10000 {
			errs = append(errs, fmt.Sprintf("step %q: max_turns must be in range [0, 10000] (got %d)", s.ID, s.MaxTurns))
		}
	}
	return errs
}

// verifierNameRegex matches lowercase alphanumeric characters and hyphens,
// but the name must not start with a hyphen.
var verifierNameRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// validateVerifierNames checks that evidence_gate steps have a verifier name
// or a sandboxed command, and that either is well-formed.
func validateVerifierNames(wf *WorkflowFile) []string {
	var errs []string
	for _, s := range wf.Steps {
		if s.Kind != "evidence_gate" {
			continue
		}
		if s.Verifier != "" {
			if !verifierNameRegex.MatchString(s.Verifier) {
				errs = append(errs, fmt.Sprintf("step %q: verifier name %q must be lowercase alphanumeric with hyphens", s.ID, s.Verifier))
			}
			continue
		}
		if s.Command == nil {
			errs = append(errs, fmt.Sprintf("step %q: evidence_gate requires a verifier or command", s.ID))
		} else if !IsBareProgramName(s.Command.Program) {
			errs = append(errs, fmt.Sprintf("step %q: command program %q must be a bare executable name", s.ID, s.Command.Program))
		}
	}
	return errs
}

// transitionCriteriaOverlap reports whether two match criteria from the same
// source step are jointly satisfiable: one outcome can match both. Criteria
// overlap when their statuses match and no output key carries conflicting
// values (an empty output map satisfies any output).
func transitionCriteriaOverlap(a, b MatchCriteria) bool {
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
func transitionCriteriaHazard(a, b MatchCriteria) bool {
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
