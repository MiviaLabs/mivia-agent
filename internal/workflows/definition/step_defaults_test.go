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
