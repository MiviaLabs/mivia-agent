package compiler

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
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

	// Derived sets for O(1) lookups
	StepIDs   map[string]bool
	LoopNames map[string]bool
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
	if err := validateTransitions(wf, stepIDs); err != nil {
		errs = append(errs, err.Error())
	}

	// Cycle checks (admission policy; resume of an admitted snapshot skips them)
	if !skipCycleValidation {
		if err := validateCycles(wf); err != nil {
			errs = append(errs, err.Error())
		}
	}

	// Context binding checks
	if err := validateContextBindings(wf, stepIDs); err != nil {
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

	// Limits validation
	if err := validateLimits(wf.Limits); err != nil {
		errs = append(errs, err.Error())
	}

	return errs
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
func validateContextBindings(wf *definition.WorkflowFile, stepIDs map[string]bool) error {
	for _, s := range wf.Steps {
		for _, cb := range s.Context {
			if strings.TrimSpace(cb.From) == "" {
				return fmt.Errorf("step %q: context from is empty", s.ID)
			}
			// Reject path traversal
			if strings.Contains(cb.From, "..") {
				return fmt.Errorf("step %q: context from %q contains path traversal", s.ID, cb.From)
			}
			// Validate source format: inputs.<name> or steps.<id>.output
			parts := strings.Split(cb.From, ".")
			switch parts[0] {
			case "inputs":
				if len(parts) != 2 {
					return fmt.Errorf("step %q: context from %q invalid (expected inputs.<name>)", s.ID, cb.From)
				}
				inputName := parts[1]
				if _, ok := wf.Inputs[inputName]; !ok {
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
func validateTransitions(wf *definition.WorkflowFile, stepIDs map[string]bool) error {
	// Group transitions by source step
	fromTransitions := make(map[string][]definition.Transition)
	for _, t := range wf.Transitions {
		fromTransitions[t.From] = append(fromTransitions[t.From], t)
	}

	for from, transitions := range fromTransitions {
		// Check for overlapping match criteria
		for i, ti := range transitions {
			for j := range transitions {
				if j <= i {
					continue
				}
				tj := transitions[j]
				if matchCriteriaEqual(ti.Match, tj.Match) {
					return fmt.Errorf("step %q: transitions to %q and %q have overlapping match criteria", from, ti.To, tj.To)
				}
			}
		}
	}

	// Check loop constraints
	seenLoops := make(map[string]int)
	for _, t := range wf.Transitions {
		if t.Loop != "" {
			seenLoops[t.Loop]++
		}
		if t.Loop == "" && t.MaxIterations != 0 {
			return fmt.Errorf("transition %s → %s: max_iterations requires a loop name", t.From, t.To)
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
			if t.MaxIterations > 100 {
				return fmt.Errorf("loop %q: max_iterations %d exceeds maximum of 100", t.Loop, t.MaxIterations)
			}
		}
	}

	// Check for duplicate loop names
	for name, count := range seenLoops {
		if count > 1 {
			return fmt.Errorf("loop name %q is used by multiple transitions", name)
		}
	}

	return nil
}

// validateDelivery checks that the delivery configuration is structurally valid.
func validateDelivery(wf *definition.WorkflowFile) error {
	if wf.Delivery == nil {
		return nil
	}
	switch wf.Delivery.Kind {
	case "":
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

// validateLimits checks that the limits configuration is within acceptable bounds.
func validateLimits(limits definition.Limits) error {
	if limits.MaxStepAttempts < 0 || limits.MaxStepAttempts > 100 {
		return fmt.Errorf("limits: max_step_attempts must be in range [0, 100] (got %d)", limits.MaxStepAttempts)
	}
	if limits.MaxDurationSeconds < 0 || limits.MaxDurationSeconds > 86400 {
		return fmt.Errorf("limits: max_duration_seconds must be in range [0, 86400] (got %d)", limits.MaxDurationSeconds)
	}
	return nil
}

// verifierNameRegex matches lowercase alphanumeric characters and hyphens,
// but the name must not start with a hyphen.
var verifierNameRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// validateVerifierNames checks that evidence_gate steps have a non-empty verifier
// and that the verifier name matches the allowed format.
func validateVerifierNames(wf *definition.WorkflowFile) error {
	for _, s := range wf.Steps {
		if s.Kind != "evidence_gate" {
			continue
		}
		if s.Verifier == "" {
			return fmt.Errorf("step %q: evidence_gate requires a verifier", s.ID)
		}
		if !verifierNameRegex.MatchString(s.Verifier) {
			return fmt.Errorf("step %q: verifier name %q must be lowercase alphanumeric with hyphens", s.ID, s.Verifier)
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
