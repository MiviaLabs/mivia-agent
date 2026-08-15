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
		wantErr: "failure_policy must be \"require_all\" or \"allow_partial\"",
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
	// FINDING E3 (DC-11): the provider registry resolves names via
	// strings.ToLower(strings.TrimSpace(name)) and binding resolution
	// lowercases the provider, so a case or whitespace variant of an
	// already-bound provider is the same provider. Without provider
	// normalization these pairs bypass require_distinct_bindings. Models stay
	// case-sensitive (NormalizeModelName only trims; selectableModel matches
	// profile.Name == model exactly), so the duplicate check must not lowercase
	// the model.
	{
		name: "duplicate provider model binding case-insensitive provider",
		mutate: func(s *definition.Step) {
			s.Panel.Members[1].Provider = "DeepSeek"
			s.Panel.Members[1].Model = s.Panel.Members[0].Model
		},
		wantErr: "duplicate provider/model binding",
	},
	{
		name: "duplicate provider model binding whitespace variant",
		mutate: func(s *definition.Step) {
			s.Panel.Members[1].Provider = "  deepseek "
			s.Panel.Members[1].Model = s.Panel.Members[0].Model
		},
		wantErr: "duplicate provider/model binding",
	},
	// Bug-audit regression: controller.sourceKeyDigest (Wave 5) concatenates
	// MemberID and FindingID with 0x00/0x1e separators. A member ID carrying
	// one of those bytes could collide two different canonical source keys
	// onto the same digest. Member IDs are workflow-definition-authored, so
	// this is rejected here, at compile time.
	{
		name:    "member id contains a control byte",
		mutate:  func(s *definition.Step) { s.Panel.Members[0].ID = "sec\x1eurity" },
		wantErr: "contains a control character",
	},
}

// TestValidatePanelsMemberRules exercises the static agent_panel validation
// rules directly. Compile cannot be used for these cases: agent_panel steps
// are rejected as non-executable before panel validation runs (FINDING E6).
func TestValidatePanelsMemberRules(t *testing.T) {
	t.Run("valid panel passes member validation", func(t *testing.T) {
		if err := validatePanels(newAgentPanelWorkflow()); err != nil {
			t.Fatalf("validatePanels() error = %v", err)
		}
	})

	for _, tc := range panelValidationCases {
		t.Run(tc.name, func(t *testing.T) {
			wf := newAgentPanelWorkflow()
			tc.mutate(&wf.Steps[0])
			err := validatePanels(wf)
			if len(err) == 0 || !strings.Contains(strings.Join(err, "; "), tc.wantErr) {
				t.Fatalf("validatePanels() error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

// TestValidatePanelsAllowsCaseDistinctModels is the FINDING E3 over-reach
// regression: models are case-sensitive (config.NormalizeModelName only trims,
// selectableModel matches profile.Name == model exactly, and the model string
// reaches the provider API unmodified), so a panel binding one provider to two
// case-distinct catalog models - e.g. 'GLM-5.2' vs 'glm-5.2' - must compile.
// Lowercasing the model in the duplicate-binding key rejected legal
// case-distinct models of one provider that the runtime treats as different,
// violating require_distinct_bindings' actual contract.
func TestValidatePanelsAllowsCaseDistinctModels(t *testing.T) {
	wf := newAgentPanelWorkflow()
	wf.Steps[0].Panel.Members[1].Provider = wf.Steps[0].Panel.Members[0].Provider
	wf.Steps[0].Panel.Members[1].Model = strings.ToUpper(wf.Steps[0].Panel.Members[0].Model)
	if err := validatePanels(wf); err != nil {
		t.Fatalf("validatePanels() error = %v, want same provider with case-distinct models to compile", err)
	}
}

func TestCompileAcceptsAgentPanel(t *testing.T) {
	if _, err := Compile(newAgentPanelWorkflow()); err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
}

// TestCompileForResumeRejectsAgentPanelSnapshot is the resume-path regression:
// a snapshot containing an agent_panel step must be refused at CompileForResume
// with the same clear error. Resume is recovery, not admission, but a run whose
// next step cannot be executed must not be resumed into a guaranteed mid-flight
// failure; refusing at resume surfaces the reason before any work runs.
func TestCompileForResumeAcceptsAgentPanelSnapshot(t *testing.T) {
	if _, err := CompileForResume(newAgentPanelWorkflow()); err != nil {
		t.Fatalf("CompileForResume() error = %v", err)
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
	if wf.Steps[0].Kind != "agent_panel" {
		t.Fatalf("step kind = %q, want agent_panel", wf.Steps[0].Kind)
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
