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

// panelWorkSpecParams names the fields that vary between one panel member's
// work spec and the synthesis work spec. Content storage, fingerprinting,
// and the fixed no-retry/fail-interrupted policy are identical for both and
// live in buildPanelTaskSpec below.
type panelWorkSpecParams struct {
	RunID, TaskID          string
	AgentName, AgentDigest string
	Skill, Provider, Model string
	Input, InputSchema     []byte
	OutputSchema           []byte
	Deadline               time.Time
	Limits                 runtime.WorkLimits
}

// buildPanelTaskSpec stores a panel child's input and schema content, builds
// its PanelTaskSpec and matching coordinator fingerprint, and finalizes the
// work fingerprint. buildPanelAttempt's per-member loop and
// buildPanelSynthesisWork (panel_synthesis.go) both call this; only the
// fields in panelWorkSpecParams differ between a member and the synthesizer.
func (c *LinearController) buildPanelTaskSpec(ctx context.Context, p panelWorkSpecParams) (workflowledger.PanelTaskSpec, error) {
	inputRef, err := c.storePanelContent(ctx, p.Input)
	if err != nil {
		return workflowledger.PanelTaskSpec{}, err
	}
	inputSchemaRef, err := c.storePanelContent(ctx, p.InputSchema)
	if err != nil {
		return workflowledger.PanelTaskSpec{}, err
	}
	outputSchemaRef, err := c.storePanelContent(ctx, p.OutputSchema)
	if err != nil {
		return workflowledger.PanelTaskSpec{}, err
	}
	var inputSchemaValue, outputSchemaValue map[string]any
	if err := json.Unmarshal(p.InputSchema, &inputSchemaValue); err != nil {
		return workflowledger.PanelTaskSpec{}, fmt.Errorf("panel task %q input schema: %w", p.TaskID, err)
	}
	if err := json.Unmarshal(p.OutputSchema, &outputSchemaValue); err != nil {
		return workflowledger.PanelTaskSpec{}, fmt.Errorf("panel task %q output schema: %w", p.TaskID, err)
	}
	limits := p.Limits
	limits.DeadlineAt = p.Deadline
	work := workflowledger.PanelTaskSpec{
		TaskName: p.AgentName, InputRef: inputRef, InputDigest: workflowledger.DigestHex(p.Input),
		InputSchemaRef: inputSchemaRef, InputSchemaDigest: workflowledger.DigestHex(p.InputSchema),
		Budget: 1, Scope: "workflow-panel:" + p.RunID, AgentName: p.AgentName, AgentDigest: p.AgentDigest,
		Skill: p.Skill, Provider: p.Provider, Model: p.Model,
		OutputSchemaRef: outputSchemaRef, OutputSchemaDigest: workflowledger.DigestHex(p.OutputSchema),
		Timeout: p.Deadline.Sub(c.now()), DeadlineAt: p.Deadline, WorkLimits: limits,
		Policy: coordledger.RunPolicy{NoRetry: true, FailInterrupted: true},
	}
	task := subagents.Task{ID: p.TaskID, Name: work.TaskName, Input: p.Input, InputSchema: inputSchemaValue, OutputSchema: outputSchemaValue, Timeout: work.Timeout, Budget: work.Budget, Scope: work.Scope, AgentName: work.AgentName, AgentDigest: work.AgentDigest, Skill: work.Skill, ProviderName: work.Provider, Model: work.Model, WorkLimits: work.WorkLimits, DisableProviderReplay: true}
	fingerprint, err := coordinator.RequestFingerprint([]subagents.Task{task}, work.Policy)
	if err != nil {
		return workflowledger.PanelTaskSpec{}, err
	}
	work.CoordinatorRequestFingerprint = fingerprint
	workflowledger.FinalizePanelTaskSpec(&work)
	return work, nil
}

func (c *LinearController) storePanelContent(ctx context.Context, data []byte) (string, error) {
	ref := "sha256:" + workflowledger.DigestHex(data)
	if err := c.Repo.StoreContent(ctx, ref, data); err != nil {
		return "", err
	}
	return ref, nil
}
