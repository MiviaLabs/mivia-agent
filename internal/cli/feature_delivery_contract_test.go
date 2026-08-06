package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/secretpath"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/verifier"
)

// TestCommittedFeatureDeliveryWorkflowContract keeps the checked-in delivery
// workflow aligned with its checked-in agents, skills, references, and fixed
// host evidence gates.
func TestCommittedFeatureDeliveryWorkflowContract(t *testing.T) {
	root := committedWorkflowRoot(t)
	workflow, base := loadCommittedFeatureDeliveryWorkflow(t, root)

	compiled, err := compiler.Compile(&workflow)
	if err != nil {
		t.Fatalf("compile committed feature-delivery workflow: %v", err)
	}
	if err := compiler.ValidateAgentReferences(&workflow, root); err != nil {
		t.Fatalf("validate committed workflow agents: %v", err)
	}
	if err := compiler.ValidateSchemaReferences(&workflow, base); err != nil {
		t.Fatalf("validate committed workflow schemas: %v", err)
	}
	for _, step := range workflow.Steps {
		if _, _, _, _, err := loadStepReferences(base, step, nil); err != nil {
			t.Fatalf("load references for step %q: %v", step.ID, err)
		}
	}

	skillRegistry, warnings, err := skills.LoadMarkdownSources(
		[]skills.Source{{Dir: filepath.Join(root, ".mivia", "skills"), Origin: skills.OriginProject}},
		skills.LoadOptions{},
	)
	if err != nil {
		t.Fatalf("load committed workflow skills: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("load committed workflow skills warnings: %v", warnings)
	}
	loaded, err := loadAgentDefinitions(root, "", skillRegistry)
	if err != nil {
		t.Fatalf("load committed workflow agents: %v", err)
	}
	if err := compiler.ValidateAgentSkillReferences(compiled, loaded.Registry, skillRegistry); err != nil {
		t.Fatalf("validate committed workflow agent skills: %v", err)
	}

	workflowEngineer, ok := loaded.Registry.Get("workflow-engineer")
	if !ok {
		t.Fatal("workflow-engineer is missing")
	}
	if hasEffectiveTool(workflowEngineer, tools.RunCommandToolName) {
		t.Fatalf("workflow-engineer must not have %q", tools.RunCommandToolName)
	}

	assertFeatureDeliveryAgentSteps(t, workflow)
	assertFeatureDeliveryEvidenceGates(t, workflow)
	if workflow.Delivery == nil || workflow.Delivery.Base != "master" {
		t.Fatalf("feature-delivery base = %#v, want master", workflow.Delivery)
	}

	assertFeatureDeliveryReviewFeedbackChannel(t, workflow)
	assertFeatureDeliverySchemasRequireInspected(t, base)
}

func committedWorkflowRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".mivia", "workflows", "feature-delivery.toml")); err != nil {
		t.Skipf("committed feature-delivery workflow is not present: %v", err)
	}
	return root
}

func loadCommittedFeatureDeliveryWorkflow(t *testing.T, root string) (definition.WorkflowFile, string) {
	t.Helper()
	base := filepath.Join(root, ".mivia", "workflows")
	raw, err := os.ReadFile(filepath.Join(base, "feature-delivery.toml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow, _, err := definition.ParseWorkflowTOML(raw, "feature-delivery.toml")
	if err != nil {
		t.Fatalf("parse committed feature-delivery workflow: %v", err)
	}
	return workflow, base
}

func assertFeatureDeliveryAgentSteps(t *testing.T, workflow definition.WorkflowFile) {
	t.Helper()
	for _, id := range []string{"plan", "plan_tests", "implement"} {
		step := featureDeliveryStep(t, workflow, id)
		if step.Agent != "workflow-engineer" || step.Skill != "workflow-feature-delivery" {
			t.Fatalf("step %q agent and skill = %q, %q; want workflow-engineer and workflow-feature-delivery", id, step.Agent, step.Skill)
		}
	}
}

func assertFeatureDeliveryEvidenceGates(t *testing.T, workflow definition.WorkflowFile) {
	t.Helper()
	want := map[string]string{
		"test_validate": "go-test",
		"verify":        "go-verify",
		"code_validate": "go-final",
	}
	catalogue := verifier.DefaultCatalogue(secretpath.Policy{})
	for id, verifierName := range want {
		step := featureDeliveryStep(t, workflow, id)
		if step.Kind != "evidence_gate" || step.Verifier != verifierName {
			t.Fatalf("step %q = kind %q, verifier %q; want evidence_gate and %q", id, step.Kind, step.Verifier, verifierName)
		}
		if _, err := catalogue.Lookup(step.Verifier); err != nil {
			t.Fatalf("step %q verifier %q is not host-owned: %v", id, step.Verifier, err)
		}
	}
}

func featureDeliveryStep(t *testing.T, workflow definition.WorkflowFile, id string) definition.Step {
	t.Helper()
	for _, step := range workflow.Steps {
		if step.ID == id {
			return step
		}
	}
	t.Fatalf("feature-delivery step %q is missing", id)
	return definition.Step{}
}

func hasEffectiveTool(agent agents.ResolvedAgent, want string) bool {
	for _, name := range agent.EffectiveTools {
		if name == want {
			return true
		}
	}
	return false
}

// assertFeatureDeliveryReviewFeedbackChannel verifies that the three
// review-repair loops feed the rejecting reviewer's output back to the
// re-invoked agent step. Without this binding the agent regenerates from
// identical context with no memory of the rejection, so it cannot correct
// false claims and the loop diverges.
func assertFeatureDeliveryReviewFeedbackChannel(t *testing.T, workflow definition.WorkflowFile) {
	t.Helper()
	wantBindings := map[string]string{
		// step ID -> the reviewer step whose output must reach the agent.
		"plan":       "steps.plan_review.output",
		"plan_tests": "steps.test_plan_review.output",
		"implement":  "steps.review.output",
	}
	for stepID, reviewerOutput := range wantBindings {
		step := featureDeliveryStep(t, workflow, stepID)
		found := false
		for _, cb := range step.Context {
			if cb.From == reviewerOutput && cb.As == "review_findings" && cb.Optional {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("step %q must bind %q as review_findings (optional); "+
				"without it the review-repair loop is blind", stepID, reviewerOutput)
		}
	}
}

// assertFeatureDeliverySchemasRequireInspected verifies that the three
// output schemas require an inspected array with at least one entry. This
// forces the agent and reviewer to cite workspace paths they read before
// making claims about the source.
func assertFeatureDeliverySchemasRequireInspected(t *testing.T, base string) {
	t.Helper()
	for _, name := range []string{"plan-v1.json", "review-v1.json", "change-summary-v1.json"} {
		raw, err := os.ReadFile(filepath.Join(base, "schemas", name))
		if err != nil {
			t.Fatalf("read schema %q: %v", name, err)
		}
		var schema struct {
			Required   []string `json:"required"`
			Properties map[string]struct {
				MinItems int    `json:"minItems"`
				Type     string `json:"type"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatalf("parse schema %q: %v", name, err)
		}
		required := false
		for _, r := range schema.Required {
			if r == "inspected" {
				required = true
			}
		}
		if !required {
			t.Fatalf("schema %q must require inspected", name)
		}
		prop, ok := schema.Properties["inspected"]
		if !ok || prop.Type != "array" || prop.MinItems < 1 {
			t.Fatalf("schema %q inspected must be an array with minItems >= 1", name)
		}
	}
}
