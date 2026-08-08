package compiler

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

type panelValidationCase struct {
	name    string
	mutate  func(*definition.Step)
	wantErr string
}

var panelValidationCases = []panelValidationCase{
	{
		name:    "too few members",
		mutate:  func(s *definition.Step) { s.Panel.Members = s.Panel.Members[:1] },
		wantErr: "between 2 and 4 members",
	},
	{
		name: "too many members",
		mutate: func(s *definition.Step) {
			s.Panel.Members = append(s.Panel.Members, s.Panel.Members[0], s.Panel.Members[1], s.Panel.Members[0])
		},
		wantErr: "between 2 and 4 members",
	},
	{
		name:    "empty member ID",
		mutate:  func(s *definition.Step) { s.Panel.Members[0].ID = " " },
		wantErr: "member[0]: id is required",
	},
	{
		name:    "duplicate member ID",
		mutate:  func(s *definition.Step) { s.Panel.Members[1].ID = s.Panel.Members[0].ID },
		wantErr: "duplicate member id",
	},
	{
		name:    "missing member agent",
		mutate:  func(s *definition.Step) { s.Panel.Members[0].Agent = "" },
		wantErr: "agent is required",
	},
	{
		name:    "missing provider",
		mutate:  func(s *definition.Step) { s.Panel.Members[0].Provider = "" },
		wantErr: "provider and model must both be set",
	},
	{
		name:    "missing model",
		mutate:  func(s *definition.Step) { s.Panel.Members[0].Model = "" },
		wantErr: "provider and model must both be set",
	},
	{
		name:    "missing skill",
		mutate:  func(s *definition.Step) { s.Panel.Members[0].Skill = "" },
		wantErr: "skill is required",
	},
	{
		name:    "missing template",
		mutate:  func(s *definition.Step) { s.Panel.Members[0].Template = "" },
		wantErr: "template is required",
	},
	{
		name:    "missing output schema",
		mutate:  func(s *definition.Step) { s.Panel.Members[0].OutputSchema = "" },
		wantErr: "output_schema is required",
	},
	{
		name:    "unsupported failure policy",
		mutate:  func(s *definition.Step) { s.Panel.FailurePolicy = "best_effort" },
		wantErr: "failure_policy must be \"require_all\"",
	},
	{
		name:    "distinct bindings required",
		mutate:  func(s *definition.Step) { s.Panel.RequireDistinctBindings = false },
		wantErr: "require_distinct_bindings must be true",
	},
	{
		name: "duplicate provider model binding",
		mutate: func(s *definition.Step) {
			s.Panel.Members[1].Provider = s.Panel.Members[0].Provider
			s.Panel.Members[1].Model = s.Panel.Members[0].Model
		},
		wantErr: "duplicate provider/model binding",
	},
}

func TestCompile_AgentPanel(t *testing.T) {
	t.Run("valid panel compiles", func(t *testing.T) {
		if _, err := Compile(newAgentPanelWorkflow()); err != nil {
			t.Fatalf("Compile() error = %v", err)
		}
	})

	for _, tc := range panelValidationCases {
		t.Run(tc.name, func(t *testing.T) {
			wf := newAgentPanelWorkflow()
			tc.mutate(&wf.Steps[0])
			_, err := Compile(wf)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Compile() error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestCompileRejectsPanelOnNonPanelStep(t *testing.T) {
	wf := &definition.WorkflowFile{
		Version:     1,
		Name:        "incompatible-panel",
		InitialStep: "approve",
		Steps:       []definition.Step{{ID: "approve", Kind: "human_gate", Panel: &definition.AgentPanel{}}},
		Transitions: []definition.Transition{{From: "approve", To: "success", Match: definition.MatchCriteria{Status: "approved"}}},
	}
	_, err := Compile(wf)
	if err == nil || !strings.Contains(err.Error(), "only valid for kind") {
		t.Fatalf("Compile() error = %v", err)
	}
}

func TestCompile_AgentPanelDecodedDefinition(t *testing.T) {
	data := []byte(`version = 1
name = "decoded-panel"
initial_step = "review"

[[steps]]
id = "review"
kind = "agent_panel"
agent = "review-synthesizer"
skill = "review-synthesis"
template = "templates/synthesis.md"
output_schema = "schemas/final.json"

[steps.panel]
failure_policy = "require_all"
require_distinct_bindings = true

[[steps.panel.members]]
id = "correctness"
agent = "panel-reviewer"
provider = "deepseek"
model = "deepseek-v4-flash"
skill = "bug-audit"
template = "templates/correctness.md"
output_schema = "schemas/panel.json"

[[steps.panel.members]]
id = "security"
agent = "panel-reviewer"
provider = "openrouter"
model = "tencent/hy3-preview"
skill = "secure-change"
template = "templates/security.md"
output_schema = "schemas/panel.json"

[[transitions]]
from = "review"
to = "success"
match = { status = "succeeded" }
`)
	wf, _, err := definition.ParseWorkflowTOML(data, "decoded-panel.toml")
	if err != nil {
		t.Fatalf("ParseWorkflowTOML() error = %v", err)
	}
	if _, err := Compile(&wf); err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
}

func newAgentPanelWorkflow() *definition.WorkflowFile {
	return &definition.WorkflowFile{
		Version:     1,
		Name:        "agent-panel",
		InitialStep: "review",
		Steps: []definition.Step{{
			ID:    "review",
			Kind:  "agent_panel",
			Agent: "review-synthesizer",
			Panel: &definition.AgentPanel{
				FailurePolicy:           "require_all",
				RequireDistinctBindings: true,
				Members: []definition.PanelMember{
					{ID: "correctness", Agent: "panel-reviewer", Provider: "deepseek", Model: "deepseek-v4-flash", Skill: "bug-audit", Template: "templates/correctness.md", OutputSchema: "schemas/panel.json"},
					{ID: "security", Agent: "panel-reviewer", Provider: "openrouter", Model: "tencent/hy3-preview", Skill: "secure-change", Template: "templates/security.md", OutputSchema: "schemas/panel.json"},
				},
			},
		}},
		Transitions: []definition.Transition{{From: "review", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}}},
	}
}
