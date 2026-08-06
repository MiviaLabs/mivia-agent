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
// LLM-provider failures (overload, rate limit, upstream 5xx, or a
// prompt-too-long from accumulated child context) with a FRESH subagent run:
// re-running the whole step resets the child's accumulated context, and the
// coordinator dedupes on TaskID, so a new identity is minted per retry. The
// step attempt is persisted only after the final outcome, so the retry
// budget never leaks extra attempts into the ledger. Real agent failures
// (schema, binding, refusal) do not match the transient markers and fail
// immediately, preserving the "agent failures use on_failure" contract.
func (c *LinearController) runStepWithTransientRetry(ctx context.Context, req AgentStepRequest, attempt workflowledger.StepAttempt, step definition.Step) (AgentStepResult, error) {
	result, runErr := c.Runner.RunStep(ctx, req)
	for i := 0; runErr != nil && i < maxTransientStepRetries && isTransientProviderError(runErr); i++ {
		log.Printf("workflow: run %s step %s attempt %d transient provider failure: %v; retrying in %s", c.RunID, step.ID, attempt.AttemptNo, runErr, stepTransientRetryBackoff(i))
		select {
		case <-ctx.Done():
			return AgentStepResult{}, ctx.Err()
		case <-time.After(stepTransientRetryBackoff(i)):
			req.TaskID = newWorkflowTaskID()
			result, runErr = c.Runner.RunStep(ctx, req)
		}
	}
	return result, runErr
}

func (c *LinearController) executeAgentAttempt(ctx context.Context, run workflowledger.RunSnapshot, step definition.Step, runtime StepRuntime, attempt workflowledger.StepAttempt, attempts []workflowledger.StepAttempt) (workflowledger.RunSnapshot, bool, error) {
	stepInputs, evidence, err := c.contextForStep(ctx, step, attempts)
	if err != nil {
		return c.failAttempt(ctx, run, attempt, err)
	}
	if err := validateBindingLimits(step, c.Inputs, attempts, c.Repo, ctx); err != nil {
		return c.failAttempt(ctx, run, attempt, err)
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
	req := AgentStepRequest{WorkflowRunID: c.RunID, StepID: step.ID, AttemptNo: attempt.AttemptNo, TaskID: attempt.TaskID, CoordinatorRunID: attempt.CoordinatorRunID, AgentName: runtime.Agent.Name, AgentDigest: runtime.Digest, Skill: step.Skill, ProviderName: runtime.ProviderName, Model: runtime.Model, Timeout: timeout, ForceResume: c.forceResume, Template: runtime.Template, Inputs: stepInputs, Evidence: evidence, MaxBindingBytes: maxBinding(step), MaxContextBytes: maxStepContextBytes, OutputSchema: runtime.Schema}
	result, runErr := c.runStepWithTransientRetry(ctx, req, attempt, step)
	writeCtx, cancel := stepPersistenceContext(ctx)
	defer cancel()
	status := classifyStepStatus(runErr, result.Status)
	if runErr != nil {
		result.ErrorRef = storeErrorText(writeCtx, c.Repo, runErr)
	}
	route := RouteDecision{}
	if runErr == nil {
		outMap, mapErr := resultOutputMap(result)
		if mapErr != nil {
			status, runErr = workflowledger.AttemptStatusFailed, mapErr
			route = failureRoute(step)
		} else {
			route, err = c.selectRoute(ctx, step, status, outMap)
			if err != nil {
				status, runErr = workflowledger.AttemptStatusFailed, err
				if route.ToStepID == "" {
					route = failureRoute(step)
				}
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
	_ = CompleteExistingStepResult(writeCtx, c.Repo, attempt, AgentStepResult{}, workflowledger.AttemptStatusFailed, RouteDecision{})
	return c.fail(writeCtx, run, cause)
}

// maxTransientStepRetries bounds step-level retries for transient
// LLM-provider failures. Each retry re-runs the whole subagent step with a
// fresh task identity and a fresh child context. Three retries with the
// 10/30/60s backoff give a flaky provider roughly two minutes to recover
// before the step fails.
const maxTransientStepRetries = 3

// transientProviderMarkers identify retryable LLM-provider transport errors:
// overload/rate limits, upstream 5xx, and prompt-too-long (the latter comes
// from accumulated child context, which a fresh subagent run clears). Real
// agent failures (schema, binding, refusal) do not match and fail
// immediately.
var transientProviderMarkers = []string{
	"HTTP 429", "temporarily overloaded", "rate limited", "overloaded",
	"HTTP 400", "prompt too long",
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
