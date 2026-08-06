package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/matcher"
)

// selectRoute chooses the next step from closed structural match criteria.
// Infrastructure failures use on_failure and never enter a repair loop.
// Back-edges check per-loop caps before the route is returned; the counter
// increments only after a durable successful completion (see recordLoopAfterComplete).
func (c *LinearController) selectRoute(ctx context.Context, step definition.Step, status workflowledger.AttemptStatus, output map[string]any) (RouteDecision, error) {
	if status != workflowledger.AttemptStatusSucceeded {
		return failureRoute(step), nil
	}
	decision, err := matcher.Match(step.ID, "succeeded", output, c.Workflow.Transitions)
	if err != nil {
		route := RouteDecision{
			ToStepID:        failureTarget(step),
			TransitionIndex: decision.TransitionIndex,
			MatchDigest:     decision.MatchDigest,
			DecisionJSON:    append([]byte(nil), decision.DecisionJSON...),
		}
		return route, fmt.Errorf("transition match failed: %w", err)
	}
	route := RouteDecision{
		ToStepID:        decision.ToStepID,
		TransitionIndex: decision.TransitionIndex,
		MatchDigest:     decision.MatchDigest,
		DecisionJSON:    append([]byte(nil), decision.DecisionJSON...),
		Loop:            decision.Loop,
		MaxIterations:   decision.MaxIterations,
	}
	if decision.Loop != "" {
		if err := c.checkLoopCap(ctx, decision.Loop, decision.MaxIterations); err != nil {
			route.ToStepID = failureTarget(step)
			return route, err
		}
	}
	return route, nil
}

// selectEvidenceFailureRoute selects an explicit failed transition for an
// evidence gate. A verifier result can fail a check without an infrastructure
// error. That result is repairable only when the workflow declares one exact
// failed transition. Missing or ambiguous transitions fail closed.
func (c *LinearController) selectEvidenceFailureRoute(ctx context.Context, step definition.Step, output map[string]any) (RouteDecision, error) {
	decision, err := matcher.Match(step.ID, "failed", output, c.Workflow.Transitions)
	if err != nil {
		route := RouteDecision{
			ToStepID:        failureTarget(step),
			TransitionIndex: decision.TransitionIndex,
			MatchDigest:     decision.MatchDigest,
			DecisionJSON:    append([]byte(nil), decision.DecisionJSON...),
		}
		return route, fmt.Errorf("failed evidence transition match failed: %w", err)
	}
	route := RouteDecision{
		ToStepID:        decision.ToStepID,
		TransitionIndex: decision.TransitionIndex,
		MatchDigest:     decision.MatchDigest,
		DecisionJSON:    append([]byte(nil), decision.DecisionJSON...),
		Loop:            decision.Loop,
		MaxIterations:   decision.MaxIterations,
	}
	if decision.Loop != "" {
		if err := c.checkLoopCap(ctx, decision.Loop, decision.MaxIterations); err != nil {
			route.ToStepID = failureTarget(step)
			return route, err
		}
	}
	return route, nil
}

func failureRoute(step definition.Step) RouteDecision {
	return RouteDecision{ToStepID: failureTarget(step), TransitionIndex: -1}
}

func failureTarget(step definition.Step) string {
	if step.OnFailure != "" {
		return step.OnFailure
	}
	return "failure"
}

// checkLoopCap refuses a back-edge when the durable counter already hit the cap.
// max_iterations = -1 means unlimited per-loop. It does not increment.
func (c *LinearController) checkLoopCap(ctx context.Context, loopName string, maxIterations int) error {
	counters, err := c.Repo.GetLoopCounters(ctx, c.RunID)
	if err != nil {
		return err
	}
	current := 0
	for _, lc := range counters {
		if lc.LoopName == loopName {
			current = lc.Iterations
			break
		}
	}
	if maxIterations >= 0 && current >= maxIterations {
		return fmt.Errorf("loop %q exhausted: max_iterations=%d", loopName, maxIterations)
	}
	return nil
}

// recordLoopAfterComplete increments the loop counter after a successful durable
// route completion. Crash before this call under-counts (safer than over-count).
func (c *LinearController) recordLoopAfterComplete(ctx context.Context, route RouteDecision) error {
	if route.Loop == "" {
		return nil
	}
	if _, err := c.Repo.IncrementLoopCounter(ctx, c.RunID, route.Loop); err != nil {
		return fmt.Errorf("increment loop %q: %w", route.Loop, err)
	}
	return nil
}

// completeSucceededRoute persists the attempt outcome then records a taken loop.
// If the outcome is durable but loop accounting fails, it returns a
// loopAccountError so callers do not fail the whole run for under-count.
func (c *LinearController) completeSucceededRoute(ctx context.Context, attempt workflowledger.StepAttempt, result AgentStepResult, route RouteDecision) error {
	if err := CompleteExistingStepResult(ctx, c.Repo, attempt, result, workflowledger.AttemptStatusSucceeded, route); err != nil {
		return err
	}
	if err := c.recordLoopAfterComplete(ctx, route); err != nil {
		return &loopAccountError{err: err}
	}
	return nil
}

// loopAccountError means the step route was persisted but the loop counter
// write failed. Callers must not mark the run failed for this alone.
type loopAccountError struct{ err error }

func (e *loopAccountError) Error() string {
	if e == nil || e.err == nil {
		return "loop counter write failed after durable route"
	}
	return e.err.Error()
}

func (e *loopAccountError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func isLoopAccountError(err error) bool {
	var target *loopAccountError
	return errors.As(err, &target)
}

// enforceGlobalAttemptCap refuses a new attempt when the run already hit the
// global max_step_attempts ceiling. max_step_attempts <= 0 means unlimited.
func (c *LinearController) enforceGlobalAttemptCap(attempts []workflowledger.StepAttempt) error {
	limit := c.Workflow.Limits.MaxStepAttempts
	if limit <= 0 {
		return nil
	}
	if len(attempts) >= limit {
		return fmt.Errorf("run exceeded max_step_attempts %d (exceeded max attempts)", limit)
	}
	return nil
}

func resultOutputMap(result AgentStepResult) (map[string]any, error) {
	if m, ok := result.ValidatedOutput.(map[string]any); ok {
		return m, nil
	}
	if len(result.Output) == 0 {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal(result.Output, &m); err != nil {
		// Non-object output (scalar, array, null) is a valid child result when
		// no output schema is in force; status-only transitions still route on
		// an empty map instead of failing the whole run.
		return map[string]any{}, nil
	}
	return m, nil
}

func settleAfterRoute(ctx context.Context, c *LinearController, run workflowledger.RunSnapshot, route RouteDecision) (workflowledger.RunSnapshot, bool, error) {
	run, err := c.Repo.GetRun(ctx, c.RunID)
	if err != nil {
		return run, false, err
	}
	if workflowledger.IsTerminalStepID(route.ToStepID) {
		return c.reconcileTerminalRoute(ctx, run)
	}
	return run, false, nil
}
