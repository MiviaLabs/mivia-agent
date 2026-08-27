package controller

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// newEvidenceLoopControllerWithOutput is newEvidenceLoopController with a
// caller-chosen implement output, so a loop-exhaustion test can make the
// salvaged attempt's stored content a panel member/synthesis report without
// building full agent_panel step machinery - decodeSalvagedFindings only
// looks at the stored bytes, never the step kind that produced them.
func newEvidenceLoopControllerWithOutput(t *testing.T, name, runID string, output json.RawMessage) *LinearController {
	t.Helper()
	wf := &definition.WorkflowFile{
		Version: 1, Name: name, InitialStep: "verify",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Limits: definition.Limits{MaxStepAttempts: 0},
		Steps: []definition.Step{
			{ID: "verify", Kind: "evidence_gate", Verifier: "always-fails", OnFailure: "failure"},
			{ID: "implement", Kind: "agent", Agent: "dev", OnFailure: "failure"},
		},
		Transitions: []definition.Transition{
			{From: "implement", To: "verify", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "verify", To: "success", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"status": "passed"}}},
			{From: "verify", To: "implement", Match: definition.MatchCriteria{Status: "failed"}, Loop: "repair", MaxIterations: 2},
		},
	}
	compiled, err := definition.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	cat := definition.NewCatalogue()
	if err := cat.Register(fixedVerifierProfile{
		name:   "always-fails",
		result: definition.Result{Status: "failed", Checks: []definition.Check{{Name: "test", Status: "failed", Class: "source"}}},
	}); err != nil {
		t.Fatal(err)
	}
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{"implement#*": output}}
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, runner, compiled, map[string]StepRuntime{
		"implement": {Agent: agents.ResolvedAgent{Name: "dev"}},
	}, map[string]any{"task": "x"}, runID, []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetVerifiers(cat); err != nil {
		t.Fatal(err)
	}
	return ctrl
}

// TestLoopExhaustedFindingsSurfacePanelMemberVerdict is the regression for the
// review_repair diagnosability gap: an exhausted loop whose salvaged attempt
// is a panel member report (verdict + findings) must surface the verdict and
// the leading finding's title/severity in the terminal error, not just an
// opaque content-addressed ref a human can no longer resolve once the run's
// worktree is reused.
func TestLoopExhaustedFindingsSurfacePanelMemberVerdict(t *testing.T) {
	member := PanelMemberReport{
		Verdict: PanelVerdictChangesRequested,
		Findings: []PanelFinding{
			{ID: "f1", Title: "missing nil check", Severity: "high", Description: "panics on empty input"},
		},
	}
	raw, err := json.Marshal(member)
	if err != nil {
		t.Fatal(err)
	}
	ctrl := newEvidenceLoopControllerWithOutput(t, "evidence-loop-panel-member", "wfr-evidence-loop-panel-member", raw)
	_, runErr := ctrl.Run(context.Background())
	if runErr == nil {
		t.Fatal("run succeeded; want loop-exhausted failure")
	}
	var loopErr *loopExhaustedError
	if !errors.As(runErr, &loopErr) {
		t.Fatalf("error %v does not carry the structured loop-exhaustion hint", runErr)
	}
	if len(loopErr.Findings) == 0 {
		t.Fatalf("loop hint carries no decoded findings; want the panel member verdict surfaced")
	}
	joined := strings.Join(loopErr.Findings, "; ")
	for _, want := range []string{"changes_requested", "missing nil check", "high"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("findings = %q, want it to contain %q", joined, want)
		}
	}
	if !strings.Contains(runErr.Error(), "(last review verdicts:") {
		t.Fatalf("error = %v; want the decoded findings named in the recovery hint", runErr)
	}
}

// TestLoopExhaustedFindingsSurfacePanelFinalReport is the synthesis-report
// counterpart: a salvaged review_panel synthesis output (host_verdict +
// dispositions) must surface the host verdict and how many findings were
// included, not just a ref.
func TestLoopExhaustedFindingsSurfacePanelFinalReport(t *testing.T) {
	final := PanelFinalReport{
		HostVerdict: PanelVerdictChangesRequested,
		Dispositions: []PanelSourceDisposition{
			{MemberID: "m1", FindingID: "f1", Disposition: PanelDispositionIncluded, FinalFindingID: "f1"},
			{MemberID: "m2", FindingID: "f2", Disposition: PanelDispositionDuplicate, FinalFindingID: "f1"},
		},
	}
	raw, err := json.Marshal(final)
	if err != nil {
		t.Fatal(err)
	}
	ctrl := newEvidenceLoopControllerWithOutput(t, "evidence-loop-panel-final", "wfr-evidence-loop-panel-final", raw)
	_, runErr := ctrl.Run(context.Background())
	if runErr == nil {
		t.Fatal("run succeeded; want loop-exhausted failure")
	}
	var loopErr *loopExhaustedError
	if !errors.As(runErr, &loopErr) {
		t.Fatalf("error %v does not carry the structured loop-exhaustion hint", runErr)
	}
	joined := strings.Join(loopErr.Findings, "; ")
	for _, want := range []string{"changes_requested", "1/2 findings included"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("findings = %q, want it to contain %q", joined, want)
		}
	}
}

// TestLoopExhaustedFindingsAbsentForNonPanelSalvage pins the negative case:
// an exhausted loop whose salvaged attempt is an ordinary (non-panel) output
// must not fabricate a findings line - decodeSalvagedFindings degrades
// silently rather than misreporting an unrelated JSON shape as a verdict.
func TestLoopExhaustedFindingsAbsentForNonPanelSalvage(t *testing.T) {
	ctrl := newEvidenceLoopControllerWithOutput(t, "evidence-loop-plain", "wfr-evidence-loop-plain", json.RawMessage(`{"summary":"repair"}`))
	_, runErr := ctrl.Run(context.Background())
	if runErr == nil {
		t.Fatal("run succeeded; want loop-exhausted failure")
	}
	var loopErr *loopExhaustedError
	if !errors.As(runErr, &loopErr) {
		t.Fatalf("error %v does not carry the structured loop-exhaustion hint", runErr)
	}
	if len(loopErr.Findings) != 0 {
		t.Fatalf("findings = %v, want none for a non-panel salvaged output", loopErr.Findings)
	}
	if strings.Contains(runErr.Error(), "(last review verdicts:") {
		t.Fatalf("error = %v; must not claim review verdicts for a non-panel salvaged output", runErr)
	}
}
