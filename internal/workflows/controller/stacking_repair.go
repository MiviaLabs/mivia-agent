package controller

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/matcher"
)

// stackingRepairLoopMax reads the repair loop's max_iterations from the
// synthesized graph so the controller stays in sync with the compiler.
func stackingRepairLoopMax(wf *compiler.CompiledWorkflow) int {
	if wf == nil {
		return 0
	}
	for _, tr := range wf.Transitions {
		if tr.From == synthesizedChunkPlanValidateStepID && tr.Loop == stackingRepairLoopName {
			return tr.MaxIterations
		}
	}
	return 0
}

// chunkPlanRepairRoute is the deterministic decompose gate. When a stacked run
// routes a succeeded decompose step, the controller validates the verdict
// before honoring it:
//   - a no_bug verdict (decompose -> success) is valid only when the plan
//     declares no actionable steps. A no_bug verdict on a plan with steps is
//     a contradiction and is rerouted to decompose for repair, so a
//     reviewed, actionable plan can never settle as a zero-diff success
//     without being implemented (the observed zero-diff delivery bug:
//     run wfr-inv-b252179884a57b2b9411fb34d30371fa settled no_diff on a plan
//     that declared seven implementation steps).
//   - a multi verdict is validated against the stacking rules before the
//     engine-synthesized chunk_plan_validate gate runs; an invalid plan is
//     rerouted to decompose through the engine's repair loop (the
//     synthesized graph already carries the edge).
//
// The route is only refused when the repair loop is exhausted. Single-mode
// routes pass through untouched.
func (c *LinearController) chunkPlanRepairRoute(ctx context.Context, step definition.Step, route RouteDecision, outMap map[string]any) (RouteDecision, bool, error) {
	if c.Workflow == nil || c.Workflow.Stacking == nil {
		return route, false, nil
	}
	if step.ID != synthesizedDecomposeStepID {
		return route, false, nil
	}
	// no_bug routes decompose straight to success. The verdict is trusted only
	// when the plan itself declares no actionable work; a plan with steps must
	// be delivered through single or multi mode.
	if mode, _ := outMap["stack_mode"].(string); mode == "no_bug" {
		planStep := c.Workflow.Stacking.PlanStep
		if planStep == "" {
			return route, false, fmt.Errorf("chunk plan validation failed: no_bug verdict has no plan step to check against")
		}
		actionable, err := c.planDeclaresActionableSteps(ctx, planStep)
		if err != nil {
			return route, false, fmt.Errorf("chunk plan validation could not read the plan output: %w", err)
		}
		if !actionable {
			return route, false, nil // the plan really is a no-op; no_bug stands
		}
		// Contradiction: the plan declares steps but decompose reports no
		// actionable change. Reroute through the bounded repair loop so the
		// decompose agent must produce a real chunk plan. On loop exhaustion
		// the run fails honestly instead of silently dropping the work.
		return c.decomposeRepairRoute(ctx, step, route)
	}
	if route.ToStepID != synthesizedChunkPlanValidateStepID {
		return route, false, nil
	}
	raw, err := json.Marshal(outMap)
	if err != nil {
		return route, false, fmt.Errorf("chunk plan validation could not marshal decompose output: %w", err)
	}
	outcome, err := ValidateChunkPlan(raw, c.Workflow.Stacking)
	if err != nil {
		return route, false, fmt.Errorf("chunk plan validation failed: %w", err)
	}
	if outcome.Valid {
		return route, false, nil
	}
	return c.decomposeRepairRoute(ctx, step, route)
}

// decomposeRepairRoute rewrites the route back to the decompose step through
// the engine's bounded repair loop (decompose_repair). When the loop budget is
// exhausted the run takes the loop-exhaustion route instead: the decompose
// verdict was rejected repeatedly, and the run must stop with an honest cause
// rather than advance on an unchecked plan.
func (c *LinearController) decomposeRepairRoute(ctx context.Context, step definition.Step, route RouteDecision) (RouteDecision, bool, error) {
	maxRepairs := stackingRepairLoopMax(c.Workflow)
	if err := c.checkLoopCap(ctx, stackingRepairLoopName, maxRepairs); err != nil {
		decision := matcher.Decision{
			TransitionIndex: route.TransitionIndex,
			ToStepID:        route.ToStepID,
			MatchDigest:     route.MatchDigest,
			DecisionJSON:    append([]byte(nil), route.DecisionJSON...),
		}
		rr, rerr := c.loopExhaustionRoute(ctx, step, decision, c.loopExhaustedRouteError(ctx, err, step.ID))
		return rr, true, rerr
	}
	repair := RouteDecision{
		ToStepID:        synthesizedDecomposeStepID,
		TransitionIndex: route.TransitionIndex,
		MatchDigest:     route.MatchDigest,
		DecisionJSON:    append([]byte(nil), route.DecisionJSON...),
		Loop:            stackingRepairLoopName,
		MaxIterations:   maxRepairs,
	}
	return repair, true, nil
}

// planDeclaresActionableSteps resolves the run's plan artifact and reports
// whether the plan declares any steps. The plan-v1 schema requires at least
// one step, so for plan-v1 plans this is always true and a no_bug verdict is
// always a contradiction. A plan whose artifact cannot be resolved or parsed
// cannot prove a no-op and fails closed as actionable. Only a plan that
// explicitly declares zero steps (or carries no steps field at all) is
// treated as a genuine no-op.
func (c *LinearController) planDeclaresActionableSteps(ctx context.Context, planStep string) (bool, error) {
	attempts, err := c.Repo.ListStepAttempts(ctx, c.RunID)
	if err != nil {
		return false, err
	}
	prior, ok := latestOutputAttempt(attempts, planStep)
	if !ok {
		// No plan output in this run: the no_bug verdict cannot be verified
		// against a plan, so it fails closed as actionable.
		return true, nil
	}
	raw, err := c.Repo.LoadContent(ctx, prior.OutputRef)
	if err != nil {
		return false, err
	}
	var doc struct {
		Steps []string `json:"steps"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		// An unparseable plan artifact (prose, or a ledger reference
		// envelope) cannot prove a no-op; fail closed as actionable.
		return true, nil
	}
	return len(doc.Steps) > 0, nil
}
