package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	coordledger "github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

type completedPanelHandler struct{}

func (completedPanelHandler) Invoke(context.Context, runtime.Request) (json.RawMessage, error) {
	return json.RawMessage(`{"ok":true}`), nil
}

func TestBuildPanelAttemptPinsEachMemberBindingAndDeadline(t *testing.T) {
	now := time.Date(2026, time.August, 9, 3, 0, 0, 0, time.UTC)
	parentDeadline := now.Add(10 * time.Minute)
	step := definition.Step{
		ID: "review", Kind: "agent_panel", Context: []definition.ContextBinding{{From: "inputs.task", As: "task", MaxBytes: 1024}},
		Panel: &definition.AgentPanel{FailurePolicy: "require_all", Members: []definition.PanelMember{
			{ID: "security", Agent: "panel-reviewer", Skill: "secure-change", Template: "security", OutputSchema: "report"},
			{ID: "correctness", Agent: "panel-reviewer", Skill: "bug-audit", Template: "correctness", OutputSchema: "report"},
		}},
	}
	snapshot, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{
		PanelBindings: map[string]workflowledger.PanelBindingSnapshot{
			"review/security":    {AgentName: "panel-reviewer", AgentDigest: "sha256:security", ProviderName: "deepseek", Model: "deepseek-v4-flash"},
			"review/correctness": {AgentName: "panel-reviewer", AgentDigest: "sha256:correctness", ProviderName: "zai", Model: "glm-5.2"},
		},
		Templates: map[string]workflowledger.RefSnapshot{
			"security": {Bytes: []byte("Review {{inputs.task}}.")}, "correctness": {Bytes: []byte("Review {{inputs.task}}.")},
		},
		Schemas: map[string]workflowledger.RefSnapshot{"report": {Bytes: []byte(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, &linearRunner{}, &compiler.CompiledWorkflow{Steps: []definition.Step{step}}, nil, map[string]any{"task": "change"}, "wfr-panel-pins", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetTimeSource(func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	attempt, err := ctrl.buildPanelAttempt(context.Background(), workflowledger.RunSnapshot{RunID: ctrl.RunID, DeadlineAt: &parentDeadline}, step, nil)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.PanelExecution == nil || len(attempt.PanelExecution.Members) != 2 {
		t.Fatalf("panel execution = %+v", attempt.PanelExecution)
	}
	got := attempt.PanelExecution.Members
	if got[0].Work.Provider != "deepseek" || got[0].Work.Model != "deepseek-v4-flash" || got[1].Work.Provider != "zai" || got[1].Work.Model != "glm-5.2" {
		t.Fatalf("pinned bindings = %#v", got)
	}
	for _, member := range got {
		if !member.Work.DeadlineAt.Equal(parentDeadline) || member.Work.Timeout != 10*time.Minute {
			t.Fatalf("member %q deadline work = %+v", member.MemberID, member.Work)
		}
		if !member.Work.Policy.NoRetry || !member.Work.Policy.FailInterrupted || !member.Work.WorkLimits.DeadlineAt.Equal(parentDeadline) {
			t.Fatalf("member %q policy and work limits = %+v", member.MemberID, member.Work)
		}
	}
}

// TestPanelMemberWorkLimitsHonorStepMaxTurns pins that buildPanelAttempt
// applies the step's max_turns knob (0 default = unlimited) to every member's
// WorkLimits instead of a hardcoded turn cap.
func TestPanelMemberWorkLimitsHonorStepMaxTurns(t *testing.T) {
	cases := []struct {
		name     string
		maxTurns int
		want     int
	}{
		{"unset defaults to unlimited", 0, 0},
		{"positive bounds member turns", 7, 7},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, time.August, 9, 3, 0, 0, 0, time.UTC)
			step := definition.Step{
				ID: "review", Kind: "agent_panel", MaxTurns: tc.maxTurns,
				Context: []definition.ContextBinding{{From: "inputs.task", As: "task", MaxBytes: 1024}},
				Panel: &definition.AgentPanel{FailurePolicy: "require_all", Members: []definition.PanelMember{
					{ID: "correctness", Agent: "panel-reviewer", Skill: "bug-audit", Template: "correctness", OutputSchema: "report"},
				}},
			}
			snapshot, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{
				PanelBindings: map[string]workflowledger.PanelBindingSnapshot{
					"review/correctness": {AgentName: "panel-reviewer", AgentDigest: "sha256:correctness", ProviderName: "zai", Model: "glm-5.2"},
				},
				Templates: map[string]workflowledger.RefSnapshot{"correctness": {Bytes: []byte("Review {{inputs.task}}.")}},
				Schemas:   map[string]workflowledger.RefSnapshot{"report": {Bytes: []byte(`{"type":"object"}`)}},
			})
			if err != nil {
				t.Fatal(err)
			}
			repo := workflowledger.NewMemoryRepository()
			ctrl, err := NewLinearController(repo, &linearRunner{}, &compiler.CompiledWorkflow{Steps: []definition.Step{step}}, nil, map[string]any{"task": "change"}, fmt.Sprintf("wfr-panel-turns-%d", i), snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if err := ctrl.SetTimeSource(func() time.Time { return now }); err != nil {
				t.Fatal(err)
			}
			deadline := now.Add(10 * time.Minute)
			attempt, err := ctrl.buildPanelAttempt(context.Background(), workflowledger.RunSnapshot{RunID: ctrl.RunID, DeadlineAt: &deadline}, step, nil)
			if err != nil {
				t.Fatal(err)
			}
			if attempt.PanelExecution == nil || len(attempt.PanelExecution.Members) != 1 {
				t.Fatalf("panel execution = %+v", attempt.PanelExecution)
			}
			if got := attempt.PanelExecution.Members[0].Work.WorkLimits.MaxTurns; got != tc.want {
				t.Fatalf("member WorkLimits.MaxTurns = %d, want %d", got, tc.want)
			}
		})
	}
}

// panelStepFixture builds a controller whose workflow starts with an
// agent_panel step. It returns the controller, the workflow repository, and
// the coordinator ledger so tests can assert settlement and dispatch.
func panelStepFixture(t *testing.T, runID string) (*LinearController, workflowledger.Repository, coordledger.LedgerRepository, definition.Step) {
	t.Helper()
	step := definition.Step{
		ID: "review", Kind: "agent_panel", Context: []definition.ContextBinding{{From: "inputs.task", As: "task", MaxBytes: 1024}},
		Panel: &definition.AgentPanel{FailurePolicy: "require_all", Members: []definition.PanelMember{
			{ID: "security", Agent: "panel-reviewer", Skill: "secure-change", Template: "security", OutputSchema: "report"},
			{ID: "correctness", Agent: "panel-reviewer", Skill: "bug-audit", Template: "correctness", OutputSchema: "report"},
		}},
	}
	snapshot, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{
		PanelBindings: map[string]workflowledger.PanelBindingSnapshot{
			"review/security":    {AgentName: "panel-reviewer", AgentDigest: strings.Repeat("a", 64), ProviderName: "deepseek", Model: "deepseek-v4-flash"},
			"review/correctness": {AgentName: "panel-reviewer", AgentDigest: strings.Repeat("b", 64), ProviderName: "zai", Model: "glm-5.2"},
		},
		Templates: map[string]workflowledger.RefSnapshot{"security": {Bytes: []byte("Review {{inputs.task}}.")}, "correctness": {Bytes: []byte("Review {{inputs.task}}.")}},
		Schemas:   map[string]workflowledger.RefSnapshot{"report": {Bytes: []byte(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := runtime.New(runtime.Policy{})
	if err := dispatcher.Register(runtime.Subagent, "panel-reviewer", completedPanelHandler{}); err != nil {
		t.Fatal(err)
	}
	coordLedger := coordledger.NewMemoryLedgerRepository()
	coord := coordinator.New(coordLedger, subagents.New(dispatcher, subagents.Policy{Workers: 2}))
	repo := workflowledger.NewMemoryRepository()
	wf := &compiler.CompiledWorkflow{Name: "panel", InitialStep: step.ID, Steps: []definition.Step{step}}
	ctrl, err := NewLinearController(repo, NewCoordinatorRunner(coord), wf, nil, map[string]any{"task": "change"}, runID, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return ctrl, repo, coordLedger, step
}

// TestPanelStepFailsClosedOnInvalidMemberReport: a member report that does
// not decode against the strict panel-review schema (completedPanelHandler's
// stub output is a generic "job completed" payload, not a valid verdict/
// findings report) must fail the run closed rather than leave the attempt
// running forever (G9) or synthesize from unvalidated data. Member dispatch
// itself does happen - this is a decode/validation failure downstream of a
// real coordinator run, not a routing refusal.
func TestPanelStepFailsClosedOnInvalidMemberReport(t *testing.T) {
	ctrl, _, coordLedger, _ := panelStepFixture(t, "wfr-panel-fail-closed")
	run, err := ctrl.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "panel member") {
		t.Fatalf("Run error = %v, want a panel member report failure", err)
	}
	if !workflowledger.IsTerminalRunStatus(run.Status) || run.Status != workflowledger.RunStatusFailed {
		t.Fatalf("Run status = %q, want terminal failed", run.Status)
	}
	runs, err := coordLedger.ListRuns(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) == 0 {
		t.Fatal("member coordinator runs = 0, want at least one member actually dispatched")
	}
}
