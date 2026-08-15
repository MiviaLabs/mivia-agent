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
	// from the run deadline and panelMemberDeadlineDefault); reuse it for
	// synthesis so
	// the synthesis child cannot outlive the attempt that admitted it.
	deadline := attempt.PanelExecution.Members[0].Work.DeadlineAt
	if !deadline.After(c.now()) {
		return workflowledger.PanelTaskSpec{}, context.DeadlineExceeded
	}
	runID, taskID := attempt.PanelExecution.SynthesisRunID, attempt.PanelExecution.SynthesisTaskID
	synthesisLimits := panelSynthesisLimits
	synthesisLimits.MaxTurns = step.MaxTurns // 0 = unlimited (default)
	work, err := c.buildPanelTaskSpec(ctx, panelWorkSpecParams{
		RunID: runID, TaskID: taskID, AgentName: binding.AgentName, AgentDigest: binding.AgentDigest,
		Skill: step.Skill, Provider: binding.ProviderName, Model: binding.Model,
		// The runtime dispatches every panel child through the multi-step
		// subagent handler, whose Invoke contract is a JSON-string task prompt
		// (json.Unmarshal into a string) - the same shape buildPanelAttempt
		// uses for members (mustJSON(prompt)). Wrapping the envelope JSON in a
		// JSON string makes the envelope itself the synthesis task prompt (the
		// review-synthesis skill/template describe receiving "one host-assembled
		// JSON envelope"). Passing the raw envelope object bytes instead fails
		// live dispatch with "invalid task input: cannot unmarshal object into
		// string" (observed on feature-delivery runs once member reports
		// started decoding; the fake handlers in unit tests ignored req.Input
		// so the shape mismatch only surfaced live).
		Input: mustJSON(string(envelope)), InputSchema: []byte(`{"type":"string"}`), OutputSchema: schemaRef.Bytes,
		Deadline: deadline, Limits: synthesisLimits, WorkflowRunID: c.RunID,
	})
	if err != nil {
		return workflowledger.PanelTaskSpec{}, fmt.Errorf("panel step %q synthesis: %w", step.ID, err)
	}
	return work, nil
}

// advancePanelSynthesis drives the panel's synthesis phase after every
// member has succeeded (D13's split runner: RunMembers completes before this
// runs). On first entry for one attempt it builds the synthesis envelope from
// the in-memory member results, persists the exact synthesis work spec via
// CompareAndSetPanelPhase (members_admitted -> synthesis_admitted, D13's only
// legal forward transition) before any synthesis dispatch, then drives
// EnsureSynthesis/JoinSynthesis. A later re-entry (resume/takeover) finds the
// phase already synthesis_admitted and only joins the existing child; it
// never re-persists or re-dispatches. Because the re-entry has no in-memory
// member results, the envelope is rebuilt from the persisted synthesis work
// input (rebuildPanelSynthesisEnvelope) instead of from membersResult, so
// AllCanonicalSourceKeys and HostVerdict stay host-derived (D10/D11).
func (c *LinearController) advancePanelSynthesis(ctx context.Context, run workflowledger.RunSnapshot, step definition.Step, attempt workflowledger.StepAttempt, panel workflowledger.PanelCoordinator, membersResult PanelMembersResult) (workflowledger.RunSnapshot, bool, error) {
	var envelopeStruct PanelSynthesisEnvelope
	switch attempt.PanelExecution.Phase {
	case workflowledger.PanelPhaseMembersAdmitted:
		// First pass: every member has just completed, so the envelope is
		// built from the in-memory member results and the synthesis work spec
		// is persisted exactly once via the claim-fenced phase transition.
		memberInputs, err := panelSynthesisMemberInputs(attempt.PanelExecution, membersResult)
		if err != nil {
			return c.failAttempt(ctx, run, attempt, err)
		}
		var envelope []byte
		envelopeStruct, envelope, err = BuildSynthesisEnvelopeWithFilter(step.ID, memberInputs, c.panelChunkScopeFilter(step.ID, attempt.AttemptNo))
		if err != nil {
			return c.settleAgentAttempt(ctx, run, step, attempt, AgentStepResult{Status: "failed"}, err)
		}
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
	case workflowledger.PanelPhaseSynthesisAdmitted:
		// Re-entry (F1): a resume/takeover after the phase transition
		// committed but before EnsureSynthesis ran has no in-memory member
		// results. The envelope is rebuilt from the persisted synthesis work
		// input - the same host-authored, content-addressed, digest-verified
		// envelope that was validated at admission - so AllCanonicalSourceKeys
		// and HostVerdict stay host-derived (D10/D11). A missing or corrupt
		// persisted envelope fails the attempt; it never panics.
		reconstructed, err := c.rebuildPanelSynthesisEnvelope(ctx, attempt)
		if err != nil {
			return c.failAttempt(ctx, run, attempt, err)
		}
		envelopeStruct = reconstructed
	default:
		return c.failAttempt(ctx, run, attempt, fmt.Errorf("panel attempt %q has unexpected phase %q for synthesis", attempt.AttemptID, attempt.PanelExecution.Phase))
	}
	return c.settlePanelSynthesis(ctx, run, step, attempt, panel, envelopeStruct)
}

// settlePanelSynthesis drives EnsureSynthesis/JoinSynthesis for the already
// prepared envelope and settles the attempt with the host-assembled final
// report (PanelFinalReport, HostVerdict from the envelope). It is split out
// of advancePanelSynthesis so the phase-switch envelope preparation and the
// join/decode/settle tail each stay under the structure gate's function
// length limit; the two halves share nothing but the envelope.
func (c *LinearController) settlePanelSynthesis(ctx context.Context, run workflowledger.RunSnapshot, step definition.Step, attempt workflowledger.StepAttempt, panel workflowledger.PanelCoordinator, envelopeStruct PanelSynthesisEnvelope) (workflowledger.RunSnapshot, bool, error) {
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
	// Same envelope unwrap as members: the synthesis child also returns the
	// handler envelope, and decoding it directly would skip every field the
	// strict synthesis decoder requires.
	synthOut, err := DecodeStrictPanelSynthesisOutput(extractTaskOutput(taskResult.Output), keys)
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

// rebuildPanelSynthesisEnvelope reconstructs the host-authored synthesis
// envelope from the persisted synthesis work input on re-entry
// (synthesis_admitted). The runtime dispatches every panel child through the
// multi-step subagent handler, whose Invoke contract is a JSON-string task
// prompt, so the persisted input is the envelope JSON wrapped in a JSON string
// (mustJSON). LoadContent verifies the content-addressed sha256 digest, which
// ties the reconstruction to exactly the envelope the host built and validated
// at admission (1-4 strict-decoded member reports, a host-computed verdict);
// AllCanonicalSourceKeys and HostVerdict are derived from that same
// host-authored document, so D10/D11 hold on the resume path. A missing or
// corrupt persisted envelope fails with a cause naming the persisted envelope;
// it never panics.
func (c *LinearController) rebuildPanelSynthesisEnvelope(ctx context.Context, attempt workflowledger.StepAttempt) (PanelSynthesisEnvelope, error) {
	if attempt.PanelExecution == nil || attempt.PanelExecution.Synthesis == nil || attempt.PanelExecution.Synthesis.Work.InputRef == "" {
		return PanelSynthesisEnvelope{}, fmt.Errorf("panel attempt %q has no persisted synthesis work input", attempt.AttemptID)
	}
	stored, err := c.Repo.LoadContent(ctx, attempt.PanelExecution.Synthesis.Work.InputRef)
	if err != nil {
		return PanelSynthesisEnvelope{}, fmt.Errorf("panel attempt %q: persisted synthesis envelope: %w", attempt.AttemptID, err)
	}
	// Unwrap the JSON-string task prompt (mustJSON(string(envelope))) to the
	// raw envelope JSON before decoding the struct.
	var envelopeJSON string
	if err := json.Unmarshal(stored, &envelopeJSON); err != nil {
		return PanelSynthesisEnvelope{}, fmt.Errorf("panel attempt %q: persisted synthesis envelope is not a JSON-string prompt: %w", attempt.AttemptID, err)
	}
	if len(envelopeJSON) > maxSynthesisEnvelopeBytes {
		return PanelSynthesisEnvelope{}, fmt.Errorf("panel attempt %q: persisted synthesis envelope is %d bytes, exceeds %d byte bound", attempt.AttemptID, len(envelopeJSON), maxSynthesisEnvelopeBytes)
	}
	var envelope PanelSynthesisEnvelope
	if err := json.Unmarshal([]byte(envelopeJSON), &envelope); err != nil {
		return PanelSynthesisEnvelope{}, fmt.Errorf("panel attempt %q: persisted synthesis envelope decode: %w", attempt.AttemptID, err)
	}
	if len(envelope.Members) < 1 || len(envelope.Members) > 4 {
		return PanelSynthesisEnvelope{}, fmt.Errorf("panel attempt %q: persisted synthesis envelope has %d members, want 1 to 4", attempt.AttemptID, len(envelope.Members))
	}
	return envelope, nil
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
// BuildSynthesisEnvelope. Failed members (result.Err != nil, or nil result
// with no task results) are skipped. Only an error is returned for a genuine
// structural problem: when no member succeeded at all (the caller requires at
// least one successful member to build an envelope). It is only called after
// RunPanelMembers returns a nil error, so the member results here are all
// terminal.
func panelSynthesisMemberInputs(execution *workflowledger.PanelExecution, results PanelMembersResult) ([]PanelSynthesisMemberInput, error) {
	byID := make(map[string]PanelMemberResult, len(results.Members))
	for _, r := range results.Members {
		byID[r.MemberID] = r
	}
	inputs := make([]PanelSynthesisMemberInput, 0, len(execution.Members))
	for _, member := range execution.Members {
		result, ok := byID[member.MemberID]
		// Skip failed members rather than failing the whole panel synthesis:
		// a member that errored, produced no coordinator result, or produced
		// no task results is omitted from the envelope.
		if !ok || result.Err != nil || result.Result == nil || len(result.Result.Results) == 0 {
			continue
		}
		taskResult := result.Result.Results[0]
		inputs = append(inputs, PanelSynthesisMemberInput{
			MemberID: member.MemberID, AgentName: member.Work.AgentName, AgentDigest: member.Work.AgentDigest,
			Provider: member.Work.Provider, Model: member.Work.Model,
			CoordinatorRunID: member.CoordinatorRunID, CoordinatorTaskID: member.TaskID,
			// The coordinator task result is the handler envelope
			// ({"output":..., "status":..., "schema":..., "elapsed":...}), not the
			// member's model JSON. extractTaskOutput is the controller-wide
			// mechanism for unwrapping it (see its contract in agent_step.go);
			// without it the envelope's own fields (status/schema/elapsed/steps)
			// are skipped as unknown and the report decodes to verdict ""
			// (observed as "panel member report: invalid verdict \"\"" on live
			// feature-delivery runs). DC-14's unknown-field tolerance masked this
			// root cause by letting the envelope decode "successfully".
			TerminalStatus: taskResult.Status, RawOutput: extractTaskOutput(taskResult.Output),
		})
	}
	if len(inputs) == 0 {
		return nil, fmt.Errorf("panel synthesis has no successful member results to build an envelope from")
	}
	return inputs, nil
}
