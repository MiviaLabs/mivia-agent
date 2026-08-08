package controller

import (
	"context"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

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
