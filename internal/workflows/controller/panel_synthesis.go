package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// PanelSynthesisOutput is the decoded review-panel-v1.json synthesizer
// output: a disposition for every canonical source key, plus a bounded
// summary. It never carries a verdict field: the host computes the verdict
// (ComputeHostVerdict) and the model cannot override it (D10).
type PanelSynthesisOutput struct {
	Dispositions []PanelSourceDisposition `json:"dispositions"`
	Summary      string                   `json:"summary"`
}

const maxSynthesisSummaryRunes = 4000

// PanelFinalReport is the host-assembled final gate result. HostVerdict is
// computed by ComputeHostVerdict from the bounded member reports; the
// synthesizer's own output can never change it.
type PanelFinalReport struct {
	HostVerdict  string                   `json:"host_verdict"`
	Dispositions []PanelSourceDisposition `json:"dispositions"`
	Summary      string                   `json:"summary"`
}

// DecodeStrictPanelSynthesisOutput strictly decodes the synthesizer's
// review-panel-v1.json output: it applies the same duplicate-key, size-bound,
// and unknown-field-skipping defenses as DecodeStrictPanelMemberReport, then
// requires every disposition to reference a real canonical source key exactly
// once with a legal value (ValidateSourceDispositions).
func DecodeStrictPanelSynthesisOutput(raw []byte, keys []CanonicalSourceKey) (PanelSynthesisOutput, error) {
	if len(raw) == 0 {
		return PanelSynthesisOutput{}, fmt.Errorf("panel synthesis output is empty")
	}
	if len(raw) > maxFinalSynthesisOutputBytes {
		return PanelSynthesisOutput{}, fmt.Errorf("panel synthesis output is %d bytes, exceeds %d byte bound", len(raw), maxFinalSynthesisOutputBytes)
	}
	if err := checkNoDuplicateJSONKeys(raw); err != nil {
		return PanelSynthesisOutput{}, fmt.Errorf("panel synthesis output: %w", err)
	}
	// Unknown fields are skipped (same policy as member reports) so the
	// synthesizer's output cannot fail on a stray extra field.
	dec := json.NewDecoder(bytes.NewReader(raw))
	var out PanelSynthesisOutput
	if err := dec.Decode(&out); err != nil {
		return PanelSynthesisOutput{}, fmt.Errorf("panel synthesis output: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return PanelSynthesisOutput{}, fmt.Errorf("panel synthesis output: trailing content after JSON value")
	}
	if len([]rune(out.Summary)) > maxSynthesisSummaryRunes {
		return PanelSynthesisOutput{}, fmt.Errorf("panel synthesis summary exceeds %d character bound", maxSynthesisSummaryRunes)
	}
	if err := ValidateSourceDispositions(keys, out.Dispositions); err != nil {
		return PanelSynthesisOutput{}, fmt.Errorf("panel synthesis dispositions: %w", err)
	}
	return out, nil
}

// AllCanonicalSourceKeys derives every canonical source key from an
// envelope's decoded member reports, in declaration order. This is what
// ValidateSourceDispositions checks the synthesizer's output against.
func AllCanonicalSourceKeys(envelope PanelSynthesisEnvelope) []CanonicalSourceKey {
	var keys []CanonicalSourceKey
	for _, m := range envelope.Members {
		keys = append(keys, canonicalSourceKeys(m.Provenance.MemberID, m.Report)...)
	}
	return keys
}

// buildPanelSynthesisWork builds the exact, deterministic PanelTaskSpec for
// one panel step's synthesis child (D12). The synthesis prompt cannot exist
// until every member's output exists, so this always runs after
// RunPanelMembers succeeds, never at initial attempt admission.
func (c *LinearController) buildPanelSynthesisWork(ctx context.Context, run workflowledger.RunSnapshot, step definition.Step, attempt workflowledger.StepAttempt, envelope []byte) (workflowledger.PanelTaskSpec, error) {
	if step.Panel == nil || attempt.PanelExecution == nil || len(attempt.PanelExecution.Members) == 0 {
		return workflowledger.PanelTaskSpec{}, fmt.Errorf("panel step %q has no admitted member work", step.ID)
	}
	// The compiler accepts an empty step.Skill for backward compatibility with
	// workflows admitted before skill bindings existed (ValidateAgentSkillReferences
	// treats "" as "no skill declared", not an error). PanelTaskSpec.Validate,
	// deeper in the ledger, unconditionally rejects an empty Skill. Without this
	// check, a panel step that legitimately compiles with no declared skill would
	// admit its members, then fail the synthesis phase transition every time with
	// an opaque ErrInvalidTransition far from its real cause. Fail here instead,
	// with a cause that names the actual problem.
	if step.Skill == "" {
		return workflowledger.PanelTaskSpec{}, fmt.Errorf("panel step %q requires a skill for synthesis dispatch", step.ID)
	}
	snapshot, err := workflowledger.UnmarshalSnapshot(c.Snapshot)
	if err != nil {
		return workflowledger.PanelTaskSpec{}, err
	}
	binding, ok := snapshot.PanelBindings[step.ID+"/synthesis"]
	if !ok {
		return workflowledger.PanelTaskSpec{}, fmt.Errorf("panel synthesis binding %q is missing", step.ID+"/synthesis")
	}
	schemaRef := snapshot.Schemas[step.OutputSchema]
	// Every member shares the same deadline (buildPanelAttempt derives it once
	// from the run deadline and panelMemberTimeout); reuse it for synthesis so
	// the synthesis child cannot outlive the attempt that admitted it.
	deadline := attempt.PanelExecution.Members[0].Work.DeadlineAt
	if !deadline.After(c.now()) {
		return workflowledger.PanelTaskSpec{}, context.DeadlineExceeded
	}
	runID, taskID := attempt.PanelExecution.SynthesisRunID, attempt.PanelExecution.SynthesisTaskID
	work, err := c.buildPanelTaskSpec(ctx, panelWorkSpecParams{
		RunID: runID, TaskID: taskID, AgentName: binding.AgentName, AgentDigest: binding.AgentDigest,
		Skill: step.Skill, Provider: binding.ProviderName, Model: binding.Model,
		Input: envelope, InputSchema: []byte(`{"type":"object"}`), OutputSchema: schemaRef.Bytes,
		Deadline: deadline, Limits: panelSynthesisLimits,
	})
	if err != nil {
		return workflowledger.PanelTaskSpec{}, fmt.Errorf("panel step %q synthesis: %w", step.ID, err)
	}
	return work, nil
}

// advancePanelSynthesis drives the panel's synthesis phase after every
// member has succeeded (D13's split runner: RunMembers completes before this
// runs). On first entry for one attempt it persists the exact synthesis work
// spec via CompareAndSetPanelPhase (members_admitted -> synthesis_admitted,
// D13's only legal forward transition) before any synthesis dispatch, then
// drives EnsureSynthesis/JoinSynthesis. A later re-entry (resume/takeover)
// finds the phase already synthesis_admitted and only joins the existing
// child; it never re-persists or re-dispatches.
func (c *LinearController) advancePanelSynthesis(ctx context.Context, run workflowledger.RunSnapshot, step definition.Step, attempt workflowledger.StepAttempt, panel workflowledger.PanelCoordinator, membersResult PanelMembersResult) (workflowledger.RunSnapshot, bool, error) {
	memberInputs, err := panelSynthesisMemberInputs(attempt.PanelExecution, membersResult)
	if err != nil {
		return c.failAttempt(ctx, run, attempt, err)
	}
	envelopeStruct, envelope, err := BuildSynthesisEnvelope(step.ID, memberInputs)
	if err != nil {
		return c.settleAgentAttempt(ctx, run, step, attempt, AgentStepResult{Status: "failed"}, err)
	}
	if attempt.PanelExecution.Phase == workflowledger.PanelPhaseMembersAdmitted {
		work, err := c.buildPanelSynthesisWork(ctx, run, step, attempt, envelope)
		if err != nil {
			return c.failAttempt(ctx, run, attempt, err)
		}
		// D13: refresh the claim immediately before the phase-intent write, so
		// a holder that lost its claim during member execution cannot commit a
		// stale synthesis transition.
		if err := c.Repo.ClaimRun(ctx, c.RunID, c.Holder); err != nil {
			return c.fail(ctx, run, err)
		}
		synthesis := &workflowledger.PanelSynthesisExecution{Work: work}
		if err := c.Repo.CompareAndSetPanelPhase(ctx, c.RunID, attempt.AttemptID, attempt.Version, workflowledger.PanelPhaseMembersAdmitted, workflowledger.PanelPhaseSynthesisAdmitted, synthesis); err != nil {
			return c.fail(ctx, run, err)
		}
		refreshed, err := c.Repo.GetStepAttempt(ctx, c.RunID, attempt.AttemptID)
		if err != nil {
			return c.fail(ctx, run, err)
		}
		attempt = refreshed
	}
	handle, err := panel.EnsureSynthesis(ctx, attempt.AttemptID)
	if err != nil {
		return c.settleAgentAttempt(ctx, run, step, attempt, AgentStepResult{Status: "failed"}, err)
	}
	result, joinErr := panel.JoinSynthesis(ctx, attempt.AttemptID, handle)
	if joinErr != nil {
		return c.settleAgentAttempt(ctx, run, step, attempt, AgentStepResult{Status: "failed"}, joinErr)
	}
	if result == nil || result.Err != nil || len(result.Results) == 0 {
		cause := fmt.Errorf("panel synthesis produced no result")
		if result != nil && result.Err != nil {
			cause = result.Err
		}
		return c.settleAgentAttempt(ctx, run, step, attempt, AgentStepResult{Status: "failed"}, cause)
	}
	taskResult := result.Results[0]
	if taskResult.Err != nil {
		return c.settleAgentAttempt(ctx, run, step, attempt, AgentStepResult{Status: taskResult.Status}, taskResult.Err)
	}
	if err := panelSynthesisTaskStatusError(taskResult); err != nil {
		return c.settleAgentAttempt(ctx, run, step, attempt, AgentStepResult{Status: taskResult.Status}, err)
	}
	keys := AllCanonicalSourceKeys(envelopeStruct)
	synthOut, err := DecodeStrictPanelSynthesisOutput([]byte(taskResult.Output), keys)
	if err != nil {
		return c.settleAgentAttempt(ctx, run, step, attempt, AgentStepResult{Status: "failed"}, err)
	}
	final := PanelFinalReport{HostVerdict: envelopeStruct.HostVerdict, Dispositions: synthOut.Dispositions, Summary: synthOut.Summary}
	output, err := json.Marshal(final)
	if err != nil {
		return c.settleAgentAttempt(ctx, run, step, attempt, AgentStepResult{Status: "failed"}, err)
	}
	result2 := AgentStepResult{
		CoordinatorRunID: attempt.PanelExecution.SynthesisRunID, TaskID: attempt.PanelExecution.SynthesisTaskID,
		Output: output, Status: "completed",
	}
	return c.settleAgentAttempt(ctx, run, step, attempt, result2, nil)
}

// panelSynthesisTaskStatusError checks the synthesis task's own terminal
// status, mirroring panelMemberResultError's status check for members
// (panel_runner.go). coordinator.mapStatus treats Status as authoritative
// independent of Err, so a synthesis task can terminate
// failed/timed_out/canceled/blocked with Err == nil; without this check its
// stale or partial Output would decode straight into the final settled
// report. The member check is not a substitute for this one: the synthesis
// result is read directly in advancePanelSynthesis, not through that
// chokepoint.
func panelSynthesisTaskStatusError(taskResult subagents.Result) error {
	if taskResult.Status != "completed" {
		return fmt.Errorf("panel synthesis task ended with status %q, not completed", taskResult.Status)
	}
	// D14: a completed task with missing content is a panel failure, not
	// something to settle as the final report.
	if len(taskResult.Output) == 0 {
		return fmt.Errorf("panel synthesis task completed with no output content")
	}
	return nil
}

// panelSynthesisMemberInputs gathers each successful member's raw output and
// host-known identity fields, in the member's declaration order, for
// BuildSynthesisEnvelope. It is only called after RunPanelMembers returns a
// nil error, so every member result here is a successful terminal result.
func panelSynthesisMemberInputs(execution *workflowledger.PanelExecution, results PanelMembersResult) ([]PanelSynthesisMemberInput, error) {
	byID := make(map[string]PanelMemberResult, len(results.Members))
	for _, r := range results.Members {
		byID[r.MemberID] = r
	}
	inputs := make([]PanelSynthesisMemberInput, 0, len(execution.Members))
	for _, member := range execution.Members {
		result, ok := byID[member.MemberID]
		if !ok || result.Result == nil || len(result.Result.Results) == 0 {
			return nil, fmt.Errorf("panel member %q has no coordinator result", member.MemberID)
		}
		taskResult := result.Result.Results[0]
		inputs = append(inputs, PanelSynthesisMemberInput{
			MemberID: member.MemberID, AgentName: member.Work.AgentName, AgentDigest: member.Work.AgentDigest,
			Provider: member.Work.Provider, Model: member.Work.Model,
			CoordinatorRunID: member.CoordinatorRunID, CoordinatorTaskID: member.TaskID,
			TerminalStatus: taskResult.Status, RawOutput: []byte(taskResult.Output),
		})
	}
	return inputs, nil
}
