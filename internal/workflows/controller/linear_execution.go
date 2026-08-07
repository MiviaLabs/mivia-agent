package controller

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func (c *LinearController) advanceAgentStep(ctx context.Context, run workflowledger.RunSnapshot, step definition.Step, runtime StepRuntime) (workflowledger.RunSnapshot, bool, error) {
	attempts, err := c.Repo.ListStepAttempts(ctx, c.RunID)
	if err != nil {
		return run, false, err
	}
	// A non-terminal latest attempt with a recorded coordinator identity is a
	// crash artifact whose child may already have run: JOIN the recorded
	// coordinator run per the ledger contract (internal/workflows/ledger/recovery.go)
	// before admitting anything fresh, so a completed child's outcome settles
	// the attempt and its work is never re-executed under a new identity.
	if latest, found := latestAttempt(attempts, step.ID); found &&
		!workflowledger.IsTerminalAttemptStatus(latest.Status) &&
		latest.CoordinatorRunID != "" && latest.TaskID != "" {
		return c.joinInFlightAttempt(ctx, run, step, runtime, latest, attempts)
	}
	attempt, ok, err := c.admitAttempt(ctx, run, step.ID, attempts)
	if err != nil {
		return c.fail(ctx, run, err)
	}
	if !ok {
		return c.reconcileTerminalAttempt(ctx, run, attempt)
	}
	// Refresh attempts after possible create for context binding.
	attempts, err = c.Repo.ListStepAttempts(ctx, c.RunID)
	if err != nil {
		return run, false, err
	}
	return c.executeAgentAttempt(ctx, run, step, runtime, attempt, attempts)
}

// joinInFlightAttempt resumes a step whose latest attempt is in-flight by
// JOINING the recorded coordinator run first, per the ledger contract: a
// recorded attempt is never re-dispatched. When the join yields the child's
// terminal outcome, the attempt is completed with that outcome and its route —
// the step's work is NOT re-executed. Only when the runner cannot join, or the
// join shows the child never ran (nothing to join), does the controller
// interrupt the stale attempt and admit a fresh one with a new identity.
func (c *LinearController) joinInFlightAttempt(ctx context.Context, run workflowledger.RunSnapshot, step definition.Step, runtime StepRuntime, attempt workflowledger.StepAttempt, attempts []workflowledger.StepAttempt) (workflowledger.RunSnapshot, bool, error) {
	joiner, canJoin := c.Runner.(StepRunJoiner)
	if !canJoin {
		// No join capability (test seams, non-coordinator runners): keep the
		// pre-join resume behavior for the stale attempt.
		return c.interruptAndRedispatch(ctx, run, step, runtime, attempt, attempts)
	}
	req, err := c.agentStepRequest(ctx, run, step, runtime, attempt, attempts)
	if err != nil {
		return c.failAttempt(ctx, run, attempt, err)
	}
	result, joined, joinErr := joiner.JoinStep(ctx, req)
	if joined {
		// The child ran (or is being joined to completion): complete the
		// attempt with the child's outcome and its durable route. This reuses
		// the route-on-succeeded logic from executeAgentAttempt, so a joined
		// child that reported "completed" is never persisted Succeeded with an
		// empty ToStepID.
		return c.settleAgentAttempt(ctx, run, step, attempt, result, joinErr)
	}
	// Nothing to join: the child never ran (or the join could not be
	// completed, e.g. an idempotency conflict from drifted step inputs). Mark
	// the stale attempt interrupted and admit a fresh attempt with a new
	// coordinator identity, exactly as a crashed executor would have left it.
	if joinErr != nil {
		log.Printf("workflow: run %s step %s attempt %d join did not resolve (%v); re-dispatching fresh", c.RunID, step.ID, attempt.AttemptNo, joinErr)
	}
	return c.interruptAndRedispatch(ctx, run, step, runtime, attempt, attempts)
}

// interruptAndRedispatch marks the stale in-flight attempt interrupted and
// admits a fresh attempt (No+1) with a new coordinator identity, then executes
// it. This is the pre-join resume behavior, used only when the recorded child
// never ran (nothing to join) or the runner cannot join.
func (c *LinearController) interruptAndRedispatch(ctx context.Context, run workflowledger.RunSnapshot, step definition.Step, runtime StepRuntime, attempt workflowledger.StepAttempt, attempts []workflowledger.StepAttempt) (workflowledger.RunSnapshot, bool, error) {
	writeCtx, cancel := stepPersistenceContext(ctx)
	defer cancel()
	fresh, ok, err := c.admitAttempt(writeCtx, run, step.ID, attempts)
	if err != nil {
		return c.fail(writeCtx, run, err)
	}
	if !ok {
		return c.reconcileTerminalAttempt(writeCtx, run, fresh)
	}
	attempts, err = c.Repo.ListStepAttempts(writeCtx, c.RunID)
	if err != nil {
		return run, false, err
	}
	// The persistence context bounds ONLY the admit/persist writes above. The
	// fresh child must run under the CALLER's context (the run-loop ctx carrying
	// the step deadline): a child bound by the 5s writeCtx would be canceled at
	// the coordinator.Join boundary after 5s and the re-dispatched attempt would
	// time out even though the run deadline is much longer.
	return c.executeAgentAttempt(ctx, run, step, runtime, fresh, attempts)
}

// JoinInFlightAttempt joins one recorded in-flight attempt's coordinator run
// per the ledger contract (a recorded attempt is JOINED, never re-dispatched).
// It is the CLI resume boundary's consumer of PlanResume.AttemptsInFlight: each
// recorded in-flight attempt is settled from its child's outcome before the
// controller's Run loop starts, so a completed child is never orphaned and its
// work is never re-executed. When the join shows nothing to join (no join
// capability, or the child never ran), the attempt is left in-flight for
// Advance to interrupt and re-dispatch under the run claim. Idempotent: an
// attempt that is already terminal (or no longer the latest) is a no-op.
func (c *LinearController) JoinInFlightAttempt(ctx context.Context, attempt workflowledger.StepAttempt) error {
	if workflowledger.IsTerminalAttemptStatus(attempt.Status) {
		return nil
	}
	step, ok := c.WorkflowStep(attempt.StepID)
	if !ok {
		return fmt.Errorf("workflow step %q is not declared", attempt.StepID)
	}
	runtime, ok := c.Steps[attempt.StepID]
	if !ok {
		return fmt.Errorf("step %q has no snapshotted runtime", attempt.StepID)
	}
	run, err := c.Repo.GetRun(ctx, c.RunID)
	if err != nil {
		return err
	}
	attempts, err := c.Repo.ListStepAttempts(ctx, c.RunID)
	if err != nil {
		return err
	}
	latest, found := latestAttempt(attempts, attempt.StepID)
	if !found || latest.AttemptID != attempt.AttemptID || workflowledger.IsTerminalAttemptStatus(latest.Status) {
		return nil // already settled or superseded (idempotent replay)
	}
	joiner, canJoin := c.Runner.(StepRunJoiner)
	if !canJoin {
		return nil // no join capability: Advance falls back to interrupt + re-dispatch
	}
	req, err := c.agentStepRequest(ctx, run, step, runtime, latest, attempts)
	if err != nil {
		return err
	}
	result, joined, joinErr := joiner.JoinStep(ctx, req)
	if !joined {
		// The child never ran (or the join could not confirm it): leave the
		// attempt in-flight; the controller's Advance interrupts it and admits
		// a fresh attempt under the run claim.
		if joinErr != nil {
			log.Printf("workflow: run %s step %s attempt %d pre-flight join did not resolve (%v); Advance will interrupt and re-dispatch", c.RunID, step.ID, attempt.AttemptNo, joinErr)
		}
		return nil
	}
	_, _, err = c.settleAgentAttempt(ctx, run, step, latest, result, joinErr)
	return err
}

func (c *LinearController) newAttempt(stepID string, attemptNo int) workflowledger.StepAttempt {
	return workflowledger.StepAttempt{
		AttemptID: fmt.Sprintf("wfa-%s-%d", stepID, attemptNo), RunID: c.RunID, StepID: stepID,
		AttemptNo: attemptNo, Status: workflowledger.AttemptStatusRunning,
		CoordinatorRunID: coordinator.NewRunID(), TaskID: newWorkflowTaskID(),
	}
}

func (c *LinearController) reconcileTerminalAttempt(ctx context.Context, run workflowledger.RunSnapshot, attempt workflowledger.StepAttempt) (workflowledger.RunSnapshot, bool, error) {
	switch attempt.Status {
	case workflowledger.AttemptStatusSucceeded:
		return c.fail(ctx, run, fmt.Errorf("step %q succeeded without a durable route", attempt.StepID))
	case workflowledger.AttemptStatusCanceled:
		return c.failWithStatus(ctx, run, context.Canceled, workflowledger.RunStatusCanceled)
	case workflowledger.AttemptStatusTimedOut:
		return c.failWithStatus(ctx, run, context.DeadlineExceeded, workflowledger.RunStatusTimedOut)
	default:
		return c.fail(ctx, run, fmt.Errorf("step %q has terminal attempt status %q", attempt.StepID, attempt.Status))
	}
}

// runStepWithTransientRetry runs the step's agent, retrying transient
// LLM-provider failures (overload, rate limit, upstream 5xx) with a FRESH
// subagent run: re-running the whole step resets the child's accumulated
// context, and the coordinator dedupes on TaskID, so a new identity is
// minted per retry. The
// coordinator run ID is minted alongside the task ID: the idempotency key
// encodes the TaskID (agent_step.go), so a retry with the old run ID and a
// new key would find the key absent and collide with the existing run
// (ErrDuplicate on the reused run ID). Real agent failures (schema, binding,
// refusal) do not match the transient markers and fail immediately,
// preserving the "agent failures use on_failure" contract. The step attempt
// is persisted only after the final outcome, so the retry budget never leaks
// extra attempts into the ledger.
func (c *LinearController) runStepWithTransientRetry(ctx context.Context, req AgentStepRequest, attempt workflowledger.StepAttempt, step definition.Step) (AgentStepResult, error) {
	result, runErr := c.Runner.RunStep(ctx, req)
	for i := 0; runErr != nil && i < maxTransientStepRetries && isTransientProviderError(runErr); i++ {
		retryTaskID := newWorkflowTaskID()
		retryRunID := coordinator.NewRunID()
		if err := c.Repo.SetStepAttemptExecution(ctx, c.RunID, attempt.AttemptID, retryRunID, retryTaskID); err != nil {
			return AgentStepResult{}, fmt.Errorf("persist transient retry identity: %w", err)
		}
		log.Printf("workflow: run %s step %s attempt %d transient provider failure: %v; retrying in %s", c.RunID, step.ID, attempt.AttemptNo, runErr, stepTransientRetryBackoff(i))
		select {
		case <-ctx.Done():
			return AgentStepResult{}, ctx.Err()
		case <-time.After(stepTransientRetryBackoff(i)):
			req.TaskID = retryTaskID
			req.CoordinatorRunID = retryRunID
			result, runErr = c.Runner.RunStep(ctx, req)
		}
	}
	return result, runErr
}

func (c *LinearController) executeAgentAttempt(ctx context.Context, run workflowledger.RunSnapshot, step definition.Step, runtime StepRuntime, attempt workflowledger.StepAttempt, attempts []workflowledger.StepAttempt) (workflowledger.RunSnapshot, bool, error) {
	req, err := c.agentStepRequest(ctx, run, step, runtime, attempt, attempts)
	if err != nil {
		return c.failAttempt(ctx, run, attempt, err)
	}
	result, runErr := c.runStepWithTransientRetry(ctx, req, attempt, step)
	return c.settleAgentAttempt(ctx, run, step, attempt, result, runErr)
}

// stepLoopName returns the loop name declared on the step's own outgoing
// back-edge transition, or "" when the step is not in a loop. The controller
// routes and counts loops on the step's outgoing transitions (selectRoute,
// recordLoopAfterComplete), so the step's loop is the Loop field of its
// outgoing transition. The compiler allows each loop name on exactly one
// transition, so a step names at most one loop in practice; the first match
// is authoritative.
func (c *LinearController) stepLoopName(step definition.Step) string {
	if c == nil || c.Workflow == nil {
		return ""
	}
	for _, t := range c.Workflow.Transitions {
		if t.From == step.ID && t.Loop != "" {
			return t.Loop
		}
	}
	return ""
}

// agentStepRequest builds the bounded dispatch request for one attempt from
// the step's snapshotted runtime and the current context, using the attempt's
// recorded coordinator identity. On a resume join the identity is the
// attempt's ORIGINAL child, so the production runner joins that child instead
// of dispatching a fresh one. The step prompt is rendered HERE, in the
// controller, and persisted per attempt: a resume JOIN reuses the stored
// prompt (fingerprint-stable) instead of re-rendering, and the runner never
// re-renders when spec.Prompt is set.
func (c *LinearController) agentStepRequest(ctx context.Context, run workflowledger.RunSnapshot, step definition.Step, runtime StepRuntime, attempt workflowledger.StepAttempt, attempts []workflowledger.StepAttempt) (AgentStepRequest, error) {
	stepInputs, evidence, refs, err := c.contextForStep(ctx, step, attempts)
	if err != nil {
		return AgentStepRequest{}, err
	}
	if err := validateBindingLimits(step, c.Inputs, evidence); err != nil {
		return AgentStepRequest{}, err
	}
	// Synthetic round input (workflow-convergence plan v3): a step whose own
	// outgoing transition is a named loop back-edge gets inputs.round = the
	// loop's durable iteration counter (0 before the first back-edge). Review
	// templates use the round to mint stable finding ids (R<N>-...). Steps
	// outside a loop omit the input.
	if round, inLoop, err := c.roundInputForStep(ctx, step); err != nil {
		return AgentStepRequest{}, err
	} else if inLoop {
		stepInputs["round"] = round
	}
	prompt, err := c.renderStepPrompt(ctx, attempt, runtime, step, stepInputs, evidence, refs)
	if err != nil {
		return AgentStepRequest{}, err
	}
	var timeout time.Duration
	if runtime.Agent.TimeoutSeconds != nil {
		timeout = time.Duration(*runtime.Agent.TimeoutSeconds) * time.Second
	} else if run.DeadlineAt != nil {
		// No per-agent timeout configured: derive one from the remaining run
		// deadline so the agent cannot outlive the run it belongs to (D1).
		if remaining := run.DeadlineAt.Sub(c.now()); remaining > 0 {
			timeout = remaining
		}
	}
	return AgentStepRequest{WorkflowRunID: c.RunID, StepID: step.ID, AttemptNo: attempt.AttemptNo, TaskID: attempt.TaskID, CoordinatorRunID: attempt.CoordinatorRunID, AgentName: runtime.Agent.Name, AgentDigest: runtime.Digest, Skill: step.Skill, ProviderName: runtime.ProviderName, Model: runtime.Model, Timeout: timeout, ForceResume: c.forceResume, Template: runtime.Template, Inputs: stepInputs, Evidence: evidence, MaxBindingBytes: maxBinding(step), MaxContextBytes: maxStepContextBytes, OutputSchema: runtime.Schema, Prompt: prompt, EvidenceRefs: refs}, nil
}

// settleAgentAttempt records one attempt's outcome from the child result and
// error, computing a durable route. A classified success routes even when the
// join/persistence boundary errored (child reported "completed"): compute the
// route from the completed child's output so the attempt is never persisted
// Succeeded with an empty ToStepID. Route-selection failure degrades to a
// durable on_failure route, never a route-less success. Infrastructure/schema/
// agent failures use on_failure, never repair loops. Both the fresh-dispatch
// path (executeAgentAttempt) and the resume-join path (joinInFlightAttempt,
// JoinInFlightAttempt) settle through here, so a joined child's outcome is
// recorded exactly like a fresh child's.
func (c *LinearController) settleAgentAttempt(ctx context.Context, run workflowledger.RunSnapshot, step definition.Step, attempt workflowledger.StepAttempt, result AgentStepResult, runErr error) (workflowledger.RunSnapshot, bool, error) {
	writeCtx, cancel := stepPersistenceContext(ctx)
	defer cancel()
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
		outMap, mapErr := resultOutputMap(result)
		if mapErr != nil {
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
				readErr := fmt.Errorf("zero-progress check failed to read prior review output: %w", zpErr)
				status, runErr, route = c.failReviewNoProgress(writeCtx, &result, step, readErr)
			} else if noProgress {
				// Zero progress across rounds: a review that repeats the
				// previous round's findings must NOT take the loop back-edge.
				// Treat the review as failed with the durable cause so the run
				// stops instead of spinning identical findings.
				noProgressErr := errors.New("review made no progress across rounds (identical findings set); run failed")
				status, runErr, route = c.failReviewNoProgress(writeCtx, &result, step, noProgressErr)
			}
		}
	} else if status == workflowledger.AttemptStatusFailed {
		// Infrastructure/schema/agent failures use on_failure, never repair loops.
		route = failureRoute(step)
	}
	if status == workflowledger.AttemptStatusSucceeded {
		if err = c.completeSucceededRoute(writeCtx, attempt, result, route); err != nil {
			if isLoopAccountError(err) {
				// Route is durable; under-count and continue (same as crash after complete).
				return settleAfterRoute(ctx, c, run, route)
			}
			return c.fail(writeCtx, run, err)
		}
		return settleAfterRoute(ctx, c, run, route)
	}
	if err = CompleteExistingStepResult(writeCtx, c.Repo, attempt, result, status, route); err != nil {
		return c.fail(writeCtx, run, err)
	}
	runStatus := workflowledger.RunStatusFailed
	if status == workflowledger.AttemptStatusCanceled {
		runStatus = workflowledger.RunStatusCanceled
	} else if status == workflowledger.AttemptStatusTimedOut {
		runStatus = workflowledger.RunStatusTimedOut
	}
	return c.failWithStatus(writeCtx, run, runErr, runStatus)
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
	return c.fail(writeCtx, run, cause)
}

// maxTransientStepRetries bounds step-level retries for transient
// LLM-provider failures. Each retry re-runs the whole subagent step with a
// fresh task identity and a fresh child context. Three retries with the
// 10/30/60s backoff give a flaky provider roughly two minutes to recover
// before the step fails.
const maxTransientStepRetries = 3

// transientProviderMarkers identify retryable LLM-provider transport errors:
// overload/rate limits and upstream 5xx, which a fresh subagent run may
// outlive. HTTP 400-class client errors are TERMINAL at the transport layer
// (internal/provider/retry.go) and are deliberately not matched here: a
// step-level retry re-renders a byte-identical prompt (template.Render is a
// pure function of the same inputs), so re-dispatching cannot change the
// outcome. In particular, zai code 1261 "prompt too long" surfaces as
// "provider error (HTTP 400, code 1261: prompt too long)" and must settle
// immediately as attempt failed -> failureRoute (on_failure). Real agent
// failures (schema, binding, refusal) do not match and fail immediately.
var transientProviderMarkers = []string{
	"HTTP 429", "temporarily overloaded", "rate limited", "overloaded",
	"HTTP 500", "HTTP 502", "HTTP 503", "HTTP 504",
	"service unavailable", "upstream", "connection reset", "EOF",
}

func isTransientProviderError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, marker := range transientProviderMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// stepTransientRetryBackoff is the backoff schedule between step-level
// transient retries; overridable in tests.
var stepTransientRetryBackoff = func(attempt int) time.Duration {
	switch attempt {
	case 0:
		return 10 * time.Second
	case 1:
		return 30 * time.Second
	default:
		return 60 * time.Second
	}
}
