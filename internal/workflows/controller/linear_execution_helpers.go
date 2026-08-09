package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// agentFailureRepairable reports whether a genuine agent/runner failure may
// re-enter the graph via its declared on_failure target: the step must name a
// non-terminal target, and the step must not have spent its re-entry budget.
// It mirrors hostFailureRepairable for evidence-gate host failures. The
// attempt being settled is still Running in the ledger (not yet Failed), so it
// is counted here as spent, exactly as hostFailureRepairable counts the
// already-settled attempt.
func (c *LinearController) agentFailureRepairable(ctx context.Context, step definition.Step, route RouteDecision) bool {
	if step.OnFailure == "" || workflowledger.IsTerminalStepID(route.ToStepID) {
		return false
	}
	attempts, err := c.Repo.ListStepAttempts(ctx, c.RunID)
	if err != nil {
		// The history cannot be read, so the budget is unknown. Fail rather
		// than re-enter on a guess: an unbounded repair is worse than one
		// honest failure.
		return false
	}
	// The attempt being settled is still Running, so it is not in the ledger's
	// Failed set yet; count it here (hostFailureRepairable counts the
	// already-settled attempt, which is the same budget).
	spent := 1
	for _, a := range attempts {
		if a.StepID == step.ID && a.Status == workflowledger.AttemptStatusFailed {
			spent++
		}
	}
	return spent < maxOnFailureReentries
}

// settleAttemptOutcome classifies one attempt's child result into its terminal
// status and durable route, mutating result.ErrorRef so a failed attempt
// persists its cause (Defect 2, storeErrorText stays fail-soft). It returns
// the status and route to record, the effective error, whether the outcome was
// degraded by the controller (a SUCCEEDED child flipped to Failed by route
// selection or zero-progress — never a genuine agent/runner failure, so never
// diverted to on_failure), and any error from route selection.
func (c *LinearController) settleAttemptOutcome(writeCtx context.Context, step definition.Step, result *AgentStepResult, runErr error) (workflowledger.AttemptStatus, error, RouteDecision, bool, error) {
	// degraded marks a SUCCEEDED child whose outcome was flipped to Failed by
	// the controller itself (route selection, zero-progress). Those are not
	// genuine agent/runner failures and never divert to on_failure.
	degraded := false
	status := classifyStepStatus(runErr, result.Status)
	if runErr != nil {
		result.ErrorRef = storeErrorText(writeCtx, c.Repo, runErr)
	}
	route := RouteDecision{}
	var err error
	if status == workflowledger.AttemptStatusSucceeded {
		// A classified success routes even when the join/persistence boundary
		// errored (child reported "completed"): compute the route from the
		// completed child's output so the attempt is never persisted
		// Succeeded with an empty ToStepID. Route-selection failure degrades
		// to a durable on_failure route, never a route-less success.
		outMap, mapErr := resultOutputMap(*result)
		if mapErr != nil {
			degraded = true
			status, runErr = workflowledger.AttemptStatusFailed, mapErr
			// Defect 2: a succeeded child whose route selection fails flips to
			// Failed here; persist the route-selection cause so the attempt's
			// ErrorRef is never empty (storeErrorText stays fail-soft).
			result.ErrorRef = storeErrorText(writeCtx, c.Repo, mapErr)
			route = failureRoute(step)
		} else {
			// Route computation reads the ledger (loop counters, prior review
			// output). Use the detached writeCtx, not ctx: at the run deadline
			// ctx is already expired, and a context.DeadlineExceeded from those
			// reads would mis-record a completed child as Failed on on_failure.
			route, err = c.selectRoute(writeCtx, step, status, outMap)
			if err != nil {
				degraded = true
				status, runErr = workflowledger.AttemptStatusFailed, err
				result.ErrorRef = storeErrorText(writeCtx, c.Repo, err)
				if route.ToStepID == "" {
					route = failureRoute(step)
				}
			} else if noProgress, zpErr := c.reviewMadeNoProgress(writeCtx, step, route, outMap); zpErr != nil {
				// A ledger-read failure inside the zero-progress check is a HARD
				// step failure: the controller cannot safely route a review
				// whose prior findings it could not read, so it must not take
				// the loop back-edge on a guess. This matches agentStepRequest's
				// GetLoopCounters error behavior — a loop-bound step whose
				// counters cannot be read fails hard instead of proceeding with
				// a fabricated round. The durable cause is persisted so the
				// attempt carries the exact failure.
				degraded = true
				readErr := fmt.Errorf("zero-progress check failed to read prior review output: %w", zpErr)
				status, runErr, route = c.failReviewNoProgress(writeCtx, result, step, readErr)
			} else if noProgress {
				// Zero progress across rounds: a review that repeats the
				// previous round's findings must NOT take the loop back-edge.
				// Treat the review as failed with the durable cause so the run
				// stops instead of spinning identical findings.
				degraded = true
				noProgressErr := errors.New("review made no progress across rounds (identical findings set); run failed")
				status, runErr, route = c.failReviewNoProgress(writeCtx, result, step, noProgressErr)
			}
		}
	} else if status == workflowledger.AttemptStatusFailed {
		route = failureRoute(step) // transport budget already spent upstream
	}
	return status, runErr, route, degraded, err
}

// classifyStepStatus maps a runner error and the child's reported status to
// the attempt terminal status. The child's own status is authoritative for
// timeouts/cancels: joinWithCancellation can pair a child timed_out/canceled
// status with a parent ctx error, and the child's terminal status must win.
// The error type is the fallback when the runner reports no status.
func classifyStepStatus(runErr error, childStatus string) workflowledger.AttemptStatus {
	if runErr == nil {
		return workflowledger.AttemptStatusSucceeded
	}
	// A schema-validation error means the child's output FAILED the declared
	// OutputSchema. It is a genuine agent failure regardless of the child's
	// reported status: the runner pairs a "completed" child with the validation
	// error, and routing that schema-invalid output onward as Succeeded would
	// bypass the OutputSchema guard. Schema failures use on_failure, never a
	// success route. (SchemaValidationError.Unwrap exposes the jschema cause, so
	// errors.As matches direct and wrapped forms.)
	var schemaErr *SchemaValidationError
	if errors.As(runErr, &schemaErr) {
		return workflowledger.AttemptStatusFailed
	}
	switch childStatus {
	case "completed":
		// The child's work completed; a parent ctx error racing the result
		// (deadline/cancel at the join boundary) must not discard it.
		return workflowledger.AttemptStatusSucceeded
	case "timed_out":
		return workflowledger.AttemptStatusTimedOut
	case "canceled":
		// A canceled child is ambiguous: joinWithCancellation cancels the
		// child when the PARENT expires. A parent deadline means the RUN
		// timed out (must settle timed_out, not canceled); an explicit
		// parent cancel is a run cancel.
		if errors.Is(runErr, context.DeadlineExceeded) {
			return workflowledger.AttemptStatusTimedOut
		}
		return workflowledger.AttemptStatusCanceled
	case "failed":
		// A genuine child failure wins over a parent ctx error racing it.
		return workflowledger.AttemptStatusFailed
	default:
		if errors.Is(runErr, context.DeadlineExceeded) {
			return workflowledger.AttemptStatusTimedOut
		}
		if errors.Is(runErr, context.Canceled) {
			return workflowledger.AttemptStatusCanceled
		}
		return workflowledger.AttemptStatusFailed
	}
}

// stepPersistenceContext bounds the writes that record a step outcome. It is
// DETACHED from the parent (WithoutCancel) so a run deadline or cancel that
// expires between RunStep returning and the persistence writes cannot lose
// the step result: the 5s window is measured from this call, not inherited
// from an already-expired parent. The abandon fence, not context, is the
// authority for Interrupt.
func stepPersistenceContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

func (c *LinearController) failAttempt(ctx context.Context, run workflowledger.RunSnapshot, attempt workflowledger.StepAttempt, cause error) (workflowledger.RunSnapshot, bool, error) {
	writeCtx, cancel := stepPersistenceContext(ctx)
	defer cancel()
	// Defect 2: a failed attempt must persist its cause. storeErrorText is
	// fail-soft: a store failure returns "" and never masks the cause.
	result := AgentStepResult{ErrorRef: storeErrorText(writeCtx, c.Repo, cause)}
	_ = CompleteExistingStepResult(writeCtx, c.Repo, attempt, result, workflowledger.AttemptStatusFailed, RouteDecision{})
	// The attempt is now recorded as failed. Report the completion once with
	// the failed status. The controller resolves the step from the attempt,
	// so every caller emits the same event.
	step, ok := c.WorkflowStep(attempt.StepID)
	if !ok {
		step = definition.Step{ID: attempt.StepID}
	}
	c.emitStepCompleted(step, attempt, "failed")
	return c.fail(writeCtx, run, cause)
}

// maxOnFailureReentries bounds how many times one agent step may re-enter its
// declared non-terminal on_failure target after genuine agent/runner failures.
//
// The two caps that bound other re-entries do not reach this one.
// enforceGlobalAttemptCap does nothing when a workflow leaves
// max_step_attempts unset, which is legal, and checkLoopCap fires only for a
// named back-edge while an on_failure route carries no loop. Without this
// number, a workflow whose author declared an on_failure cycle (the compiler
// accepts non-terminal on_failure targets and does not reject the cycle) would
// spin to the run deadline.
const maxOnFailureReentries = 3
