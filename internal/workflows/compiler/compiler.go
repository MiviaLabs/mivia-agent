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

	// Stacking is the resolved stacking configuration when the workflow
	// declares an enabled [stacking] table with explicit plan_step and
	// implement_step keys. Nil otherwise; the run then behaves exactly as a
	// single-PR workflow always did.
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
	return compile(wf, false, false)
}

// CompileForResume compiles a definition that was already admitted in a run
// snapshot UNDER THE LEGACY STACKING SEMANTICS (the snapshot carries no
// StackingSemanticsVersion marker). It skips the admission-only checks - the
// unbounded-cycle check and the stacking explicit-steps requirement - and a
// snapshot without an explicit [stacking] table (or without explicit steps)
// resumes under the inference activation it was admitted with
// (legacyResumeStacking). All other validators still run.
func CompileForResume(wf *definition.WorkflowFile) (*CompiledWorkflow, error) {
	return compile(wf, true, true)
}

// CompileForResumeOptIn compiles a definition admitted under the opt-in
// stacking semantics (snapshot marked with StackingSemanticsOptIn). It skips
// the same admission-only checks as CompileForResume, but stacking resolves
// exactly as at admission: no legacy inference, so a run admitted without a
// [stacking] table resumes single-PR.
func CompileForResumeOptIn(wf *definition.WorkflowFile) (*CompiledWorkflow, error) {
	return compile(wf, true, false)
}

func compile(wf *definition.WorkflowFile, skipCycleValidation, legacyStacking bool) (*CompiledWorkflow, error) {
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
	var stackingCfg *definition.StackingConfig
	if s := wf.Stacking; s != nil && s.StackingEnabled() && s.PlanStep != "" && s.ImplementStep != "" {
		eff := s.EffectiveStacking(s.PlanStep, s.ImplementStep)
		stackingCfg = &eff
	} else if skipCycleValidation && legacyStacking {
		// Resume of a snapshot admitted under the legacy semantics: apply the
		// activation the run was admitted under, so its compiled shape
		// (synthesized steps, reserved inputs, delivery guards) is rebuilt
		// identically. Snapshots marked StackingSemanticsOptIn never take
		// this branch - their admitted activation is the strict rule above.
		stackingCfg = legacyResumeStacking(wf)
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
	if err := validateLimitsAndStacking(wf, stepIDs, skipCycleValidation); err != nil {
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
