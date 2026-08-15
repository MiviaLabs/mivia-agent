package controller

import (
	"context"
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

// panelAllowPartialFixture builds a controller for an agent_panel step with
// the given failure policy, members wired to the given handler, and a
// synthesizer that always succeeds. It mirrors panelRetryFixture but
// parameterizes the failure policy so tests can contrast require_all (any
// member failure fails the attempt) with allow_partial (a single member
// failure is noted and the panel proceeds to synthesis with the successful
// members; the attempt fails only if ALL members fail).
func panelAllowPartialFixture(t *testing.T, runID string, policy string, memberHandler runtime.Handler, synthesisOutput string) (*LinearController, workflowledger.Repository) {
	t.Helper()
	step := definition.Step{
		ID: "review", Kind: "agent_panel", Agent: "review-synthesizer", Skill: "review-synthesis",
		Template: "synth", OutputSchema: "synthschema",
		Context:   []definition.ContextBinding{{From: "inputs.task", As: "task", MaxBytes: 1024}},
		OnFailure: "review",
		Panel: &definition.AgentPanel{FailurePolicy: policy, Members: []definition.PanelMember{
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
	if err := dispatcher.Register(runtime.Subagent, "panel-reviewer", memberHandler); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Register(runtime.Subagent, "review-synthesizer", fixedOutputHandler{raw: synthesisOutput}); err != nil {
		t.Fatal(err)
	}
	coordLedger := coordledger.NewMemoryLedgerRepository()
	coord := coordinator.New(coordLedger, subagents.New(dispatcher, subagents.Policy{Workers: 4}))
	repo := workflowledger.NewMemoryRepository()
	wf := &compiler.CompiledWorkflow{
		Name: "panel-allow-partial", InitialStep: step.ID, Steps: []definition.Step{step},
		Limits: definition.Limits{MaxStepAttempts: 16},
		Transitions: []definition.Transition{
			{From: step.ID, To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
	}
	ctrl, err := NewLinearController(repo, NewCoordinatorRunner(coord), wf, nil, map[string]any{"task": "change"}, runID, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return ctrl, repo
}

// TestPanelAllowPartialMemberFailureProceedsToSynthesis is the regression
// test for the allow_partial failure policy: when ONE panel member fails
// while another succeeds, the panel attempt must NOT fail the whole panel.
// Instead the failed member is noted (ProgressPanelMemberFailed) and the
// panel proceeds to synthesis with the successful member, so the run
// succeeds on the first attempt with no re-entry.
func TestPanelAllowPartialMemberFailureProceedsToSynthesis(t *testing.T) {
	// failFirst=1: exactly one of the two concurrent members fails; the other
	// succeeds. Under allow_partial this is a partial failure, not a panel
	// failure.
	memberHandler := &flakyMemberHandler{raw: `{"verdict":"approved","findings":[]}`, failFirst: 1}
	ctrl, repo := panelAllowPartialFixture(t, "wfr-panel-allow-partial", definition.PanelFailurePolicyAllowPartial, memberHandler, `{"dispositions":[],"summary":"ok"}`)
	got, err := ctrl.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run status = %q, want succeeded (partial member failure must not fail the panel)", got.Status)
	}
	failed, succeeded, total := countPanelAttempts(t, repo, ctrl.RunID)
	if failed != 0 || succeeded != 1 || total != 1 {
		t.Fatalf("panel attempts failed=%d succeeded=%d total=%d, want 0 failed + 1 succeeded on the first attempt (no re-entry)", failed, succeeded, total)
	}
}

// TestPanelAllowPartialAllMembersFailSettlesFailed is the contrast case: even
// under allow_partial, the attempt fails when ALL members fail (there is no
// successful member to synthesize from).
func TestPanelAllowPartialAllMembersFailSettlesFailed(t *testing.T) {
	memberHandler := &flakyMemberHandler{raw: `{"verdict":"approved","findings":[]}`, failFirst: 1 << 30}
	ctrl, _ := panelAllowPartialFixture(t, "wfr-panel-allow-partial-allfail", definition.PanelFailurePolicyAllowPartial, memberHandler, `{"dispositions":[],"summary":"ok"}`)
	got, err := ctrl.Run(context.Background())
	if err == nil || got.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run = %+v err=%v; want failed when ALL panel members fail even under allow_partial", got, err)
	}
}

// TestPanelRequireAllMemberFailureFailsAttempt is the contrast case pinning
// the legacy default: under require_all, a single member failure fails the
// attempt (which then re-enters through the self-loop), so the panel does NOT
// proceed to synthesis with the surviving member.
func TestPanelRequireAllMemberFailureFailsAttempt(t *testing.T) {
	memberHandler := &flakyMemberHandler{raw: `{"verdict":"approved","findings":[]}`, failFirst: 1}
	ctrl, repo := panelAllowPartialFixture(t, "wfr-panel-require-all", definition.PanelFailurePolicyRequireAll, memberHandler, `{"dispositions":[],"summary":"ok"}`)
	got, err := ctrl.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	// With require_all the single member failure fails the first attempt; the
	// self-loop re-enters and the second attempt (both members succeed) wins.
	if got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run status = %q, want succeeded after re-entry", got.Status)
	}
	failed, succeeded, total := countPanelAttempts(t, repo, ctrl.RunID)
	if failed != 1 || succeeded != 1 || total != 2 {
		t.Fatalf("panel attempts failed=%d succeeded=%d total=%d, want 1 failed (require_all) + 1 succeeded after re-entry", failed, succeeded, total)
	}
}
