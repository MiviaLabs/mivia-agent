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
	return spent < c.onFailureReentryLimit()
}

// settleAttemptOutcome classifies one attempt's child result into its terminal
// status and durable route, mutating result.ErrorRef so a failed attempt
// persists its cause (Defect 2, storeErrorText stays fail-soft). It returns
// the status and route to record, the effective error, whether the outcome was
// degraded by the controller (a SUCCEEDED child flipped to Failed by route
// selection, a blocked write, or zero-progress — never a genuine agent/runner
// failure, so never diverted to on_failure), and any error from route
// selection.
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
			route, degraded, runErr, err = c.settleSucceededRoute(writeCtx, step, result, outMap)
			// Every controller degradation in settleSucceededRoute flips the
			// child from Succeeded to Failed (route-selection failure, the
			// chunk-plan gate, a blocked write, zero-progress); mirror it on
			// the shared status so the caller persists the attempt as Failed.
			if degraded {
				status = workflowledger.AttemptStatusFailed
			}
			if runErr != nil && route.ToStepID == "" {
				route = failureRoute(step)
			}
		}
	} else if status == workflowledger.AttemptStatusFailed {
		route = failureRoute(step) // transport budget already spent upstream
	}
	return status, runErr, route, degraded, err
}

// settleSucceededRoute computes the durable route for a classified-succeeded
// child: route selection, the deterministic chunk-plan gate for the
// engine-synthesized decompose step, blocked-path checks, and the review
// zero-progress guard. It returns the route, whether the outcome was degraded
// to Failed by the controller, the effective error, and the route-selection
// error (for the caller's audit trail).
func (c *LinearController) settleSucceededRoute(writeCtx context.Context, step definition.Step, result *AgentStepResult, outMap map[string]any) (RouteDecision, bool, error, error) {
	degraded := false
	status := workflowledger.AttemptStatusSucceeded
	var runErr error
	// Chunk finding scope must run BEFORE route selection: the matcher
	// reads the verdict, so sibling-chunk-only findings are dropped and an
	// emptied verdict flips to approved here, with the filtered shape
	// persisted (see applyChunkFindingScope).
	c.applyChunkFindingScope(step, result, outMap)
	// Route computation reads the ledger (loop counters, prior review
	// output). Use the detached writeCtx, not ctx: at the run deadline
	// ctx is already expired, and a context.DeadlineExceeded from those
	// reads would mis-record a completed child as Failed on on_failure.
	route, err := c.selectRoute(writeCtx, step, status, outMap)
	if err != nil {
		degraded = true
		runErr = err
		result.ErrorRef = storeErrorText(writeCtx, c.Repo, err)
		if route.ToStepID == "" {
			route = failureRoute(step)
		}
	} else if rr, repaired, rerr := c.chunkPlanRepairRoute(writeCtx, step, route, outMap); rerr != nil {
		// The deterministic chunk-plan gate could not be honored (loop
		// exhausted or unparseable decompose output): the run stops with an
		// honest cause instead of advancing on an unchecked plan.
		degraded = true
		runErr = rerr
		route = rr
		result.ErrorRef = storeErrorText(writeCtx, c.Repo, rerr)
	} else if repaired {
		// The decompose output failed the deterministic rules; the route is
		// rewritten back to decompose through the engine's repair loop. The
		// attempt stays Succeeded; the loop counter records the re-entry.
		route = rr
	} else if rr, oversized := c.chunkDiffSizeGate(writeCtx, step, route); oversized {
		// The implement step succeeded but the actual worktree diff exceeds
		// the stacking hard limit; the route is rewritten to the workflow's
		// diff-size repair step so the chunk is shrunk before the panel and
		// preflight pipeline run on it. The attempt stays Succeeded.
		route = rr
	} else if blockedErr, blocked := c.blockedCause(writeCtx, outMap); blocked {
		// A SUCCEEDED step whose output admits a write it cannot
		// perform (blocked_paths, a claimed files_changed entry inside
		// the host write-path blocklist, or a review finding demanding
		// a blocked-path edit) must fail the run HERE. Routing it to
		// review would reproduce the same demand and burn the loop
		// budget into a misattributed zero-progress failure; the run
		// cannot deliver, so it stops with an honest blocked cause.
		degraded = true
		status, runErr = workflowledger.AttemptStatusFailed, blockedErr
		result.ErrorRef = storeErrorText(writeCtx, c.Repo, blockedErr)
		route = failureRoute(step)
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
	return route, degraded, runErr, err
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
//
// The budget is configurable per workflow via [limits] max_on_failure_reentries;
// 0 (or an absent [limits] section) means this default. The same knob bounds
// agent_panel re-entries (panel failures settle through settleAgentAttempt)
// and evidence_gate host-failure repairs (hostFailureRepairable).
const defaultMaxOnFailureReentries = 3

// onFailureReentryLimit returns the per-step re-entry budget declared by the
// workflow's [limits] max_on_failure_reentries, falling back to the controller
// default when the workflow leaves it at 0. It is the single knob behind
// agentFailureRepairable and hostFailureRepairable, so agents, panels, and
// gate host repairs all spend the same configurable budget.
func (c *LinearController) onFailureReentryLimit() int {
	if c.Workflow != nil && c.Workflow.Limits.MaxOnFailureReentries > 0 {
		return c.Workflow.Limits.MaxOnFailureReentries
	}
	return defaultMaxOnFailureReentries
}

// deliveryRepairRoute resolves the workflow's delivery repair target, falling
// back from OnFailure to OnPRMetadataFailure to OnDiffSizeFailure. It mirrors
// the compile-validated route chain used by the stale-delivery heal path; an
// empty result means the workflow declares no repair target.
func deliveryRepairRoute(d *definition.Delivery) string {
	if d == nil {
		return ""
	}
	route := d.OnFailure
	if route == "" {
		route = d.OnPRMetadataFailure
	}
	if route == "" {
		route = d.OnDiffSizeFailure
	}
	return route
}
