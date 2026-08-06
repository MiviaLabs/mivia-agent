package controller

import (
	"context"
	"errors"
	"fmt"
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
	}
	req := AgentStepRequest{WorkflowRunID: c.RunID, StepID: step.ID, AttemptNo: attempt.AttemptNo, TaskID: attempt.TaskID, CoordinatorRunID: attempt.CoordinatorRunID, AgentName: runtime.Agent.Name, AgentDigest: runtime.Digest, Skill: step.Skill, ProviderName: runtime.ProviderName, Model: runtime.Model, Timeout: timeout, ForceResume: c.forceResume, Template: runtime.Template, Inputs: stepInputs, Evidence: evidence, MaxBindingBytes: maxBinding(step), MaxContextBytes: 32 << 10, OutputSchema: runtime.Schema}
	result, runErr := c.Runner.RunStep(ctx, req)
	status := workflowledger.AttemptStatusSucceeded
	if runErr != nil {
		status = workflowledger.AttemptStatusFailed
		if errors.Is(runErr, context.Canceled) {
			status = workflowledger.AttemptStatusCanceled
		} else if errors.Is(runErr, context.DeadlineExceeded) {
			status = workflowledger.AttemptStatusTimedOut
		}
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
	writeCtx, cancel := stepPersistenceContext(ctx)
	defer cancel()
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

func stepPersistenceContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx.Err() == nil {
		return ctx, func() {}
	}
	return context.WithTimeout(context.Background(), 5*time.Second)
}

func (c *LinearController) failAttempt(ctx context.Context, run workflowledger.RunSnapshot, attempt workflowledger.StepAttempt, cause error) (workflowledger.RunSnapshot, bool, error) {
	writeCtx, cancel := stepPersistenceContext(ctx)
	defer cancel()
	_ = CompleteExistingStepResult(writeCtx, c.Repo, attempt, AgentStepResult{}, workflowledger.AttemptStatusFailed, RouteDecision{})
	return c.fail(writeCtx, run, cause)
}
