package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/textutil"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// hostFailureRepairable's re-entry budget is the workflow-configurable
// max_on_failure_reentries limit (onFailureReentryLimit), shared with
// agentFailureRepairable so every repair re-entry in a run is bounded by one
// knob.
//
// The two caps that bound other re-entries do not reach this one.
// enforceGlobalAttemptCap does nothing when a workflow leaves
// max_step_attempts unset, which is legal, and checkLoopCap fires only for a
// named back-edge while this route carries no loop. Without the budget, a
// workflow with no limits and a permanently broken host would repair forever.

// maxHostFailureCauseBytes bounds how much of the verifier report is carried
// into the step error: enough to say why the sandbox failed, never the whole
// report (the failed_evidence binding already carries the full report).
const maxHostFailureCauseBytes = 1024

// hostFailureCause extracts a bounded, redacted cause from a gate's host
// failure report: the first host-class check's detail, which now carries the
// underlying error (missing bubblewrap, module baseline, git worktree init,
// sandbox command stderr). Falling back to a bounded snippet of the raw
// report keeps the step error actionable even for an unparseable report.
func hostFailureCause(output []byte) string {
	var report struct {
		Checks []struct {
			Class  string `json:"class"`
			Detail string `json:"detail"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(output, &report); err == nil {
		for _, check := range report.Checks {
			if check.Class == "host" && strings.TrimSpace(check.Detail) != "" {
				return textutil.TruncateRuneSafe(redact.Text(check.Detail), maxHostFailureCauseBytes)
			}
		}
	}
	trimmed := strings.TrimSpace(redact.Text(strings.ToValidUTF8(string(output), "\uFFFD")))
	if trimmed == "" {
		return ""
	}
	return textutil.TruncateRuneSafe(trimmed, maxHostFailureCauseBytes)
}

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
	if cause := hostFailureCause(output); cause != "" {
		hostErr = fmt.Errorf("verifier %q has a host failure: %s", step.Verifier, cause)
	}
	result := AgentStepResult{Output: output, ErrorRef: storeErrorText(ctx, c.Repo, hostErr)}
	// Decide the re-entry BEFORE persisting, like settleAgentAttempt: the
	// attempt being settled is still Running in the ledger, so the budget check
	// must not see its own completion. When the budget is spent, record the
	// TERMINAL failure route so the ledger derives a terminal step — never an
	// un-honored repair target that a crash between this persist and the status
	// CAS could resume into — then fail the run as before.
	repairable := c.hostFailureRepairable(ctx, step, route)
	if !repairable && !workflowledger.IsTerminalStepID(route.ToStepID) {
		route.ToStepID = "failure"
	}
	if err := CompleteExistingStepResult(ctx, c.Repo, attempt, result, workflowledger.AttemptStatusFailed, route); err != nil {
		return c.fail(ctx, run, err)
	}
	// The failed attempt is durable. Report the completion once with the
	// failed status before deciding where the run goes next.
	c.emitStepCompleted(step, attempt, string(workflowledger.AttemptStatusFailed))
	if !repairable {
		return c.fail(ctx, run, hostErr)
	}
	return settleAfterRoute(ctx, c, run, route)
}

// settleBlockedGateFailure records a gate failure that names one or more
// write-blocklisted paths and fails the run immediately: unlike
// settleHostFailure, this is never repairable, because no workflow agent can
// satisfy the demand regardless of what step.OnFailure names. blockedCause
// (used post-hoc, after a repair agent's own output admits the same thing)
// documents the same host-policy boundary this pre-checks before the repair
// step is ever dispatched.
func (c *LinearController) settleBlockedGateFailure(ctx context.Context, run workflowledger.RunSnapshot, attempt workflowledger.StepAttempt, step definition.Step, output []byte, blocked []string) (workflowledger.RunSnapshot, bool, error) {
	blockedErr := fmt.Errorf("workflow blocked: gate %q failure names write path(s) %s, write-blocklisted for workflow agents (host policy); the run cannot proceed - route this change through the root session or a host-owned process", step.ID, strings.Join(blocked, ", "))
	result := AgentStepResult{Output: output, ErrorRef: storeErrorText(ctx, c.Repo, blockedErr)}
	if err := CompleteExistingStepResult(ctx, c.Repo, attempt, result, workflowledger.AttemptStatusFailed, RouteDecision{ToStepID: "failure"}); err != nil {
		return c.fail(ctx, run, err)
	}
	c.emitStepCompleted(step, attempt, string(workflowledger.AttemptStatusFailed))
	return c.fail(ctx, run, blockedErr)
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
	// The attempt being settled is still Running in the ledger (not yet in its
	// Failed set) when this runs before CompleteExistingStepResult, so it is
	// counted here as spent, exactly as agentFailureRepairable counts the
	// still-Running attempt; the budget total is unchanged.
	spent := 1
	for _, a := range attempts {
		if a.StepID == step.ID && a.Status == workflowledger.AttemptStatusFailed {
			spent++
		}
	}
	return spent < c.onFailureReentryLimit()
}
