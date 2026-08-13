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

// panelMemberDeadlineDefault bounds one panel member attempt's wall clock when
// the workflow declares no run deadline (max_duration_seconds = 0). The
// workflow's declared deadline is the real contract and always wins when
// earlier; this default only fills the gap so the durable PanelTaskSpec keeps
// its fail-closed non-zero DeadlineAt invariant. Long multi-hour agentic
// reviews are the intended workload (bug-fix.toml documents "24h+ agentic
// reviews" with max_duration_seconds = 0), so the default is generous;
// workflow authors tighten it by declaring max_duration_seconds.
const panelMemberDeadlineDefault = 24 * time.Hour

// panelMemberLimits bounds each panel member child's work. MaxTurns is
// deliberately 0 (unlimited) here: the turn bound is a per-step workflow knob
// (definition.Step.MaxTurns, default 0 = unlimited) applied at build time in
// buildPanelAttempt. A hardcoded MaxTurns (historically 16) killed deep
// read-only reviews of large packages mid-panel with "agent exceeded
// max_steps (16)" — the same bogus bound class as the prompt-token and
// output-token caps below. MaxPromptTokens is also deliberately 0 (unlimited
// cumulative prompt, per runtime.WorkLimits semantics): a read-only reviewer's
// prompt volume is not a work bound. Every provider call is already bounded by
// the model context window with a prompt-too-long compaction retry.
// MaxOutputTokens is also 0 (unlimited cumulative output). A finite cumulative
// output cap with ceiling-charged accounting (work_limits.go charges each call
// its full per-call ceiling, refunded only on a steer-canceled call) gives a
// deep read-only review at most MaxOutputTokens/MaxOutputPerCall provider
// calls — for the historical 131072/8192 pair, exactly 16 — before "work limit
// exceeded: output tokens" kills the member mid-panel, deterministically and
// irretrievably (observed on live bug-fix runs: attempts 1 and 2 failed
// identically). This is the same bogus bound class as the prompt-token cap
// removed above. The member loop stays bounded by MaxOutputPerCall,
// MaxToolCalls, the attempt deadline, and the panel's retry policy.
var panelMemberLimits = runtime.WorkLimits{
	MaxTurns: 0, MaxPromptTokens: 0, MaxOutputTokens: 0,
	MaxOutputPerCall: 8192, MaxToolCalls: 64,
}

// panelSynthesisLimits bounds the synthesis child's work. buildPanelSynthesisWork
// (panel_synthesis.go) consumes these once member work succeeds and it builds
// the actual synthesis PanelTaskSpec. MaxTurns is deliberately 0 (unlimited)
// here for the same reason as panelMemberLimits: the per-step max_turns knob
// is applied at build time, so the hardcoded synthesis cap (historically 8)
// is gone. MaxPromptTokens is 0 (unlimited) and MaxOutputTokens is 0
// (unlimited cumulative output) for the same reason as panelMemberLimits; the
// synthesis child is still bounded by MaxOutputPerCall, MaxToolCalls, and its
// deadline.
var panelSynthesisLimits = runtime.WorkLimits{
	MaxTurns: 0, MaxPromptTokens: 0, MaxOutputTokens: 0,
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
	deadline := c.now().Add(panelMemberDeadlineDefault)
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
		memberLimits := panelMemberLimits
		memberLimits.MaxTurns = step.MaxTurns // 0 = unlimited (default)
		work, err := c.buildPanelTaskSpec(ctx, panelWorkSpecParams{
			RunID: runID, TaskID: taskID, AgentName: binding.AgentName, AgentDigest: binding.AgentDigest,
			Skill: member.Skill, Provider: binding.ProviderName, Model: binding.Model,
			Input: input, InputSchema: []byte(`{"type":"string"}`), OutputSchema: schemaRef.Bytes,
			Deadline: deadline, Limits: memberLimits,
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
