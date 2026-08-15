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
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// panelWorkSpecParams names the fields that vary between one panel member's
// work spec and the synthesis work spec. Content storage, fingerprinting,
// and the fixed no-retry/fail-interrupted policy (D12: these are fixed
// agent_panel host policies, not per-call choices) are identical for both
// and live in buildPanelTaskSpec below. buildPanelAttempt's per-member loop
// (panel_attempt.go) and buildPanelSynthesisWork (panel_synthesis.go) both
// build one of these and call buildPanelTaskSpec; this file holds the
// plumbing neither phase owns exclusively.
type panelWorkSpecParams struct {
	// RunID is the per-child coordinator run id (workflowledger.PanelChildIDs),
	// unique to this member or the synthesis step - used for Scope, never for
	// SessionID (see WorkflowRunID).
	RunID, TaskID          string
	AgentName, AgentDigest string
	Skill, Provider, Model string
	Input, InputSchema     []byte
	OutputSchema           []byte
	Deadline               time.Time
	Limits                 runtime.WorkLimits
	// WorkflowRunID is the parent workflow run's own id (LinearController.
	// RunID) - the same value PanelCoordinator.workflowRunID carries at
	// dispatch time. It must match exactly, or the fingerprint computed here
	// diverges from the one PanelCoordinator.request recomputes at dispatch
	// and every panel member/synthesis call fails closed with ErrConflict.
	WorkflowRunID string
}

// buildPanelTaskSpec builds one panel child's PanelTaskSpec and matching
// coordinator fingerprint, then stores its input and schema content and
// finalizes the work fingerprint. Schema validation runs before any content
// store call, so a malformed schema fails fast with no repository write.
func (c *LinearController) buildPanelTaskSpec(ctx context.Context, p panelWorkSpecParams) (workflowledger.PanelTaskSpec, error) {
	var inputSchemaValue, outputSchemaValue map[string]any
	if err := json.Unmarshal(p.InputSchema, &inputSchemaValue); err != nil {
		return workflowledger.PanelTaskSpec{}, fmt.Errorf("panel task %q input schema: %w", p.TaskID, err)
	}
	if err := json.Unmarshal(p.OutputSchema, &outputSchemaValue); err != nil {
		return workflowledger.PanelTaskSpec{}, fmt.Errorf("panel task %q output schema: %w", p.TaskID, err)
	}
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
	task := subagents.Task{ID: p.TaskID, Name: work.TaskName, Input: p.Input, InputSchema: inputSchemaValue, OutputSchema: outputSchemaValue, Timeout: work.Timeout, Budget: work.Budget, Scope: work.Scope, AgentName: work.AgentName, AgentDigest: work.AgentDigest, Skill: work.Skill, ProviderName: work.Provider, Model: work.Model, WorkLimits: work.WorkLimits, DisableProviderReplay: true, SessionID: p.WorkflowRunID}
	fingerprint, err := coordinator.RequestFingerprint([]subagents.Task{task}, work.Policy)
	if err != nil {
		return workflowledger.PanelTaskSpec{}, err
	}
	work.CoordinatorRequestFingerprint = fingerprint
	workflowledger.FinalizePanelTaskSpec(&work)
	return work, nil
}

// storePanelContent stores data content-addressed and returns its ref.
func (c *LinearController) storePanelContent(ctx context.Context, data []byte) (string, error) {
	ref := "sha256:" + workflowledger.DigestHex(data)
	if err := c.Repo.StoreContent(ctx, ref, data); err != nil {
		return "", err
	}
	return ref, nil
}
