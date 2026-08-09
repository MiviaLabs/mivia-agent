package controller

import (
	"context"
	"errors"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// ErrPanelMembersComplete reports that Wave 4 completed member work but Wave
// 5 synthesis is not available to finish the workflow step.
var ErrPanelMembersComplete = errors.New("panel members completed; synthesis is unavailable")

// panelsEnabled gates agent_panel execution. Wave 5 synthesis has no agent or
// template definition surface, so the controller fails panel steps closed:
// running members without synthesis left the attempt running forever (G9).
// The member-running code below stays dead until Wave 5 defines synthesis.
const panelsEnabled = false

func (c *LinearController) advancePanelStep(ctx context.Context, run workflowledger.RunSnapshot, step definition.Step) (workflowledger.RunSnapshot, bool, error) {
	attempts, err := c.Repo.ListStepAttempts(ctx, c.RunID)
	if err != nil {
		return run, false, err
	}
	var attempt workflowledger.StepAttempt
	for _, existing := range attempts {
		if existing.StepID == step.ID && existing.PanelExecution != nil && !workflowledger.IsTerminalAttemptStatus(existing.Status) {
			attempt = existing
			break
		}
	}
	if attempt.AttemptID == "" {
		attempt, err = c.buildPanelAttempt(ctx, run, step, attempts)
		if err != nil {
			return c.fail(ctx, run, err)
		}
		if err := c.Repo.CreateStepAttempt(ctx, attempt); err != nil {
			return c.fail(ctx, run, err)
		}
		// Re-read the stored attempt: CreateStepAttempt records version 1 and
		// the settle CAS below compares against that version.
		stored, getErr := c.Repo.GetStepAttempt(ctx, c.RunID, attempt.AttemptID)
		if getErr != nil {
			return c.fail(ctx, run, getErr)
		}
		attempt = stored
	}
	if !panelsEnabled {
		return c.refusePanelStep(ctx, run, step, attempt)
	}
	runner, ok := c.Runner.(*CoordinatorRunner)
	if !ok || runner.Coordinator == nil {
		return c.failAttempt(ctx, run, attempt, fmt.Errorf("panel step runner has no coordinator"))
	}
	panel := workflowledger.NewPanelCoordinator(c.RunID, runner.Coordinator, c.Repo)
	members := make([]PanelMemberRequest, len(attempt.PanelExecution.Members))
	for i, member := range attempt.PanelExecution.Members {
		members[i] = PanelMemberRequest{MemberID: member.MemberID, RunID: member.CoordinatorRunID}
	}
	_, runErr := RunPanelMembers(ctx, c.PanelLimiter, PanelMembersRequest{AttemptID: attempt.AttemptID, Members: members, Coordinator: panel})
	if runErr != nil {
		return c.settleAgentAttempt(ctx, run, step, attempt, AgentStepResult{Status: "failed"}, runErr)
	}
	// Wave 4 completes only the member phase. Wave 5 persists and executes
	// synthesis. Keep this attempt nonterminal so a successful panel does not
	// take the failure route before that phase exists.
	return run, false, nil
}

// refusePanelStep fails the panel attempt and its run closed with a durable
// refusal cause. The attempt is settled failed exactly like the member-failure
// path, so settleAgentAttempt persists the cause on the attempt ErrorRef. The
// ProgressPanelRefused event carries the same cause.
func (c *LinearController) refusePanelStep(ctx context.Context, run workflowledger.RunSnapshot, step definition.Step, attempt workflowledger.StepAttempt) (workflowledger.RunSnapshot, bool, error) {
	cause := fmt.Sprintf("agent_panel step %q is not supported (Wave 5 synthesis unavailable)", step.ID)
	result := AgentStepResult{Status: "failed"}
	runErr := errors.New(cause)
	snapshot, done, settleErr := c.settleAgentAttempt(ctx, run, step, attempt, result, runErr)
	c.emitProgress(ProgressEvent{
		Kind: ProgressPanelRefused, StepID: step.ID, AttemptNo: attempt.AttemptNo, Detail: cause,
	})
	return snapshot, done, settleErr
}
