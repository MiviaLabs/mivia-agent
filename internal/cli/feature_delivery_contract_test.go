package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/jschema"
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
	assertFeatureDeliveryIntegrationGate(t, workflow)
	assertFeatureDeliverySchemasRequireInspected(t, base, filepath.Join(root, "internal", "workflows", "testdata"))
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

// assertFeatureDeliveryIntegrationGate pins the cross-cutting integration
// review gate: a reviewer step that runs after the main review and checks
// cross-layer interaction surfaces (context budget x tool results, retry x
// preparation consistency, concurrency), with its findings fed back to the
// implement step via a dedicated evidence channel so the repair loop is not
// blind. Without it, package-scoped reviews miss cross-layer defects (the
// prompt-too-long retry leaving a stale preparation, oversized tool results
// defeating the budget) because no single package owns the interaction.
func assertFeatureDeliveryIntegrationGate(t *testing.T, workflow definition.WorkflowFile) {
	t.Helper()
	step := featureDeliveryStep(t, workflow, "review_integration")
	if step.Kind != "agent_gate" {
		t.Fatalf("step review_integration kind = %q, want agent_gate", step.Kind)
	}
	if step.Agent != "reviewer" || step.Skill != "secure-change" {
		t.Fatalf("step review_integration agent/skill = %q/%q; want reviewer/secure-change", step.Agent, step.Skill)
	}
	if step.Template != "templates/review-integration.md" {
		t.Fatalf("step review_integration template = %q, want templates/review-integration.md", step.Template)
	}
	if step.OutputSchema != "schemas/review-v1.json" {
		t.Fatalf("step review_integration output schema = %q, want schemas/review-v1.json", step.OutputSchema)
	}
	impl := featureDeliveryStep(t, workflow, "implement")
	found := false
	for _, cb := range impl.Context {
		if cb.From == "steps.review_integration.output" && cb.As == "integration_findings" && cb.Optional {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("step implement must bind steps.review_integration.output as integration_findings (optional)")
	}
}

// featureDeliveryContractSchemas are the shared output-schema filenames that
// must stay pinned across every checked-in copy of the delivery workflow's
// schema directory. The verifier catalogue and presentation tests read the
// mirror under internal/workflows/testdata/schemas, so a weakened mirror must
// fail the contract just like a weakened primary.
var featureDeliveryContractSchemas = []string{
	"plan-v1.json",
	"review-v1.json",
	"change-summary-v1.json",
}

// schemaContractTB is the subset of testing.TB the schema contract assertions
// need, so a negative-control test can record failures instead of aborting.
type schemaContractTB interface {
	Helper()
	Fatalf(format string, args ...any)
}

// failingT records the first fatal assertion for negative-control tests.
type failingT struct {
	failed bool
	msg    string
}

func (f *failingT) Helper() {}
func (f *failingT) Fatalf(format string, args ...any) {
	f.failed = true
	f.msg = fmt.Sprintf(format, args...)
}

// assertFeatureDeliverySchemasRequireInspected verifies that every checked-in
// copy of the delivery workflow's output schemas enforces the inspected and
// files_changed invariants, and that the copies have not diverged
// byte-for-byte. The host workflow reads .mivia/workflows/schemas, while the
// verifier catalogue and presentation tests read the mirror under
// internal/workflows/testdata/schemas; both are pinned here.
func assertFeatureDeliverySchemasRequireInspected(tb schemaContractTB, bases ...string) {
	tb.Helper()
	if len(bases) == 0 {
		tb.Fatalf("no schema bases provided")
	}
	// Pin every checked-in copy to the primary: the verifier catalogue and
	// presentation tests read the mirror under internal/workflows/testdata/
	// schemas, so a weakened mirror (e.g. minItems removed from
	// files_changed) must fail CI exactly like a weakened primary.
	for _, name := range featureDeliveryContractSchemas {
		primary := readSchemaBytes(tb, bases[0], name)
		for _, other := range bases[1:] {
			if !bytes.Equal(primary, readSchemaBytes(tb, other, name)) {
				tb.Fatalf("schema %q diverged between %s and %s", name, bases[0], other)
			}
		}
	}
	for _, base := range bases {
		assertFeatureDeliverySchemaCopyRequiresInspected(tb, base)
	}
}

// assertFeatureDeliverySchemaCopyRequiresInspected verifies that one copy of
// the delivery workflow's output schemas requires an inspected array with at
// least one entry and a non-empty files_changed. This forces the agent and
// reviewer to cite workspace paths they read before making claims about the
// source, and stops a BLOCKED/no-op summary from validating as success.
func assertFeatureDeliverySchemaCopyRequiresInspected(tb schemaContractTB, base string) {
	tb.Helper()
	for _, name := range featureDeliveryContractSchemas {
		raw := readSchemaBytes(tb, base, name)
		var schema struct {
			Required   []string `json:"required"`
			Properties map[string]struct {
				MinItems int    `json:"minItems"`
				Type     string `json:"type"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			tb.Fatalf("parse schema %q from %s: %v", name, base, err)
		}
		requiresInspected := false
		for _, r := range schema.Required {
			if r == "inspected" {
				requiresInspected = true
			}
		}
		if !requiresInspected {
			tb.Fatalf("schema %q in %s must require inspected", name, base)
		}
		prop, ok := schema.Properties["inspected"]
		if !ok || prop.Type != "array" || prop.MinItems < 1 {
			tb.Fatalf("schema %q in %s: inspected must be an array with minItems >= 1", name, base)
		}
	}

	// change-summary-v1.json is the output schema for the implement and repair
	// steps. It must also require a non-empty files_changed array: a
	// BLOCKED/no-op output like {summary: "BLOCKED...", files_changed: []}
	// must not validate, or the workflow step is wrongly marked succeeded.
	raw := readSchemaBytes(tb, base, "change-summary-v1.json")
	var changeSummary struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			MinItems int    `json:"minItems"`
			Type     string `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &changeSummary); err != nil {
		tb.Fatalf("parse schema change-summary-v1.json from %s: %v", base, err)
	}
	requiresFilesChanged := false
	for _, r := range changeSummary.Required {
		if r == "files_changed" {
			requiresFilesChanged = true
		}
	}
	if !requiresFilesChanged {
		tb.Fatalf("schema change-summary-v1.json in %s must require files_changed", base)
	}
	filesChanged, ok := changeSummary.Properties["files_changed"]
	if !ok || filesChanged.Type != "array" || filesChanged.MinItems < 1 {
		tb.Fatalf("schema change-summary-v1.json in %s: files_changed must be an array with minItems >= 1", base)
	}

	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		tb.Fatalf("unmarshal schema change-summary-v1.json from %s: %v", base, err)
	}
	compiled, err := jschema.Compile(doc)
	if err != nil {
		tb.Fatalf("compile schema change-summary-v1.json from %s: %v", base, err)
	}
	noopOutput := `{"summary":"BLOCKED: no changes to implement","files_changed":[],"inspected":["internal/cli/feature_delivery_contract_test.go"]}`
	if _, err := compiled.ValidateJSONBytes([]byte(noopOutput)); err == nil {
		tb.Fatalf("schema change-summary-v1.json in %s must reject empty files_changed (no-op output validated)", base)
	}
}

// readSchemaBytes reads one schema file from a schema base directory.
func readSchemaBytes(tb schemaContractTB, base, name string) []byte {
	tb.Helper()
	raw, err := os.ReadFile(filepath.Join(base, "schemas", name))
	if err != nil {
		tb.Fatalf("read schema %q from %s: %v", name, base, err)
	}
	return raw
}

// TestFeatureDeliveryContractRejectsWeakenedCopy is the negative control
// for the schema contract assertions: a copy of change-summary-v1.json with
// minItems removed from files_changed must be rejected, whether it is the
// primary copy or an unpinned mirror.
func TestFeatureDeliveryContractRejectsWeakenedCopy(t *testing.T) {
	root := committedWorkflowRoot(t)

	t.Run("single-copy", func(t *testing.T) {
		dir := t.TempDir()
		copyCommittedContractSchemas(t, root, dir)
		weakenFilesChangedMinItems(t, filepath.Join(dir, "schemas", "change-summary-v1.json"))

		rec := &failingT{}
		assertFeatureDeliverySchemasRequireInspected(rec, dir)
		if !rec.failed {
			t.Fatal("weakened change-summary copy was accepted by the schema contract")
		}
	})

	t.Run("diverged-mirror", func(t *testing.T) {
		primary := t.TempDir()
		mirror := t.TempDir()
		copyCommittedContractSchemas(t, root, primary)
		copyCommittedContractSchemas(t, root, mirror)
		weakenFilesChangedMinItems(t, filepath.Join(mirror, "schemas", "change-summary-v1.json"))

		rec := &failingT{}
		assertFeatureDeliverySchemasRequireInspected(rec, primary, mirror)
		if !rec.failed {
			t.Fatal("diverged mirror copy was accepted by the schema contract")
		}
	})
}

// copyCommittedContractSchemas copies the pinned contract schemas from the
// committed host workflow into dst/schemas so a control can mutate one copy.
func copyCommittedContractSchemas(t *testing.T, root, dst string) {
	t.Helper()
	src := filepath.Join(root, ".mivia", "workflows", "schemas")
	dir := filepath.Join(dst, "schemas")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range featureDeliveryContractSchemas {
		raw, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// weakenFilesChangedMinItems removes the minItems constraint from files_changed
// in a change-summary-v1.json copy, simulating the drift the contract must
// catch in every copy.
func weakenFilesChangedMinItems(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	props, ok := doc["properties"].(map[string]any)
	if !ok {
		t.Fatalf("%s: properties is not an object", path)
	}
	filesChanged, ok := props["files_changed"].(map[string]any)
	if !ok {
		t.Fatalf("%s: files_changed is not an object", path)
	}
	delete(filesChanged, "minItems")
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
}
