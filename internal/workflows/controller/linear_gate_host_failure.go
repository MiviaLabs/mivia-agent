package controller

import (
	"context"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// maxHostFailureRepairs bounds how many times one gate step may re-enter a
// repair after its verifier failed to run.
//
// The two caps that bound other re-entries do not reach this one.
// enforceGlobalAttemptCap does nothing when a workflow leaves
// max_step_attempts unset, which is legal, and checkLoopCap fires only for a
// named back-edge while this route carries no loop. Without this number, a
// workflow with no limits and a permanently broken host would repair forever.
const maxHostFailureRepairs = 3

// settleHostFailure records a gate whose verifier could not run, and decides
// where the run goes next.
//
// A host failure is a MISSING verdict, not a verdict of "fail". The sandbox
// did not start, the binary was absent, the check was killed. None of those
// says anything about the delivered change, so the attempt is recorded Failed
// with the cause, never as a pass.
//
// Where it goes next is the workflow author's choice. A step that names a
// repair target reaches it, so a run can fix a broken host and carry on
// instead of losing every finished step. A step that names nothing, or names a
// terminal, keeps the old behavior exactly: the run fails and the host cause
// travels back to the caller, which is the only signal an operator gets.
func (c *LinearController) settleHostFailure(ctx context.Context, run workflowledger.RunSnapshot, attempt workflowledger.StepAttempt, step definition.Step, output []byte) (workflowledger.RunSnapshot, bool, error) {
	route := failureRoute(step)
	hostErr := fmt.Errorf("verifier %q has a host failure", step.Verifier)
	result := AgentStepResult{Output: output, ErrorRef: storeErrorText(ctx, c.Repo, hostErr)}
	if err := CompleteExistingStepResult(ctx, c.Repo, attempt, result, workflowledger.AttemptStatusFailed, route); err != nil {
		return c.fail(ctx, run, err)
	}
	// The failed attempt is durable. Report the completion once with the
	// failed status before deciding where the run goes next.
	c.emitStepCompleted(step, attempt, string(workflowledger.AttemptStatusFailed))
	if !c.hostFailureRepairable(ctx, step, route) {
		return c.fail(ctx, run, hostErr)
	}
	return settleAfterRoute(ctx, c, run, route)
}

// hostFailureRepairable reports whether this host failure may re-enter the
// graph: the step must name a non-terminal target, and the step must not have
// spent its re-entry budget.
func (c *LinearController) hostFailureRepairable(ctx context.Context, step definition.Step, route RouteDecision) bool {
	if step.OnFailure == "" || definition.ReservedStepIDs[route.ToStepID] {
		return false
	}
	attempts, err := c.Repo.ListStepAttempts(ctx, c.RunID)
	if err != nil {
		// The history cannot be read, so the budget is unknown. Fail rather
		// than re-enter on a guess: an unbounded repair is worse than one
		// honest failure.
		return false
	}
	// The attempt being settled is already recorded, so it counts here.
	spent := 0
	for _, a := range attempts {
		if a.StepID == step.ID && a.Status == workflowledger.AttemptStatusFailed {
			spent++
		}
	}
	return spent < maxHostFailureRepairs
}
