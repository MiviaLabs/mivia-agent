package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/verifier"
)

// The evidence-gate drive: admit an attempt, run the host verifier, and settle
// the success or failure (route or host-failure repair). Kept separate from
// linear_gates.go so the evidence-gate drive and the human-gate admission stay
// independently readable, matching the go-structure file-size policy.

func (c *LinearController) advanceEvidenceGate(ctx context.Context, run workflowledger.RunSnapshot, step definition.Step) (workflowledger.RunSnapshot, bool, error) {
	profile, err := c.verifierProfile(step)
	if err != nil {
		return c.fail(ctx, run, err)
	}
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
	// Report the gate start before the host verifier runs. The detail is the
	// verifier name, or the step kind when the gate uses a command profile.
	gateDetail := step.Verifier
	if gateDetail == "" {
		gateDetail = step.Kind
	}
	result, verifyErr := c.runGateVerify(ctx, step, attempt, gateDetail, profile)
	status := workflowledger.AttemptStatusSucceeded
	if verifyErr != nil || result.Status == "failed" {
		status = workflowledger.AttemptStatusFailed
		result.Status = "failed"
		if verifyErr == nil {
			verifyErr = fmt.Errorf("verifier %q reported failed checks", step.Verifier)
		}
	}
	// A caller deadline or cancel is a run timeout, not a verifier host failure.
	if errors.Is(verifyErr, context.DeadlineExceeded) || errors.Is(verifyErr, context.Canceled) {
		writeCtx, cancel := stepPersistenceContext(ctx)
		defer cancel()
		// Settle the admitted attempt as timed_out before the run reaches a
		// terminal state, mirroring timeoutOpenHumanAttempt and the agent-step
		// settle path: a terminal run must never leave an attempt Running. The
		// deadline cause is persisted so the CLI can explain the timeout.
		if err := CompleteExistingStepResult(writeCtx, c.Repo, attempt, AgentStepResult{ErrorRef: storeErrorText(writeCtx, c.Repo, verifyErr)}, workflowledger.AttemptStatusTimedOut, RouteDecision{}); err != nil {
			return c.fail(writeCtx, run, err)
		}
		c.emitStepCompleted(step, attempt, string(workflowledger.AttemptStatusTimedOut))
		return c.failWithStatus(writeCtx, run, context.DeadlineExceeded, workflowledger.RunStatusTimedOut)
	}
	output, err := json.Marshal(result)
	if err != nil {
		return c.failAttempt(ctx, run, attempt, err)
	}
	outputMap := map[string]any{
		"status": result.Status,
		"checks": checksAsAny(result.Checks),
	}
	if status == workflowledger.AttemptStatusFailed {
		return c.routeEvidenceFailure(ctx, run, attempt, step, result, output, outputMap)
	}
	writeCtx, cancel := stepPersistenceContext(ctx)
	defer cancel()
	route := RouteDecision{}
	route, err = c.selectRoute(writeCtx, step, status, outputMap)
	if err != nil {
		if route.ToStepID == "" {
			route = failureRoute(step)
		}
		_ = CompleteExistingStepResult(writeCtx, c.Repo, attempt, AgentStepResult{Output: output, ErrorRef: storeErrorText(writeCtx, c.Repo, err)}, workflowledger.AttemptStatusFailed, route)
		c.emitStepCompleted(step, attempt, string(workflowledger.AttemptStatusFailed))
		return c.fail(writeCtx, run, err)
	}
	routeErr := c.completeSucceededRoute(writeCtx, attempt, AgentStepResult{Output: output}, route)
	if routeErr == nil || isLoopAccountError(routeErr) {
		// The attempt is durably succeeded; a loop under-count continues.
		c.emitStepCompleted(step, attempt, string(workflowledger.AttemptStatusSucceeded))
	}
	if routeErr != nil && !isLoopAccountError(routeErr) {
		return c.fail(writeCtx, run, routeErr)
	}
	return settleAfterRoute(ctx, c, run, route)
}

// runGateVerify reports the gate start, keeps the durable heartbeat trail
// alive while the SYNCHRONOUS host verifier runs, and runs the profile. A
// long-running gate stays observable in the durable ledger even though it
// dispatches no coordinator child: the ticker is stopped on every return path
// (defer) and exits when the step context is canceled, and its writes are
// throttled + best-effort, so a ledger write error can never fail the gate.
func (c *LinearController) runGateVerify(ctx context.Context, step definition.Step, attempt workflowledger.StepAttempt, gateDetail string, profile verifier.Profile) (verifier.Result, error) {
	c.emitProgress(ProgressEvent{
		Kind: ProgressGateStarted, StepID: step.ID, AttemptNo: attempt.AttemptNo, Detail: gateDetail,
	})
	stopGateHeartbeats := c.startDurableHeartbeatTicker(ctx, attempt.AttemptID)
	defer stopGateHeartbeats()
	return profile.Verify(ctx, verifier.Request{
		WorkDir:        c.WorkDir,
		StepID:         step.ID,
		RunID:          c.RunID,
		ModuleBaseline: c.ModuleBaseline,
	})
}

// verifierProfile resolves the evidence gate's verifier: a named catalogue
// profile, or a sandboxed command profile built from the step declaration.
func (c *LinearController) verifierProfile(step definition.Step) (verifier.Profile, error) {
	if step.Command != nil {
		return verifier.NewCommandProfile(step.Command.Check, step.Command.Program, step.Command.Args, c.SecretPolicy)
	}
	if c.Verifiers == nil {
		return nil, fmt.Errorf("step %q requires a verifier catalogue", step.ID)
	}
	return c.Verifiers.Lookup(step.Verifier)
}

func (c *LinearController) routeEvidenceFailure(ctx context.Context, run workflowledger.RunSnapshot, attempt workflowledger.StepAttempt, step definition.Step, result verifier.Result, output []byte, outputMap map[string]any) (workflowledger.RunSnapshot, bool, error) {
	writeCtx, cancel := stepPersistenceContext(ctx)
	defer cancel()
	if !result.Repairable() {
		return c.settleHostFailure(writeCtx, run, attempt, step, output)
	}
	route, err := c.selectEvidenceFailureRoute(ctx, step, outputMap)
	if err != nil {
		if completeErr := CompleteExistingStepResult(writeCtx, c.Repo, attempt, AgentStepResult{Output: output, ErrorRef: storeErrorText(writeCtx, c.Repo, err)}, workflowledger.AttemptStatusFailed, route); completeErr != nil {
			return c.fail(writeCtx, run, completeErr)
		}
		c.emitStepCompleted(step, attempt, string(workflowledger.AttemptStatusFailed))
		return c.fail(writeCtx, run, err)
	}
	if err := CompleteExistingStepResult(writeCtx, c.Repo, attempt, AgentStepResult{Output: output}, workflowledger.AttemptStatusFailed, route); err != nil {
		return c.fail(writeCtx, run, err)
	}
	// The failed attempt is durable. Report the completion once with the
	// failed status before the run advances to the repair route.
	c.emitStepCompleted(step, attempt, string(workflowledger.AttemptStatusFailed))
	// Increment the loop counter after the repair route is durable. A failed
	// increment under-counts (one extra allowed iteration) rather than blocking
	// repair. This mirrors the crash-after-complete policy in
	// completeSucceededRoute. Without this call, checkLoopCap always reads
	// zero for evidence-gate repair routes and a finite max_iterations cap is
	// a no-op.
	if err := c.recordLoopAfterComplete(writeCtx, route); err != nil {
		// Loop counter write failed (e.g. abandon fence) but the route is
		// durable; under-count and continue rather than fail the run.
	}
	return settleAfterRoute(ctx, c, run, route)
}

func checksAsAny(checks []verifier.Check) []any {
	out := make([]any, len(checks))
	for i, c := range checks {
		item := map[string]any{"name": c.Name, "status": c.Status}
		if c.Class != "" {
			item["class"] = c.Class
		}
		if c.Detail != "" {
			item["detail"] = c.Detail
		}
		out[i] = item
	}
	return out
}
