package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/template"
)

const panelMemberTimeout = 30 * time.Minute

var panelMemberLimits = runtime.WorkLimits{
	MaxTurns: 16, MaxPromptTokens: 524288, MaxOutputTokens: 131072,
	MaxOutputPerCall: 8192, MaxToolCalls: 64,
}

// panelSynthesisLimits bounds the synthesis child's work. buildPanelSynthesisWork
// (panel_synthesis.go) consumes these once member work succeeds and it builds
// the actual synthesis PanelTaskSpec.
var panelSynthesisLimits = runtime.WorkLimits{
	MaxTurns: 8, MaxPromptTokens: 524288, MaxOutputTokens: 65536,
	MaxOutputPerCall: 8192, MaxToolCalls: 16,
}

func (c *LinearController) buildPanelAttempt(ctx context.Context, run workflowledger.RunSnapshot, step definition.Step, attempts []workflowledger.StepAttempt) (workflowledger.StepAttempt, error) {
	if step.Panel == nil || step.Panel.FailurePolicy != "require_all" {
		return workflowledger.StepAttempt{}, fmt.Errorf("panel step %q must use require_all", step.ID)
	}
	snapshot, err := workflowledger.UnmarshalSnapshot(c.Snapshot)
	if err != nil {
		return workflowledger.StepAttempt{}, err
	}
	inputs, evidence, _, err := c.contextForStep(ctx, step, attempts)
	if err != nil {
		return workflowledger.StepAttempt{}, err
	}
	if err := validateBindingLimits(step, c.Inputs, evidence); err != nil {
		return workflowledger.StepAttempt{}, err
	}
	if err := c.enforceGlobalAttemptCap(attempts); err != nil {
		return workflowledger.StepAttempt{}, err
	}
	attempt := c.newAttempt(step.ID, nextAttemptNo(attempts, step.ID))
	deadline := c.now().Add(panelMemberTimeout)
	if run.DeadlineAt != nil && run.DeadlineAt.Before(deadline) {
		deadline = *run.DeadlineAt
	}
	if !deadline.After(c.now()) {
		return workflowledger.StepAttempt{}, context.DeadlineExceeded
	}
	members := make([]workflowledger.PanelMemberExecution, 0, len(step.Panel.Members))
	for order, member := range step.Panel.Members {
		binding, ok := snapshot.PanelBindings[step.ID+"/"+member.ID]
		if !ok {
			return workflowledger.StepAttempt{}, fmt.Errorf("panel binding %q is missing", step.ID+"/"+member.ID)
		}
		templateRef, schemaRef := snapshot.Templates[member.Template], snapshot.Schemas[member.OutputSchema]
		prompt, err := template.Render(string(templateRef.Bytes), inputs, evidence, maxBinding(step), maxStepContextBytes)
		if err != nil {
			return workflowledger.StepAttempt{}, err
		}
		input := mustJSON(prompt)
		runID, taskID := workflowledger.PanelChildIDs(c.RunID, attempt.AttemptID, member.ID)
		work, err := c.buildPanelTaskSpec(ctx, panelWorkSpecParams{
			RunID: runID, TaskID: taskID, AgentName: binding.AgentName, AgentDigest: binding.AgentDigest,
			Skill: member.Skill, Provider: binding.ProviderName, Model: binding.Model,
			Input: input, InputSchema: []byte(`{"type":"string"}`), OutputSchema: schemaRef.Bytes,
			Deadline: deadline, Limits: panelMemberLimits,
		})
		if err != nil {
			return workflowledger.StepAttempt{}, fmt.Errorf("panel member %q: %w", member.ID, err)
		}
		members = append(members, workflowledger.PanelMemberExecution{MemberID: member.ID, CoordinatorRunID: runID, TaskID: taskID, Work: work, Order: order})
	}
	synthesisRun, synthesisTask := workflowledger.PanelChildIDs(c.RunID, attempt.AttemptID, "synthesis")
	attempt.PanelExecution = &workflowledger.PanelExecution{Members: members, SynthesisRunID: synthesisRun, SynthesisTaskID: synthesisTask, Phase: workflowledger.PanelPhaseMembersAdmitted}
	return attempt, nil
}
