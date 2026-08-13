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

// MergeStackingInputs merges the engine-reserved stacking input definitions
// into a compiled stacking workflow's input contract. Names the workflow
// already declares are left untouched; a non-stacking workflow (nil Stacking)
// and a nil compiled workflow are no-ops. The merge is additive and
// post-compile - it never moves the compile-time digest - and mirrors the
// reserved set SynthesizeStacking adds to the run graph, so admission and
// resume validate against the same input contract.
func MergeStackingInputs(compiled *CompiledWorkflow) {
	if compiled == nil {
		return
	}
	reserved := SynthesizedInputs(compiled.Stacking)
	if len(reserved) == 0 {
		return
	}
	if compiled.Inputs == nil {
		compiled.Inputs = make(map[string]definition.InputDef, len(reserved))
	}
	for name, def := range reserved {
		if _, ok := compiled.Inputs[name]; !ok {
			compiled.Inputs[name] = def
		}
	}
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
//
// Synthesis is idempotent: a graph that already carries every engine-reserved
// stacking artifact (both synthesized steps AND the repair loop) is the run
// graph itself and is returned unchanged. The runtime build synthesizes once
// before building step runtimes, and the controller re-synthesizes on direct
// construction, so both sides must agree on what "already synthesized" means.
// A workflow that declares only SOME of the reserved identifiers still fails
// the reserved-identifier check below, exactly as before.
func SynthesizeStacking(cw *CompiledWorkflow) (*CompiledWorkflow, error) {
	if cw == nil || cw.Stacking == nil {
		return cw, nil
	}
	if cw.StepIDs[stepDecompose] && cw.StepIDs[stepChunkPlanValidate] && cw.LoopNames[loopDecomposeRepair] {
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

	steps := synthesizedStackingSteps(cw.Steps, agent, cfg.PlanStep)

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

// synthesizedStackingSteps appends the engine-synthesized decompose and
// chunk_plan_validate steps to the workflow's declared steps. The synthesized
// steps carry the context bindings their templates require - decompose reads
// the plan artifact as "plan", the gate reads the decompose output as
// "chunk_plan" - so a real agent run (not just the scripted harness) sees the
// artifacts it must judge.
func synthesizedStackingSteps(declared []definition.Step, agent, planStep string) []definition.Step {
	steps := make([]definition.Step, 0, len(declared)+2)
	steps = append(steps, declared...)
	steps = append(steps,
		definition.Step{
			ID:           stepDecompose,
			Kind:         "agent",
			Agent:        agent,
			Template:     templateDecompose,
			OutputSchema: schemaChunkPlan,
			Context: []definition.ContextBinding{
				{From: "steps." + planStep + ".output", As: "plan"},
			},
		},
		definition.Step{
			ID:           stepChunkPlanValidate,
			Kind:         "agent_gate",
			Agent:        agent,
			Template:     templateChunkPlanValid,
			OutputSchema: schemaChunkPlanReview,
			Context: []definition.ContextBinding{
				{From: "steps." + stepDecompose + ".output", As: "chunk_plan"},
			},
		},
	)
	return steps
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

// synthesizeTransitions rewires the plan phase's exit edge and appends the
// stacking router. The router anchors at the last plan-phase step (see
// stackingRouterAnchor): the anchor's succeeded exit into the implement step
// is superseded by the router and removed. Every other declared edge - repair
// loops, failed edges, and plan-phase edges before the anchor - survives, so a
// multi-step plan phase (plan -> plan_review -> plan_tests -> ... ->
// implement) keeps every step reachable and the implement step's plan-phase
// context bindings resolvable in single mode. The router routes decompose by
// its stack_mode output and bounds the chunk-plan repair loop.
func synthesizeTransitions(cw *CompiledWorkflow, planStep, implementStep string) []definition.Transition {
	anchor := stackingRouterAnchor(cw, planStep, implementStep)
	out := make([]definition.Transition, 0, len(cw.Transitions)+8)
	// Only the anchor's succeeded exit into the implement step is superseded
	// by the router. When the plan phase has a distinct anchor, a direct
	// plan-step exit into the implement step is rewired through the anchor
	// instead of dropped: dropping both would leave the plan step without a
	// succeeded exit and its happy path would hard-fail ('no matching
	// transition') at runtime while the plan-failed path still worked. Every
	// plan-phase step keeps at least one succeeded exit.
	rewirePlanToAnchor := false
	for _, tr := range cw.Transitions {
		if tr.To != implementStep || tr.From == implementStep ||
			(tr.Match.Status != "" && tr.Match.Status != "succeeded") {
			out = append(out, tr)
			continue
		}
		switch tr.From {
		case anchor:
			continue // superseded by the anchor -> decompose router edge
		case planStep:
			if len(tr.Match.Output) > 0 {
				// Output-discriminated shortcut (e.g. skip_review=true): not
				// superseded by the anchor rewire, which only replaces the
				// plain generic succeeded edge. Preserve it unchanged so the
				// discriminated route still reaches implementStep directly.
				out = append(out, tr)
				continue
			}
			if planStep == anchor {
				continue // same step; superseded by the anchor -> decompose edge
			}
			rewirePlanToAnchor = true
		default:
			out = append(out, tr)
		}
	}
	if rewirePlanToAnchor && !hasPlainSucceededEdge(cw, planStep, anchor) {
		out = append(out, definition.Transition{From: planStep, To: anchor, Match: definition.MatchCriteria{Status: "succeeded"}})
	}
	out = append(out,
		definition.Transition{From: anchor, To: stepDecompose, Match: definition.MatchCriteria{Status: "succeeded"}},
		definition.Transition{From: stepDecompose, To: implementStep, Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"stack_mode": "single"}}},
		definition.Transition{From: stepDecompose, To: "success", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"stack_mode": "no_bug"}}},
		definition.Transition{From: stepDecompose, To: stepChunkPlanValidate, Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"stack_mode": "multi"}}},
		definition.Transition{From: stepDecompose, To: "failure", Match: definition.MatchCriteria{Status: "failed"}},
		definition.Transition{From: stepChunkPlanValidate, To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		definition.Transition{From: stepChunkPlanValidate, To: stepDecompose, Match: definition.MatchCriteria{Status: "failed"}, Loop: loopDecomposeRepair, MaxIterations: 3},
	)
	return out
}

// hasPlainSucceededEdge reports whether the workflow already declares a plain
// succeeded edge (no output discriminator) from from to to. Synthesis uses it
// to avoid rewiring a second identical edge: admission would reject two
// jointly satisfiable, equally specific criteria as ambiguous, so the rewire
// is skipped when the plan step already has the route.
func hasPlainSucceededEdge(cw *CompiledWorkflow, from, to string) bool {
	for _, tr := range cw.Transitions {
		if tr.From == from && tr.To == to && tr.Match.Status == "succeeded" && len(tr.Match.Output) == 0 {
			return true
		}
	}
	return false
}

// stackingRouterAnchor returns the step the stacking router rewires: the last
// step of the workflow's plan phase, i.e. the step whose succeeded edge exits
// the plan phase into the implement step. A workflow with a multi-step plan
// phase (plan -> plan_review -> ... -> implement) keeps every plan-phase step
// reachable and the implement step's plan-phase context bindings resolvable in
// single mode. Falls back to the plan step when no distinct anchor exists,
// preserving the original behavior for one-step plan phases (plan ->
// implement).
func stackingRouterAnchor(cw *CompiledWorkflow, planStep, implementStep string) string {
	if implementStep == "" {
		return planStep
	}
	// Candidates: steps whose edge exits into the implement step with an
	// empty or succeeded status.
	candidates := map[string]bool{}
	for _, tr := range cw.Transitions {
		if tr.From == implementStep || tr.To != implementStep {
			continue
		}
		if tr.Match.Status != "" && tr.Match.Status != "succeeded" {
			continue
		}
		candidates[tr.From] = true
	}
	if len(candidates) == 0 {
		return planStep
	}
	// Exclude repair-loop exits into the implement step (for example
	// review_integration -> implement on changes_requested): those steps are
	// downstream of the implement step, not part of the plan phase.
	fromImplement := reachableSteps(cw, implementStep)
	for s := range candidates {
		if fromImplement[s] {
			delete(candidates, s)
		}
	}
	switch len(candidates) {
	case 0:
		return planStep
	case 1:
		for s := range candidates {
			return s
		}
	}
	// Multiple plan-phase exits (rare): keep the longest plan phase by
	// anchoring at the candidate farthest from the plan step.
	dist := distancesFrom(cw, planStep)
	best, bestDist := "", -1
	for s := range candidates {
		if dist[s] > bestDist {
			best, bestDist = s, dist[s]
		}
	}
	if best == "" {
		return planStep
	}
	return best
}

// reachableSteps returns the set of step ids reachable from start over the
// transition graph, including start itself.
func reachableSteps(cw *CompiledWorkflow, start string) map[string]bool {
	adj := transitionAdjacency(cw)
	seen := map[string]bool{start: true}
	queue := []string{start}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, next := range adj[cur] {
			if !seen[next] {
				seen[next] = true
				queue = append(queue, next)
			}
		}
	}
	return seen
}

// distancesFrom returns the shortest-path distance from start to every
// reachable step over the transition graph.
func distancesFrom(cw *CompiledWorkflow, start string) map[string]int {
	adj := transitionAdjacency(cw)
	dist := map[string]int{start: 0}
	queue := []string{start}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, next := range adj[cur] {
			if _, ok := dist[next]; !ok {
				dist[next] = dist[cur] + 1
				queue = append(queue, next)
			}
		}
	}
	return dist
}

// transitionAdjacency maps every step id to the step ids it can reach via a
// declared transition, including the terminal targets "success" and
// "failure".
func transitionAdjacency(cw *CompiledWorkflow) map[string][]string {
	adj := make(map[string][]string, len(cw.StepIDs))
	for _, tr := range cw.Transitions {
		adj[tr.From] = append(adj[tr.From], tr.To)
	}
	return adj
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
