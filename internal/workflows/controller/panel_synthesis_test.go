package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	coordledger "github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// fixedOutputHandler always returns the same raw JSON, regardless of input.
type fixedOutputHandler struct{ raw string }

func (h fixedOutputHandler) Invoke(context.Context, runtime.Request) (json.RawMessage, error) {
	return json.RawMessage(h.raw), nil
}

// stringInputHandler enforces the live MultiStepHandler input contract: the
// dispatched task input must be a JSON string (the task prompt), mirroring
// subagents.MultiStepHandler.Invoke's json.Unmarshal(req.Input, &taskPrompt).
// fixedOutputHandler ignores req.Input, so a controller bug that dispatches a
// non-string input (e.g. the raw synthesis envelope object instead of a
// JSON-string-wrapped prompt) would pass every fixture test and only fail on
// live runs. Registering this handler for panel children proves the input
// shape in-test too.
type stringInputHandler struct{ raw string }

func (h stringInputHandler) Invoke(_ context.Context, req runtime.Request) (json.RawMessage, error) {
	var prompt string
	if err := json.Unmarshal(req.Input, &prompt); err != nil {
		return nil, fmt.Errorf("task input must be a JSON string: %w", err)
	}
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("task prompt is empty")
	}
	return json.RawMessage(h.raw), nil
}

// panelSynthesisFixture builds a controller for an agent_panel step whose
// members and synthesizer are both wired to fake, always-succeeding
// handlers, so tests can drive the whole Wave 5 pipeline (RunPanelMembers ->
// CompareAndSetPanelPhase -> EnsureSynthesis/JoinSynthesis) directly, without
// going through the fail-closed panelsEnabled gate.
func panelSynthesisFixture(t *testing.T, runID, memberReport, synthesisOutput string) (*LinearController, workflowledger.Repository, definition.Step) {
	t.Helper()
	step := definition.Step{
		ID: "review", Kind: "agent_panel", Agent: "review-synthesizer", Skill: "review-synthesis",
		Template: "synth", OutputSchema: "synthschema",
		Context: []definition.ContextBinding{{From: "inputs.task", As: "task", MaxBytes: 1024}},
		Panel: &definition.AgentPanel{FailurePolicy: "require_all", Members: []definition.PanelMember{
			{ID: "security", Agent: "panel-reviewer", Skill: "secure-change", Template: "security", OutputSchema: "report"},
			{ID: "correctness", Agent: "panel-reviewer", Skill: "bug-audit", Template: "correctness", OutputSchema: "report"},
		}},
	}
	snapshot, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{
		PanelBindings: map[string]workflowledger.PanelBindingSnapshot{
			"review/security":    {AgentName: "panel-reviewer", AgentDigest: strings.Repeat("a", 64), ProviderName: "deepseek", Model: "deepseek-v4-flash"},
			"review/correctness": {AgentName: "panel-reviewer", AgentDigest: strings.Repeat("b", 64), ProviderName: "zai", Model: "glm-5.2"},
			"review/synthesis":   {AgentName: "review-synthesizer", AgentDigest: strings.Repeat("c", 64), ProviderName: "deepseek", Model: "deepseek-v4-flash"},
		},
		Templates: map[string]workflowledger.RefSnapshot{
			"security": {Bytes: []byte("Review {{inputs.task}}.")}, "correctness": {Bytes: []byte("Review {{inputs.task}}.")},
			"synth": {Bytes: []byte("Synthesize.")},
		},
		Schemas: map[string]workflowledger.RefSnapshot{
			"report":      {Bytes: []byte(`{"type":"object"}`)},
			"synthschema": {Bytes: []byte(`{"type":"object"}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := runtime.New(runtime.Policy{})
	if err := dispatcher.Register(runtime.Subagent, "panel-reviewer", stringInputHandler{raw: memberReport}); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Register(runtime.Subagent, "review-synthesizer", stringInputHandler{raw: synthesisOutput}); err != nil {
		t.Fatal(err)
	}
	coordLedger := coordledger.NewMemoryLedgerRepository()
	coord := coordinator.New(coordLedger, subagents.New(dispatcher, subagents.Policy{Workers: 4}))
	repo := workflowledger.NewMemoryRepository()
	wf := &compiler.CompiledWorkflow{Name: "panel", InitialStep: step.ID, Steps: []definition.Step{step}, Transitions: []definition.Transition{
		{From: step.ID, To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
	}}
	ctrl, err := NewLinearController(repo, NewCoordinatorRunner(coord), wf, nil, map[string]any{"task": "change"}, runID, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return ctrl, repo, step
}

// driveAdvancePanelSynthesis replicates advancePanelStep's admission and
// member-run steps directly, bypassing the panelsEnabled fail-closed gate
// (Wave 5's wiring is dead code in production until Wave 6 lands; this test
// exercises it directly, matching how RunPanelMembers itself is tested).
func driveAdvancePanelSynthesis(t *testing.T, ctrl *LinearController, repo workflowledger.Repository, step definition.Step) (workflowledger.RunSnapshot, bool, error) {
	t.Helper()
	if err := ctrl.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	ctx := workflowledger.ContextWithClaimHolder(context.Background(), ctrl.Holder)
	if err := repo.ClaimRun(ctx, ctrl.RunID, ctrl.Holder); err != nil {
		t.Fatalf("ClaimRun() error = %v", err)
	}
	run, err := repo.GetRun(ctx, ctrl.RunID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	if run.Status == workflowledger.RunStatusPending {
		if err := repo.CompareAndSetRunStatus(ctx, ctrl.RunID, run.Version, workflowledger.RunStatusRunning, nil); err != nil {
			t.Fatalf("CompareAndSetRunStatus() error = %v", err)
		}
		run, err = repo.GetRun(ctx, ctrl.RunID)
		if err != nil {
			t.Fatalf("GetRun() error = %v", err)
		}
	}
	attempt, err := ctrl.buildPanelAttempt(ctx, run, step, nil)
	if err != nil {
		t.Fatalf("buildPanelAttempt() error = %v", err)
	}
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		t.Fatalf("CreateStepAttempt() error = %v", err)
	}
	attempt, err = repo.GetStepAttempt(ctx, ctrl.RunID, attempt.AttemptID)
	if err != nil {
		t.Fatalf("GetStepAttempt() error = %v", err)
	}
	runner := ctrl.Runner.(*CoordinatorRunner)
	panel := workflowledger.NewPanelCoordinator(ctrl.RunID, runner.Coordinator, repo)
	members := make([]PanelMemberRequest, len(attempt.PanelExecution.Members))
	for i, member := range attempt.PanelExecution.Members {
		members[i] = PanelMemberRequest{MemberID: member.MemberID, RunID: member.CoordinatorRunID}
	}
	membersResult, err := RunPanelMembers(ctx, ctrl.PanelLimiter, PanelMembersRequest{AttemptID: attempt.AttemptID, Members: members, Coordinator: panel})
	if err != nil {
		t.Fatalf("RunPanelMembers() error = %v", err)
	}
	return ctrl.advancePanelSynthesis(ctx, run, step, attempt, panel, membersResult)
}

// countingPanelPhaseRepository counts CompareAndSetPanelPhase calls so a
// test can prove a re-entrant advancePanelSynthesis does not re-persist the
// phase transition.
type countingPanelPhaseRepository struct {
	workflowledger.Repository
	compareAndSetPanelPhaseCalls int
}

func (r *countingPanelPhaseRepository) CompareAndSetPanelPhase(ctx context.Context, runID, attemptID string, expectedVersion uint64, from, to workflowledger.PanelPhase, synthesis *workflowledger.PanelSynthesisExecution) error {
	r.compareAndSetPanelPhaseCalls++
	return r.Repository.CompareAndSetPanelPhase(ctx, runID, attemptID, expectedVersion, from, to, synthesis)
}

// conflictingPanelPhaseRepository fails the first CompareAndSetPanelPhase
// call with ErrConflict, simulating a claim/version race lost to another
// holder, then behaves normally.
type conflictingPanelPhaseRepository struct {
	workflowledger.Repository
	failed bool
}

func (r *conflictingPanelPhaseRepository) CompareAndSetPanelPhase(ctx context.Context, runID, attemptID string, expectedVersion uint64, from, to workflowledger.PanelPhase, synthesis *workflowledger.PanelSynthesisExecution) error {
	if !r.failed {
		r.failed = true
		return workflowledger.ErrConflict
	}
	return r.Repository.CompareAndSetPanelPhase(ctx, runID, attemptID, expectedVersion, from, to, synthesis)
}

// Test-review regression: no test exercised advancePanelSynthesis when the
// attempt's phase is ALREADY synthesis_admitted (the state a resumed or
// re-entrant caller sees after a crash right after the phase transition
// commits, before EnsureSynthesis/JoinSynthesis ran). advancePanelSynthesis
// must skip the CompareAndSetPanelPhase call entirely in that case, never
// re-persisting or re-dispatching.
func TestAdvancePanelSynthesis_ReentrySkipsPhaseTransition(t *testing.T) {
	memberReport := `{"verdict":"approved","findings":[]}`
	synthesisOutput := `{"dispositions":[],"summary":"Nothing to report."}`
	ctrl, repo, step := panelSynthesisFixture(t, "wfr-panel-synth-reentry", memberReport, synthesisOutput)
	counting := &countingPanelPhaseRepository{Repository: repo}
	ctrl.Repo = counting
	ctx := workflowledger.ContextWithClaimHolder(context.Background(), ctrl.Holder)

	run, attempt, panel, membersResult := driveToSynthesisAdmitted(t, ctx, ctrl, counting, step)
	if counting.compareAndSetPanelPhaseCalls != 1 {
		t.Fatalf("setup: CompareAndSetPanelPhase called %d times, want 1", counting.compareAndSetPanelPhaseCalls)
	}

	// Re-entry: advancePanelSynthesis must see the phase already
	// synthesis_admitted and skip straight to EnsureSynthesis/JoinSynthesis,
	// never calling CompareAndSetPanelPhase again.
	runNow, done, err := ctrl.advancePanelSynthesis(ctx, run, step, attempt, panel, membersResult)
	if err != nil {
		t.Fatalf("re-entry: advancePanelSynthesis() error = %v", err)
	}
	if !done || runNow.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("re-entry: run = %+v, done = %v, want succeeded/true", runNow, done)
	}
	if counting.compareAndSetPanelPhaseCalls != 1 {
		t.Fatalf("re-entry: CompareAndSetPanelPhase called %d times total, want still 1 (no re-persist)", counting.compareAndSetPanelPhaseCalls)
	}
}

// driveToSynthesisAdmitted admits a panel attempt, runs its members, and
// drives the members_admitted -> synthesis_admitted phase transition
// directly, then stops - simulating a crash right after that transition
// commits, before EnsureSynthesis/JoinSynthesis ran. It reloads the attempt
// into the exact nonterminal, synthesis_admitted state a real resume would
// see, so a caller can then re-enter advancePanelSynthesis from there.
func driveToSynthesisAdmitted(t *testing.T, ctx context.Context, ctrl *LinearController, repo workflowledger.Repository, step definition.Step) (workflowledger.RunSnapshot, workflowledger.StepAttempt, workflowledger.PanelCoordinator, PanelMembersResult) {
	t.Helper()
	if err := ctrl.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := repo.ClaimRun(ctx, ctrl.RunID, ctrl.Holder); err != nil {
		t.Fatal(err)
	}
	run, err := repo.GetRun(ctx, ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status == workflowledger.RunStatusPending {
		if err := repo.CompareAndSetRunStatus(ctx, ctrl.RunID, run.Version, workflowledger.RunStatusRunning, nil); err != nil {
			t.Fatal(err)
		}
		run, err = repo.GetRun(ctx, ctrl.RunID)
		if err != nil {
			t.Fatal(err)
		}
	}
	attempt, err := ctrl.buildPanelAttempt(ctx, run, step, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	attempt, err = repo.GetStepAttempt(ctx, ctrl.RunID, attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	runnerX := ctrl.Runner.(*CoordinatorRunner)
	panel := workflowledger.NewPanelCoordinator(ctrl.RunID, runnerX.Coordinator, repo)
	members := make([]PanelMemberRequest, len(attempt.PanelExecution.Members))
	for i, m := range attempt.PanelExecution.Members {
		members[i] = PanelMemberRequest{MemberID: m.MemberID, RunID: m.CoordinatorRunID}
	}
	membersResult, err := RunPanelMembers(ctx, ctrl.PanelLimiter, PanelMembersRequest{AttemptID: attempt.AttemptID, Members: members, Coordinator: panel})
	if err != nil {
		t.Fatal(err)
	}
	memberInputs, err := panelSynthesisMemberInputs(attempt.PanelExecution, membersResult)
	if err != nil {
		t.Fatal(err)
	}
	_, envelope, err := BuildSynthesisEnvelope(step.ID, memberInputs)
	if err != nil {
		t.Fatal(err)
	}
	work, err := ctrl.buildPanelSynthesisWork(ctx, run, step, attempt, envelope)
	if err != nil {
		t.Fatal(err)
	}
	synthesis := &workflowledger.PanelSynthesisExecution{Work: work}
	if err := repo.CompareAndSetPanelPhase(ctx, ctrl.RunID, attempt.AttemptID, attempt.Version, workflowledger.PanelPhaseMembersAdmitted, workflowledger.PanelPhaseSynthesisAdmitted, synthesis); err != nil {
		t.Fatal(err)
	}
	attempt, err = repo.GetStepAttempt(ctx, ctrl.RunID, attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.PanelExecution.Phase != workflowledger.PanelPhaseSynthesisAdmitted {
		t.Fatalf("setup: phase = %q, want synthesis_admitted", attempt.PanelExecution.Phase)
	}
	return run, attempt, panel, membersResult
}

// Test-review regression: no test exercised CompareAndSetPanelPhase
// returning an error (e.g. a lost claim/version race). advancePanelSynthesis
// must settle the run failed via c.fail, not hang or panic.
func TestAdvancePanelSynthesis_PhaseTransitionConflictFailsTheRun(t *testing.T) {
	memberReport := `{"verdict":"approved","findings":[]}`
	synthesisOutput := `{"dispositions":[],"summary":"Nothing to report."}`
	ctrl, repo, step := panelSynthesisFixture(t, "wfr-panel-synth-conflict", memberReport, synthesisOutput)
	ctrl.Repo = &conflictingPanelPhaseRepository{Repository: repo}
	run, done, err := driveAdvancePanelSynthesis(t, ctrl, ctrl.Repo, step)
	if err == nil {
		t.Fatal("advancePanelSynthesis() error = nil, want the injected phase-transition conflict")
	}
	if !done {
		t.Fatal("advancePanelSynthesis() done = false, want a terminal (failed) run")
	}
	if run.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run status = %q, want failed", run.Status)
	}
}

// Test-review regression: no test proved the coordinator task's dispatched
// Input bytes are EXACTLY BuildSynthesisEnvelope's encoded output, only that
// each side independently does the right thing. Load the synthesis work's
// stored input content and compare it byte-for-byte against an
// independently rebuilt envelope from the same member inputs.
func TestAdvancePanelSynthesis_DispatchedInputMatchesBuiltEnvelope(t *testing.T) {
	memberReport := `{"verdict":"changes_requested","findings":[{"id":"f1","title":"t","severity":"low","description":"d"}]}`
	synthesisOutput := `{"dispositions":[` +
		`{"member_id":"security","finding_id":"f1","disposition":"included","final_finding_id":"F1"},` +
		`{"member_id":"correctness","finding_id":"f1","disposition":"duplicate","final_finding_id":"F1"}` +
		`],"summary":"One finding reported by both members."}`
	ctrl, repo, step := panelSynthesisFixture(t, "wfr-panel-synth-envelope-match", memberReport, synthesisOutput)
	_, done, err := driveAdvancePanelSynthesis(t, ctrl, repo, step)
	if err != nil || !done {
		t.Fatalf("driveAdvancePanelSynthesis() done = %v, err = %v", done, err)
	}
	attempts, err := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var attempt workflowledger.StepAttempt
	for _, a := range attempts {
		if a.StepID == step.ID {
			attempt = a
		}
	}
	if attempt.PanelExecution == nil || attempt.PanelExecution.Synthesis == nil {
		t.Fatal("no persisted synthesis work")
	}
	dispatchedInput, err := repo.LoadContent(context.Background(), attempt.PanelExecution.Synthesis.Work.InputRef)
	if err != nil {
		t.Fatalf("LoadContent(InputRef) error = %v", err)
	}
	memberInputs := []PanelSynthesisMemberInput{
		panelMemberInput("security", []byte(memberReport)),
		panelMemberInput("correctness", []byte(memberReport)),
	}
	for i, m := range attempt.PanelExecution.Members {
		memberInputs[i].CoordinatorRunID = m.CoordinatorRunID
		memberInputs[i].CoordinatorTaskID = m.TaskID
		memberInputs[i].AgentDigest = m.Work.AgentDigest
		memberInputs[i].Provider = m.Work.Provider
		memberInputs[i].Model = m.Work.Model
		memberInputs[i].TerminalStatus = "completed"
	}
	_, wantEnvelope, err := BuildSynthesisEnvelope(step.ID, memberInputs)
	if err != nil {
		t.Fatalf("BuildSynthesisEnvelope() error = %v", err)
	}
	// The runtime dispatches panel children through the multi-step subagent
	// handler, which requires the task input to be a JSON string. The synthesis
	// prompt is the envelope JSON wrapped in a JSON string (mustJSON), so the
	// dispatched input must equal that wrapping, not the raw envelope object.
	wantInput, err := json.Marshal(string(wantEnvelope))
	if err != nil {
		t.Fatalf("json.Marshal(string(wantEnvelope)) error = %v", err)
	}
	if string(dispatchedInput) != string(wantInput) {
		t.Fatalf("dispatched Input =\n%s\nwant (JSON-string-wrapped envelope) =\n%s", dispatchedInput, wantInput)
	}
}

func TestAdvancePanelSynthesis_EndToEndSuccess(t *testing.T) {
	memberReport := `{"verdict":"changes_requested","findings":[{"id":"f1","title":"t","severity":"low","description":"d"}]}`
	synthesisOutput := `{"dispositions":[` +
		`{"member_id":"security","finding_id":"f1","disposition":"included","final_finding_id":"F1"},` +
		`{"member_id":"correctness","finding_id":"f1","disposition":"duplicate","final_finding_id":"F1"}` +
		`],"summary":"One finding reported by both members."}`
	ctrl, repo, step := panelSynthesisFixture(t, "wfr-panel-synth-ok", memberReport, synthesisOutput)
	run, done, err := driveAdvancePanelSynthesis(t, ctrl, repo, step)
	if err != nil {
		t.Fatalf("advancePanelSynthesis() error = %v", err)
	}
	if !done {
		t.Fatal("advancePanelSynthesis() done = false, want true")
	}
	if run.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run status = %q, want succeeded", run.Status)
	}
	attempts, err := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var attempt workflowledger.StepAttempt
	for _, a := range attempts {
		if a.StepID == step.ID {
			attempt = a
		}
	}
	if attempt.Status != workflowledger.AttemptStatusSucceeded {
		t.Fatalf("attempt status = %q, want succeeded", attempt.Status)
	}
	if attempt.PanelExecution.Phase != workflowledger.PanelPhaseSynthesisAdmitted {
		t.Fatalf("panel phase = %q, want synthesis_admitted", attempt.PanelExecution.Phase)
	}
	if attempt.PanelExecution.Synthesis == nil {
		t.Fatal("panel execution has no persisted synthesis work")
	}
	body, err := repo.LoadContent(context.Background(), attempt.OutputRef)
	if err != nil {
		t.Fatalf("LoadContent() error = %v", err)
	}
	var final PanelFinalReport
	if err := json.Unmarshal(body, &final); err != nil {
		t.Fatalf("attempt output did not decode: %v; output = %s", err, body)
	}
	if final.HostVerdict != PanelVerdictChangesRequested {
		t.Fatalf("HostVerdict = %q, want %q", final.HostVerdict, PanelVerdictChangesRequested)
	}
	if len(final.Dispositions) != 2 {
		t.Fatalf("dispositions = %d, want 2", len(final.Dispositions))
	}
}

// Regression (live feature-delivery runs, "panel member report: invalid
// verdict \"\""): the real agent handler returns a transport envelope from
// every coordinator task - {"output": <model JSON>, "status": "completed",
// "schema": "ok", "steps": N, "elapsed": "...", "step_count": N} - and the
// panel path must unwrap it with extractTaskOutput before strict decoding.
// The fake handlers in panelSynthesisFixture return the raw payload, which is
// why this never surfaced in unit tests. Driving the whole pipeline with
// REAL envelope-shaped member and synthesis outputs proves the unwrap on both
// decode sites; before the fix this fails with the exact production error
// ("panel member report: invalid verdict \"\"").
func TestAdvancePanelSynthesis_UnwrapsCoordinatorResultEnvelopes(t *testing.T) {
	memberJSON := `{"verdict":"changes_requested","findings":[{"id":"f1","title":"t","severity":"low","description":"d"}]}`
	memberEnvelope := `{"output":` + memberJSON + `,"schema":"ok","status":"completed","steps":4,"elapsed":"1m0s","step_count":5}`
	synthJSON := `{"dispositions":[` +
		`{"member_id":"security","finding_id":"f1","disposition":"included","final_finding_id":"F1"},` +
		`{"member_id":"correctness","finding_id":"f1","disposition":"duplicate","final_finding_id":"F1"}` +
		`],"summary":"One finding reported by both members."}`
	synthEnvelope := `{"output":` + synthJSON + `,"schema":"ok","status":"completed","steps":3,"elapsed":"42s","step_count":3}`
	ctrl, repo, step := panelSynthesisFixture(t, "wfr-panel-synth-envelope-unwrap", memberEnvelope, synthEnvelope)
	run, done, err := driveAdvancePanelSynthesis(t, ctrl, repo, step)
	if err != nil {
		t.Fatalf("advancePanelSynthesis() error = %v", err)
	}
	if !done {
		t.Fatal("advancePanelSynthesis() done = false, want true")
	}
	if run.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run status = %q, want succeeded", run.Status)
	}
}

// Focused regression for the member side of the same envelope bug: a
// coordinator task result whose Output is the handler envelope must be
// unwrapped before it becomes a member's RawOutput, so the strict decoder
// sees the report, not the envelope's own fields.
func TestPanelSynthesisMemberInputs_UnwrapsEnvelope(t *testing.T) {
	memberJSON := `{"verdict":"approved","findings":[]}`
	envelope := `{"output":` + memberJSON + `,"schema":"ok","status":"completed","steps":4,"elapsed":"1m0s","step_count":5}`
	execution := &workflowledger.PanelExecution{Members: []workflowledger.PanelMemberExecution{{
		MemberID: "security", CoordinatorRunID: "run-1", TaskID: "task-1",
		Work: workflowledger.PanelTaskSpec{AgentName: "panel-reviewer", AgentDigest: strings.Repeat("a", 64), Provider: "deepseek", Model: "deepseek-v4-flash"},
	}}}
	results := PanelMembersResult{Members: []PanelMemberResult{{MemberID: "security", Result: &coordinator.RunResult{Results: []subagents.Result{{TaskID: "task-1", Status: "completed", Output: json.RawMessage(envelope)}}}}}}
	inputs, err := panelSynthesisMemberInputs(execution, results)
	if err != nil {
		t.Fatalf("panelSynthesisMemberInputs() error = %v", err)
	}
	if len(inputs) != 1 {
		t.Fatalf("member inputs = %d, want 1", len(inputs))
	}
	if got := string(inputs[0].RawOutput); got != memberJSON {
		t.Fatalf("RawOutput = %s, want unwrapped %s", got, memberJSON)
	}
	if _, _, err := DecodeStrictPanelMemberReport(inputs[0].RawOutput); err != nil {
		t.Fatalf("DecodeStrictPanelMemberReport(unwrapped) error = %v", err)
	}
}

// A model cannot flip the host verdict to approved by claiming so in its own
// member report text or in the synthesizer's own output: the host always
// recomputes it from the bounded member reports (D10).
func TestAdvancePanelSynthesis_HostVerdictOverridesMemberContent(t *testing.T) {
	memberReport := `{"verdict":"approved","findings":[]}`
	synthesisOutput := `{"dispositions":[],"summary":"Nothing to report."}`
	ctrl, repo, step := panelSynthesisFixture(t, "wfr-panel-synth-approved", memberReport, synthesisOutput)
	run, done, err := driveAdvancePanelSynthesis(t, ctrl, repo, step)
	if err != nil {
		t.Fatalf("advancePanelSynthesis() error = %v", err)
	}
	if !done || run.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v, done = %v, want succeeded/true", run, done)
	}
	attempts, _ := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	var attempt workflowledger.StepAttempt
	for _, a := range attempts {
		if a.StepID == step.ID {
			attempt = a
		}
	}
	body, err := repo.LoadContent(context.Background(), attempt.OutputRef)
	if err != nil {
		t.Fatalf("LoadContent() error = %v", err)
	}
	var final PanelFinalReport
	if err := json.Unmarshal(body, &final); err != nil {
		t.Fatalf("attempt output did not decode: %v", err)
	}
	if final.HostVerdict != PanelVerdictApproved {
		t.Fatalf("HostVerdict = %q, want %q", final.HostVerdict, PanelVerdictApproved)
	}
}

// The synthesizer cannot invent a disposition for a source key that does not
// exist, or omit one that does: DecodeStrictPanelSynthesisOutput rejects the
// mismatch, and the attempt fails rather than settling on unverified output.
func TestAdvancePanelSynthesis_RejectsIncompleteDispositions(t *testing.T) {
	memberReport := `{"verdict":"changes_requested","findings":[{"id":"f1","title":"t","severity":"low","description":"d"}]}`
	synthesisOutput := `{"dispositions":[{"member_id":"security","finding_id":"f1","disposition":"included","final_finding_id":"F1"}],"summary":"Missing the other member's disposition."}`
	ctrl, repo, step := panelSynthesisFixture(t, "wfr-panel-synth-incomplete", memberReport, synthesisOutput)
	run, done, err := driveAdvancePanelSynthesis(t, ctrl, repo, step)
	if err == nil {
		t.Fatal("advancePanelSynthesis() error = nil, want incomplete-disposition rejection")
	}
	if !done {
		t.Fatal("advancePanelSynthesis() done = false, want a terminal (failed) run")
	}
	if run.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run status = %q, want failed", run.Status)
	}
}

// Bug-audit regression: the compiler's ValidateAgentSkillReferences accepts
// an empty step.Skill on an agent_panel step (backward compatibility for
// workflows admitted before skill bindings existed), but PanelTaskSpec.Validate
// unconditionally rejects an empty Skill. buildPanelSynthesisWork must fail
// fast with a clear cause naming the missing skill, not let a legally
// compiled workflow build an invalid work spec that fails deep inside the
// ledger's phase-transition validation with an opaque ErrInvalidTransition.
func TestAdvancePanelSynthesis_RejectsMissingStepSkill(t *testing.T) {
	memberReport := `{"verdict":"approved","findings":[]}`
	synthesisOutput := `{"dispositions":[],"summary":"Nothing to report."}`
	ctrl, repo, step := panelSynthesisFixture(t, "wfr-panel-synth-no-skill", memberReport, synthesisOutput)
	step.Skill = ""
	_, done, err := driveAdvancePanelSynthesis(t, ctrl, repo, step)
	if err == nil {
		t.Fatal("advancePanelSynthesis() error = nil, want a missing-skill rejection")
	}
	if !strings.Contains(err.Error(), "requires a skill") {
		t.Fatalf("advancePanelSynthesis() error = %v, want it to name the missing skill", err)
	}
	if !done {
		t.Fatal("advancePanelSynthesis() done = false, want a terminal (failed) run")
	}
}

// Bug-audit regression (round 3): coordinator.mapStatus treats Status as
// authoritative independent of Err, so a synthesis task can terminate
// non-completed with a nil Err. panelSynthesisTaskStatusError must reject
// that, the same way panelMemberResultError already rejects it for members
// — this is a second, independent site of the same defect class, not
// covered by the member-side fix.
func TestPanelSynthesisTaskStatusErrorRejectsNonCompletedStatusWithNilErr(t *testing.T) {
	for _, status := range []string{"failed", "timed_out", "canceled", "blocked"} {
		result := subagents.Result{TaskID: "synthesis", Status: status, Err: nil, Output: json.RawMessage(`{"dispositions":[],"summary":"x"}`)}
		if err := panelSynthesisTaskStatusError(result); err == nil {
			t.Fatalf("status %q with nil Err: panelSynthesisTaskStatusError() = nil, want an error", status)
		}
	}
	completed := subagents.Result{TaskID: "synthesis", Status: "completed", Err: nil, Output: json.RawMessage(`{"dispositions":[],"summary":"x"}`)}
	if err := panelSynthesisTaskStatusError(completed); err != nil {
		t.Fatalf("status completed: panelSynthesisTaskStatusError() = %v, want nil", err)
	}
}

// D14: a completed coordinator task with missing content is a panel failure,
// not something to synthesize from.
func TestPanelSynthesisTaskStatusErrorRejectsCompletedWithEmptyOutput(t *testing.T) {
	for name, output := range map[string]json.RawMessage{"nil": nil, "empty": json.RawMessage("")} {
		t.Run(name, func(t *testing.T) {
			result := subagents.Result{TaskID: "synthesis", Status: "completed", Err: nil, Output: output}
			if err := panelSynthesisTaskStatusError(result); err == nil {
				t.Fatal("completed with no output content: panelSynthesisTaskStatusError() = nil, want an error")
			}
		})
	}
}
