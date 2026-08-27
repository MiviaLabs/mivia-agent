package controller

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	coordledger "github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// flakyMemberHandler fails the first failFirst invocations (each panel attempt
// invokes every member once, so failFirst=2 fails one whole two-member
// attempt), then returns raw. The counter is atomic because members of one
// attempt run concurrently.
type flakyMemberHandler struct {
	raw       string
	failFirst int32
	calls     int32
}

func (h *flakyMemberHandler) Invoke(context.Context, runtime.Request) (json.RawMessage, error) {
	if atomic.AddInt32(&h.calls, 1) <= h.failFirst {
		return nil, errors.New("member provider overloaded")
	}
	return json.RawMessage(h.raw), nil
}

// panelRetryFixture builds a controller for an agent_panel step whose
// declared on_failure is the step itself (a self-loop: a failed panel attempt
// re-enters the panel with a fresh attempt), with members wired to the given
// handler and a synthesizer that always succeeds. The workflow carries the
// given limits so tests can configure the re-entry budget.
func panelRetryFixture(t *testing.T, runID string, memberHandler runtime.Handler, synthesisOutput string, limits definition.Limits) (*LinearController, workflowledger.Repository) {
	t.Helper()
	step := definition.Step{
		ID: "review", Kind: "agent_panel", Agent: "review-synthesizer", Skill: "review-synthesis",
		Template: "synth", OutputSchema: "synthschema",
		Context:   []definition.ContextBinding{{From: "inputs.task", As: "task", MaxBytes: 1024}},
		OnFailure: "review",
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
	if err := dispatcher.Register(runtime.Subagent, "panel-reviewer", memberHandler); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Register(runtime.Subagent, "review-synthesizer", fixedOutputHandler{raw: synthesisOutput}); err != nil {
		t.Fatal(err)
	}
	coordLedger := coordledger.NewMemoryLedgerRepository()
	coord := coordinator.New(coordLedger, subagents.New(dispatcher, subagents.Policy{Workers: 4}))
	repo := workflowledger.NewMemoryRepository()
	wf := &definition.CompiledWorkflow{
		Name: "panel-retry", InitialStep: step.ID, Steps: []definition.Step{step},
		Limits: limits,
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

func countPanelAttempts(t *testing.T, repo workflowledger.Repository, runID string) (failed, succeeded, total int) {
	t.Helper()
	attempts, err := repo.ListStepAttempts(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range attempts {
		if a.StepID != "review" {
			continue
		}
		total++
		switch a.Status {
		case workflowledger.AttemptStatusFailed:
			failed++
		case workflowledger.AttemptStatusSucceeded:
			succeeded++
		}
	}
	return failed, succeeded, total
}

// TestPanelRetryReentersOnMemberFailure: a panel whose members fail once
// re-enters the panel (a fresh attempt) through its declared self-loop
// on_failure instead of failing the run, then succeeds. This exercises the
// full Run loop: the repairable failure leaves the agent_panel step active
// after Advance, and Run must loop (not stop at the Wave-4 sentinel) to admit
// the next attempt.
func TestPanelRetryReentersOnMemberFailure(t *testing.T) {
	memberHandler := &flakyMemberHandler{raw: `{"verdict":"approved","findings":[]}`, failFirst: 2}
	ctrl, repo := panelRetryFixture(t, "wfr-panel-retry-reenter", memberHandler, `{"dispositions":[],"summary":"ok"}`, definition.Limits{MaxStepAttempts: 16})
	got, err := ctrl.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run status = %q, want succeeded", got.Status)
	}
	failed, succeeded, total := countPanelAttempts(t, repo, ctrl.RunID)
	if failed != 1 || succeeded != 1 || total != 2 {
		t.Fatalf("panel attempts failed=%d succeeded=%d total=%d, want 1 failed + 1 succeeded", failed, succeeded, total)
	}
}

// TestPanelRetryBoundedByDefaultBudget: a permanently failing panel re-enters
// until the on_failure re-entry budget (default 3) is spent, then the run
// fails instead of spinning forever.
func TestPanelRetryBoundedByDefaultBudget(t *testing.T) {
	memberHandler := &flakyMemberHandler{raw: `{"verdict":"approved","findings":[]}`, failFirst: 1 << 30}
	ctrl, repo := panelRetryFixture(t, "wfr-panel-retry-bounded", memberHandler, `{"dispositions":[],"summary":"ok"}`, definition.Limits{MaxStepAttempts: 16})
	got, err := ctrl.Run(context.Background())
	if err == nil || got.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run = %+v err=%v; want failed once the panel re-entry budget is spent", got, err)
	}
	failed, succeeded, total := countPanelAttempts(t, repo, ctrl.RunID)
	if total != defaultMaxOnFailureReentries || failed != total || succeeded != 0 {
		t.Fatalf("panel attempts failed=%d succeeded=%d total=%d, want %d failed and no success", failed, succeeded, total, defaultMaxOnFailureReentries)
	}
}

// TestPanelRetryBudgetConfigurable: the workflow's max_on_failure_reentries
// limit raises the panel re-entry budget above the default, so a panel that
// fails three attempts recovers on the fourth instead of hard-failing.
func TestPanelRetryBudgetConfigurable(t *testing.T) {
	memberHandler := &flakyMemberHandler{raw: `{"verdict":"approved","findings":[]}`, failFirst: 6}
	ctrl, repo := panelRetryFixture(t, "wfr-panel-retry-configurable", memberHandler, `{"dispositions":[],"summary":"ok"}`, definition.Limits{MaxStepAttempts: 16, MaxOnFailureReentries: 5})
	got, err := ctrl.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run status = %q, want succeeded", got.Status)
	}
	failed, succeeded, total := countPanelAttempts(t, repo, ctrl.RunID)
	if failed != 3 || succeeded != 1 || total != 4 {
		t.Fatalf("panel attempts failed=%d succeeded=%d total=%d, want 3 failed re-entries within a budget of 5 then success", failed, succeeded, total)
	}
}

// TestPanelRetryBudgetOneDisablesReentry: a budget of 1 means no re-entry at
// all - the first failed panel attempt fails the run, exactly the pre-retry
// behavior.
func TestPanelRetryBudgetOneDisablesReentry(t *testing.T) {
	memberHandler := &flakyMemberHandler{raw: `{"verdict":"approved","findings":[]}`, failFirst: 2}
	ctrl, repo := panelRetryFixture(t, "wfr-panel-retry-budget-one", memberHandler, `{"dispositions":[],"summary":"ok"}`, definition.Limits{MaxStepAttempts: 16, MaxOnFailureReentries: 1})
	got, err := ctrl.Run(context.Background())
	if err == nil || got.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run = %+v err=%v; want failed with a re-entry budget of 1", got, err)
	}
	failed, succeeded, total := countPanelAttempts(t, repo, ctrl.RunID)
	if total != 1 || failed != 1 || succeeded != 0 {
		t.Fatalf("panel attempts failed=%d succeeded=%d total=%d, want exactly 1 failed attempt", failed, succeeded, total)
	}
}
