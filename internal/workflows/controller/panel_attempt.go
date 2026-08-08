package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	coordledger "github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/template"
)

const panelMemberTimeout = 30 * time.Minute

var panelMemberLimits = runtime.WorkLimits{
	MaxTurns: 16, MaxPromptTokens: 524288, MaxOutputTokens: 131072,
	MaxOutputPerCall: 8192, MaxToolCalls: 64,
}

// panelSynthesisLimits reserves the fixed future synthesis slice. Wave 4
// persists only member work and does not create a synthesis task.
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
		var schema map[string]any
		if err := json.Unmarshal(schemaRef.Bytes, &schema); err != nil {
			return workflowledger.StepAttempt{}, fmt.Errorf("panel member %q schema: %w", member.ID, err)
		}
		prompt, err := template.Render(string(templateRef.Bytes), inputs, evidence, maxBinding(step), maxStepContextBytes)
		if err != nil {
			return workflowledger.StepAttempt{}, err
		}
		input := mustJSON(prompt)
		inputSchema := []byte(`{"type":"string"}`)
		inputRef, err := c.storePanelContent(ctx, input)
		if err != nil {
			return workflowledger.StepAttempt{}, err
		}
		inputSchemaRef, err := c.storePanelContent(ctx, inputSchema)
		if err != nil {
			return workflowledger.StepAttempt{}, err
		}
		outputSchemaRef, err := c.storePanelContent(ctx, schemaRef.Bytes)
		if err != nil {
			return workflowledger.StepAttempt{}, err
		}
		runID, taskID := workflowledger.PanelChildIDs(c.RunID, attempt.AttemptID, member.ID)
		limits := panelMemberLimits
		limits.DeadlineAt = deadline
		work := workflowledger.PanelTaskSpec{
			TaskName: member.Agent, InputRef: inputRef, InputDigest: workflowledger.DigestHex(input),
			InputSchemaRef: inputSchemaRef, InputSchemaDigest: workflowledger.DigestHex(inputSchema),
			Budget: 1, Scope: "workflow-panel:" + runID, AgentName: binding.AgentName, AgentDigest: binding.AgentDigest,
			Skill: member.Skill, Provider: binding.ProviderName, Model: binding.Model,
			OutputSchemaRef: outputSchemaRef, OutputSchemaDigest: workflowledger.DigestHex(schemaRef.Bytes),
			Timeout: deadline.Sub(c.now()), DeadlineAt: deadline, WorkLimits: limits,
			Policy: coordledger.RunPolicy{NoRetry: true, FailInterrupted: true},
		}
		task := subagents.Task{ID: taskID, Name: work.TaskName, Input: input, InputSchema: map[string]any{"type": "string"}, OutputSchema: schema, Timeout: work.Timeout, Budget: work.Budget, Scope: work.Scope, AgentName: work.AgentName, AgentDigest: work.AgentDigest, Skill: work.Skill, ProviderName: work.Provider, Model: work.Model, WorkLimits: work.WorkLimits, DisableProviderReplay: true}
		fingerprint, err := coordinator.RequestFingerprint([]subagents.Task{task}, work.Policy)
		if err != nil {
			return workflowledger.StepAttempt{}, err
		}
		work.CoordinatorRequestFingerprint = fingerprint
		workflowledger.FinalizePanelTaskSpec(&work)
		members = append(members, workflowledger.PanelMemberExecution{MemberID: member.ID, CoordinatorRunID: runID, TaskID: taskID, Work: work, Order: order})
	}
	synthesisRun, synthesisTask := workflowledger.PanelChildIDs(c.RunID, attempt.AttemptID, "synthesis")
	attempt.PanelExecution = &workflowledger.PanelExecution{Members: members, SynthesisRunID: synthesisRun, SynthesisTaskID: synthesisTask, Phase: workflowledger.PanelPhaseMembersAdmitted}
	return attempt, nil
}

func (c *LinearController) storePanelContent(ctx context.Context, data []byte) (string, error) {
	ref := "sha256:" + workflowledger.DigestHex(data)
	if err := c.Repo.StoreContent(ctx, ref, data); err != nil {
		return "", err
	}
	return ref, nil
}
