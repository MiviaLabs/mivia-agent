package compiler

import (
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

// Engine-reserved identifiers for stacking synthesis. A workflow must not
// declare a step or loop with these names; synthesis fails closed instead of
// emitting a duplicate.
const (
	stepDecompose          = "decompose"
	stepChunkPlanValidate  = "chunk_plan_validate"
	loopDecomposeRepair    = "decompose_repair"
	templateDecompose      = "templates/decompose.md"
	templateChunkPlanValid = "templates/chunk-plan-validate.md"
	schemaChunkPlan        = "schemas/chunk-plan-v1.json"
	schemaChunkPlanReview  = "schemas/chunk-plan-review-v1.json"
)

// stackingReservedInputs returns the engine-reserved input definitions a
// stacking run graph adds to the workflow's input contract (decision D3).
// None are required: a plan-mode run starts without them, and a chunk-mode
// run carries them in the admission payload.
func stackingReservedInputs() map[string]definition.InputDef {
	return map[string]definition.InputDef{
		"stack_mode": {Type: "string"},
		"chunk":      {Type: "string"},
		"pr_base":    {Type: "string"},
		"stack_part": {Type: "string"},
		"chunk_plan": {Type: "string"},
	}
}

// SynthesizedInputs returns the reserved stacking inputs admission adds for a
// stacking run. A missing or disabled config contributes no inputs.
func SynthesizedInputs(cfg *definition.StackingConfig) map[string]definition.InputDef {
	if cfg == nil || !cfg.Enabled {
		return nil
	}
	return stackingReservedInputs()
}

// SynthesizeStacking returns the run graph for a compiled stacking workflow:
// the original graph plus the engine-injected decompose and
// chunk_plan_validate steps, the reserved inputs, and the router edges from
// the plan step. It never mutates the input; it always returns a copy.
//
// When cw.Stacking is nil, the workflow is not stacked and the input is
// returned unchanged (the same pointer). The digest is copied unchanged:
// synthesis is a post-compile admission step and never moves the definition
// digest.
func SynthesizeStacking(cw *CompiledWorkflow) (*CompiledWorkflow, error) {
	if cw == nil || cw.Stacking == nil {
		return cw, nil
	}
	cfg := *cw.Stacking

	if err := checkSynthesisIdentifiers(cw); err != nil {
		return nil, err
	}

	agent := cfg.Agent
	if agent == "" {
		agent = planStepAgent(cw, cfg.PlanStep)
	}
	if agent == "" {
		return nil, fmt.Errorf("stacking synthesis: plan step %q has no agent and stacking.agent is empty", cfg.PlanStep)
	}

	inputs := make(map[string]definition.InputDef, len(cw.Inputs)+len(stackingReservedInputs()))
	for name, def := range cw.Inputs {
		inputs[name] = def
	}
	for name, def := range stackingReservedInputs() {
		inputs[name] = def
	}

	steps := make([]definition.Step, 0, len(cw.Steps)+2)
	steps = append(steps, cw.Steps...)
	steps = append(steps,
		definition.Step{
			ID:           stepDecompose,
			Kind:         "agent",
			Agent:        agent,
			Template:     templateDecompose,
			OutputSchema: schemaChunkPlan,
		},
		definition.Step{
			ID:           stepChunkPlanValidate,
			Kind:         "agent_gate",
			Agent:        agent,
			Template:     templateChunkPlanValid,
			OutputSchema: schemaChunkPlanReview,
		},
	)

	transitions := synthesizeTransitions(cw, cfg.PlanStep, cfg.ImplementStep)

	stepIDs := copyIDSet(cw.StepIDs)
	stepIDs[stepDecompose] = true
	stepIDs[stepChunkPlanValidate] = true

	loopNames := copyIDSet(cw.LoopNames)
	loopNames[loopDecomposeRepair] = true

	synth := &CompiledWorkflow{
		Name:        cw.Name,
		Description: cw.Description,
		Version:     cw.Version,
		InitialStep: cw.InitialStep,
		Inputs:      inputs,
		Limits:      cw.Limits,
		Steps:       steps,
		Transitions: transitions,
		Delivery:    cw.Delivery,
		Digest:      cw.Digest,
		Stacking:    &cfg,
		StepIDs:     stepIDs,
		LoopNames:   loopNames,
	}

	if err := validateSynthesizedGraph(synth); err != nil {
		return nil, err
	}
	return synth, nil
}

// checkSynthesisIdentifiers fails closed when the workflow already declares an
// engine-reserved step id or loop name.
func checkSynthesisIdentifiers(cw *CompiledWorkflow) error {
	if cw.StepIDs[stepDecompose] || cw.StepIDs[stepChunkPlanValidate] {
		return fmt.Errorf("stacking synthesis: step ids %q and %q are engine-reserved", stepDecompose, stepChunkPlanValidate)
	}
	if cw.LoopNames[loopDecomposeRepair] {
		return fmt.Errorf("stacking synthesis: loop name %q is engine-reserved", loopDecomposeRepair)
	}
	return nil
}

// planStepAgent returns the agent of the workflow's plan step.
func planStepAgent(cw *CompiledWorkflow, planStep string) string {
	for _, s := range cw.Steps {
		if s.ID == planStep {
			return s.Agent
		}
	}
	return ""
}

// copyIDSet copies a set of ids into a fresh map.
func copyIDSet(src map[string]bool) map[string]bool {
	dst := make(map[string]bool, len(src))
	for id := range src {
		dst[id] = true
	}
	return dst
}

// synthesizeTransitions rewires the plan step's outgoing edges and appends
// the stacking router. Every declared plan edge with an empty or succeeded
// status is superseded by the router and removed; edges with other explicit
// statuses (for example failed) survive. The router routes decompose by its
// stack_mode output and bounds the chunk-plan repair loop.
func synthesizeTransitions(cw *CompiledWorkflow, planStep, implementStep string) []definition.Transition {
	out := make([]definition.Transition, 0, len(cw.Transitions)+7)
	for _, tr := range cw.Transitions {
		if tr.From == planStep && (tr.Match.Status == "" || tr.Match.Status == "succeeded") {
			continue
		}
		out = append(out, tr)
	}
	out = append(out,
		definition.Transition{From: planStep, To: stepDecompose, Match: definition.MatchCriteria{Status: "succeeded"}},
		definition.Transition{From: stepDecompose, To: implementStep, Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"stack_mode": "single"}}},
		definition.Transition{From: stepDecompose, To: "success", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"stack_mode": "no_bug"}}},
		definition.Transition{From: stepDecompose, To: stepChunkPlanValidate, Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"stack_mode": "multi"}}},
		definition.Transition{From: stepDecompose, To: "failure", Match: definition.MatchCriteria{Status: "failed"}},
		definition.Transition{From: stepChunkPlanValidate, To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		definition.Transition{From: stepChunkPlanValidate, To: stepDecompose, Match: definition.MatchCriteria{Status: "failed"}, Loop: loopDecomposeRepair, MaxIterations: 3},
	)
	return out
}

// validateSynthesizedGraph runs the compiler's own admission validators on
// the synthesized graph and joins every failure into one error.
func validateSynthesizedGraph(cw *CompiledWorkflow) error {
	wf := &definition.WorkflowFile{
		Version:     cw.Version,
		Name:        cw.Name,
		Description: cw.Description,
		InitialStep: cw.InitialStep,
		Inputs:      cw.Inputs,
		Limits:      cw.Limits,
		Steps:       cw.Steps,
		Transitions: cw.Transitions,
		Delivery:    cw.Delivery,
	}
	stepIDs := copyIDSet(cw.StepIDs)

	var errs []string
	validators := []struct {
		name string
		run  func() error
	}{
		{"validateGraph", func() error { return validateGraph(wf, stepIDs) }},
		{"validateTransitions", func() error { return validateTransitions(wf, stepIDs, false) }},
		{"validateCycles", func() error { return validateCycles(wf) }},
		{"validateContextBindings", func() error { return validateContextBindings(wf, stepIDs, false) }},
		{"validateOnFailure", func() error { return validateOnFailure(wf, stepIDs) }},
		{"validateLimitsAndStacking", func() error { return validateLimitsAndStacking(wf, stepIDs) }},
		{"validateStepMaxTurns", func() error { return validateStepMaxTurns(wf) }},
		{"validatePanels", func() error { return validatePanels(wf) }},
	}
	for _, v := range validators {
		if err := v.run(); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", v.name, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("stacking synthesis validation failed:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}
