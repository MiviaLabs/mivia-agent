package controller

import (
	"context"
	"encoding/json"
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
	if err := dispatcher.Register(runtime.Subagent, "panel-reviewer", fixedOutputHandler{raw: memberReport}); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Register(runtime.Subagent, "review-synthesizer", fixedOutputHandler{raw: synthesisOutput}); err != nil {
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
