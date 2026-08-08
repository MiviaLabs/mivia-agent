package controller

import (
	"context"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

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
