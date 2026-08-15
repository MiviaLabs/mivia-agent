package definition

import "testing"

const stepDefaultsUnitFixture = `version = 1
name = "delivery"
initial_step = "plan"

[inputs.task]
type = "string"
required = true
max_bytes = 100

[step_defaults]
kind = "agent"
agent = "worker"
skill = "delivery-skill"
template = "templates/plan.md"
output_schema = "schemas/result.json"
on_failure = "failure"
max_turns = 5
context = [{ from = "inputs.task", as = "task", max_bytes = 100 }]

[[steps]]
id = "plan"

[[steps]]
id = "verify"
kind = "evidence_gate"
command = { check = "tests pass", program = "go", args = ["test", "./..."] }

[[transitions]]
from = "plan"
to = "verify"
match = { status = "succeeded" }

[[transitions]]
from = "verify"
to = "success"
match = { status = "succeeded" }
`

func TestParseWorkflowTOML_StepDefaultsFillsEmptyAgentStepFields(t *testing.T) {
	wf, _, err := ParseWorkflowTOML([]byte(stepDefaultsUnitFixture), "delivery.toml")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	plan := wf.Steps[0]
	if plan.Kind != "agent" || plan.Agent != "worker" || plan.Skill != "delivery-skill" ||
		plan.Template != "templates/plan.md" || plan.OutputSchema != "schemas/result.json" ||
		plan.OnFailure != "failure" || plan.MaxTurns != 5 {
		t.Fatalf("plan step not filled from step_defaults: %+v", plan)
	}
	if len(plan.Context) != 1 || plan.Context[0].As != "task" {
		t.Fatalf("plan step context not filled from step_defaults: %+v", plan.Context)
	}
}

func TestParseWorkflowTOML_StepDefaultsClearedAfterParse(t *testing.T) {
	wf, _, err := ParseWorkflowTOML([]byte(stepDefaultsUnitFixture), "delivery.toml")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if wf.StepDefaults != nil {
		t.Fatalf("wf.StepDefaults = %+v, want nil after parse", wf.StepDefaults)
	}
}

func TestParseWorkflowTOML_NoStepDefaultsTableIsNoop(t *testing.T) {
	const body = `version = 1
name = "delivery"
initial_step = "plan"

[[steps]]
id = "plan"
kind = "agent"
agent = "worker"

[[transitions]]
from = "plan"
to = "success"
match = { status = "succeeded" }
`
	wf, err := decodeOnly(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if wf.StepDefaults != nil {
		t.Fatalf("StepDefaults = %+v, want nil for a file without [step_defaults]", wf.StepDefaults)
	}
	if wf.Steps[0].Agent != "worker" {
		t.Fatalf("step mutated by applyStepDefaults with no table present: %+v", wf.Steps[0])
	}
}

// decodeOnly parses via the public entry point; kept as a thin wrapper so
// this file's intent (desugar-is-a-noop) reads clearly at the call site.
func decodeOnly(body string) (WorkflowFile, error) {
	wf, _, err := ParseWorkflowTOML([]byte(body), "delivery.toml")
	return wf, err
}

// TestApplyStepDefaults_MaxTurnsZeroCannotOverrideToUnlimited pins the
// documented limitation: once step_defaults.max_turns is positive, a step
// cannot opt back into "unlimited" (0) because 0 is Step.MaxTurns' own
// zero-value sentinel for that, indistinguishable from "field omitted." This
// test exists so a future fix (e.g. a sentinel value) is a deliberate,
// visible change to this assertion, not a silent behavior drift.
func TestApplyStepDefaults_MaxTurnsZeroCannotOverrideToUnlimited(t *testing.T) {
	wf := WorkflowFile{
		StepDefaults: &StepDefaults{Kind: "agent", Agent: "worker", MaxTurns: 5},
		Steps:        []Step{{ID: "a", MaxTurns: 0}},
	}
	if err := applyStepDefaults(&wf); err != nil {
		t.Fatalf("applyStepDefaults: %v", err)
	}
	if wf.Steps[0].MaxTurns != 5 {
		t.Fatalf("max_turns = %d, want 5 (documented limitation: step's explicit 0 cannot override the default)", wf.Steps[0].MaxTurns)
	}
}

func TestApplyStepDefaults_RejectsUnknownKind(t *testing.T) {
	wf := WorkflowFile{
		StepDefaults: &StepDefaults{Kind: "not-a-kind"},
		Steps:        []Step{{ID: "a"}},
	}
	err := applyStepDefaults(&wf)
	if err == nil {
		t.Fatal("expected error for unknown step_defaults.kind")
	}
}

func TestApplyStepDefaults_RejectsNegativeMaxTurns(t *testing.T) {
	wf := WorkflowFile{
		StepDefaults: &StepDefaults{Kind: "agent", MaxTurns: -1},
		Steps:        []Step{{ID: "a"}},
	}
	err := applyStepDefaults(&wf)
	if err == nil {
		t.Fatal("expected error for negative step_defaults.max_turns")
	}
}

func TestApplyStepDefaults_StepOwnKindSuppressesDefaultKind(t *testing.T) {
	wf := WorkflowFile{
		StepDefaults: &StepDefaults{Kind: "agent", Agent: "worker"},
		Steps:        []Step{{ID: "a", Kind: "evidence_gate"}},
	}
	if err := applyStepDefaults(&wf); err != nil {
		t.Fatalf("applyStepDefaults: %v", err)
	}
	if wf.Steps[0].Kind != "evidence_gate" {
		t.Fatalf("kind = %q, want evidence_gate preserved", wf.Steps[0].Kind)
	}
	if wf.Steps[0].Agent != "" {
		t.Fatalf("agent = %q, want empty (kind stayed evidence_gate)", wf.Steps[0].Agent)
	}
}

// TestApplyStepDefaults_FillsAgentPanelTopLevelFields pins the fix for the
// gap where real workflows (bug-fix.toml, feature-delivery.toml) duplicate
// the review-panel synthesis agent/skill/template/schema/context across
// their agent_panel steps exactly like the repair_* agent steps this feature
// targets. An agent_panel step's own Panel (member list) is per-step data
// and must stay untouched; only the top-level scalar fields and context
// inherit from step_defaults, same as an "agent" step.
func TestApplyStepDefaults_FillsAgentPanelTopLevelFields(t *testing.T) {
	panel := &AgentPanel{
		FailurePolicy:           "require_all",
		RequireDistinctBindings: true,
		Members: []PanelMember{
			{ID: "a", Agent: "member-a"},
			{ID: "b", Agent: "member-b"},
		},
	}
	wf := WorkflowFile{
		StepDefaults: &StepDefaults{
			Kind:         "agent",
			Agent:        "review-synthesizer",
			Skill:        "review-synthesis",
			Template:     "templates/review-panel-synthesis.md",
			OutputSchema: "schemas/review-panel-v1.json",
			OnFailure:    "failure",
			Context:      []ContextBinding{{From: "inputs.task", As: "task", MaxBytes: 100}},
		},
		Steps: []Step{{ID: "review_panel", Kind: "agent_panel", Panel: panel}},
	}
	if err := applyStepDefaults(&wf); err != nil {
		t.Fatalf("applyStepDefaults: %v", err)
	}
	step := wf.Steps[0]
	if step.Agent != "review-synthesizer" || step.Skill != "review-synthesis" ||
		step.Template != "templates/review-panel-synthesis.md" ||
		step.OutputSchema != "schemas/review-panel-v1.json" || step.OnFailure != "failure" {
		t.Fatalf("agent_panel step not filled from step_defaults: %+v", step)
	}
	if len(step.Context) != 1 || step.Context[0].As != "task" {
		t.Fatalf("agent_panel step context not filled from step_defaults: %+v", step.Context)
	}
	if step.Panel != panel || len(step.Panel.Members) != 2 {
		t.Fatalf("agent_panel Panel/Members mutated by step_defaults: %+v", step.Panel)
	}
}

// TestApplyStepDefaults_PanelMembersStayUntouched confirms per-member fields
// (each member's own Agent/Skill/Template/OutputSchema) never inherit from
// step_defaults - each member is independent by design, unlike the panel
// step's own top-level fields.
func TestApplyStepDefaults_PanelMembersStayUntouched(t *testing.T) {
	wf := WorkflowFile{
		StepDefaults: &StepDefaults{Kind: "agent", Agent: "review-synthesizer", Template: "templates/synthesis.md"},
		Steps: []Step{{
			ID:   "review_panel",
			Kind: "agent_panel",
			Panel: &AgentPanel{
				FailurePolicy:           "require_all",
				RequireDistinctBindings: true,
				Members: []PanelMember{
					{ID: "a", Agent: "member-a"},
					{ID: "b", Agent: "member-b"},
				},
			},
		}},
	}
	if err := applyStepDefaults(&wf); err != nil {
		t.Fatalf("applyStepDefaults: %v", err)
	}
	for _, m := range wf.Steps[0].Panel.Members {
		if m.Template != "" {
			t.Fatalf("panel member %q inherited a step_defaults field it must not: %+v", m.ID, m)
		}
	}
	if wf.Steps[0].Panel.Members[0].Agent != "member-a" || wf.Steps[0].Panel.Members[1].Agent != "member-b" {
		t.Fatalf("panel member Agent overwritten: %+v", wf.Steps[0].Panel.Members)
	}
}
