package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// PanelLimits is the resolved, non-pointer set of tunables
// buildPanelAttempt/buildPanelSynthesisWork apply to every agent_panel
// step's member and synthesis children. It replaces what were once
// hardcoded package vars/consts so a host config file can override them
// (internal/config's WorkflowsConfig.Panels, resolved by the caller
// before SetPanelLimits); DefaultPanelLimits is what an unconfigured
// host still gets, byte-identical to the pre-config-driven values.
//
// MaxTurns for both member and synthesis is deliberately NOT part of
// this struct: the turn bound is a per-step workflow knob
// (definition.Step.MaxTurns, default 0 = unlimited) applied at build
// time in buildPanelAttempt/buildPanelSynthesisWork, not a host-wide
// default. MaxPromptTokens/MaxOutputTokens are deliberately not part
// of this struct either and stay 0 (unlimited cumulative), per
// runtime.WorkLimits semantics: a read-only reviewer's prompt/output
// volume is not a work bound a host config should need to raise. A
// finite cumulative output cap with ceiling-charged accounting
// (work_limits.go charges each call its full per-call ceiling,
// refunded only on a steer-canceled call) previously killed deep
// read-only reviews mid-panel with "work limit exceeded: output
// tokens" (observed on live bug-fix runs: attempts 1 and 2 failed
// identically) - the same bogus bound class MaxTurns used to be. The
// member/synthesis loop stays bounded by MaxOutputPerCall,
// MaxToolCalls, the attempt deadline, and the panel's retry policy -
// exactly the fields below.
type PanelLimits struct {
	MemberMaxOutputPerCall    int
	MemberMaxToolCalls        int
	SynthesisMaxOutputPerCall int
	SynthesisMaxToolCalls     int
	// MemberDeadlineDefault bounds one panel member attempt's wall
	// clock when the workflow declares no run deadline
	// (max_duration_seconds = 0). The workflow's declared deadline is
	// the real contract and always wins when earlier; this default
	// only fills the gap so the durable PanelTaskSpec keeps its
	// fail-closed non-zero DeadlineAt invariant. Long multi-hour
	// agentic reviews are the intended workload (bug-fix.toml
	// documents "24h+ agentic reviews" with max_duration_seconds = 0),
	// so the default is generous; workflow authors tighten it by
	// declaring max_duration_seconds, or a host lowers the default via
	// config.
	MemberDeadlineDefault time.Duration
}

// DefaultPanelLimits returns the compiled defaults every panel step ran
// under before PanelLimits became config-driven: 8192 output tokens per
// call for both member and synthesis children, 64 cumulative tool calls
// for members, 16 for synthesis, and a 24h member deadline default.
func DefaultPanelLimits() PanelLimits {
	return PanelLimits{
		MemberMaxOutputPerCall:    8192,
		MemberMaxToolCalls:        64,
		SynthesisMaxOutputPerCall: 8192,
		SynthesisMaxToolCalls:     16,
		MemberDeadlineDefault:     24 * time.Hour,
	}
}

func (c *LinearController) buildPanelAttempt(ctx context.Context, run workflowledger.RunSnapshot, step definition.Step, attempts []workflowledger.StepAttempt) (workflowledger.StepAttempt, error) {
	policy := ""
	if step.Panel != nil {
		policy = step.Panel.FailurePolicy
	}
	if policy != "require_all" && policy != "allow_partial" {
		return workflowledger.StepAttempt{}, fmt.Errorf("panel step %q has unsupported failure_policy %q", step.ID, policy)
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
	deadline := c.now().Add(c.PanelLimits.MemberDeadlineDefault)
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
		templateRef, ok := snapshot.Templates[member.Template]
		if !ok {
			return workflowledger.StepAttempt{}, fmt.Errorf("panel template %q is missing", member.Template)
		}
		schemaRef, ok := snapshot.Schemas[member.OutputSchema]
		if !ok {
			return workflowledger.StepAttempt{}, fmt.Errorf("panel schema %q is missing", member.OutputSchema)
		}
		prompt, err := delivery.Render(string(templateRef.Bytes), inputs, evidence, maxBinding(step), maxStepContextBytes)
		if err != nil {
			return workflowledger.StepAttempt{}, err
		}
		input := mustJSON(prompt)
		runID, taskID := workflowledger.PanelChildIDs(c.RunID, attempt.AttemptID, member.ID)
		memberLimits := runtime.WorkLimits{
			MaxTurns:         step.MaxTurns, // 0 = unlimited (default)
			MaxOutputPerCall: c.PanelLimits.MemberMaxOutputPerCall,
			MaxToolCalls:     c.PanelLimits.MemberMaxToolCalls,
		}
		work, err := c.buildPanelTaskSpec(ctx, panelWorkSpecParams{
			RunID: runID, TaskID: taskID, AgentName: binding.AgentName, AgentDigest: binding.AgentDigest,
			Skill: member.Skill, Provider: binding.ProviderName, Model: binding.Model,
			Input: input, InputSchema: []byte(`{"type":"string"}`), OutputSchema: schemaRef.Bytes,
			Deadline: deadline, Limits: memberLimits, WorkflowRunID: c.RunID,
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
