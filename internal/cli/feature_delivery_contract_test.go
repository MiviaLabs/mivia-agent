package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/jschema"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

// TestFeatureDeliveryWorkflowContract keeps the checked-in delivery
// workflow aligned with its checked-in agents, skills, references, and fixed
// host evidence gates.
func TestFeatureDeliveryWorkflowContract(t *testing.T) {
	root := committedWorkflowRoot(t)
	workflow, base := loadCommittedFeatureDeliveryWorkflow(t, root)

	compiled, err := compiler.Compile(&workflow)
	if err != nil {
		t.Fatalf("compile committed feature-delivery workflow: %v", err)
	}
	if err := sliceErrors("workflow", compiler.ValidateAgentReferences(&workflow, root)); err != nil {
		t.Fatalf("validate committed workflow agents: %v", err)
	}
	if err := sliceErrors("workflow", compiler.ValidateSchemaReferences(&workflow, base)); err != nil {
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
	if err := sliceErrors("workflow", compiler.ValidateAgentSkillReferences(compiled, loaded.Registry, skillRegistry)); err != nil {
		t.Fatalf("validate committed workflow agent skills: %v", err)
	}

	workflowEngineer, ok := loaded.Registry.Get("workflow-engineer")
	if !ok {
		t.Fatal("workflow-engineer is missing")
	}
	if hasEffectiveTool(workflowEngineer, tools.RunCommandToolName) {
		t.Fatalf("workflow-engineer must not have %q", tools.RunCommandToolName)
	}
	// get_diagnostics is a command-running tool like run_command. Workflow
	// steps run no commands by design (the evidence gates execute checks in
	// the verifier sandbox), so it must stay out of the step agent's toolset
	// too; workflowDefaultRegistry deliberately does not map
	// DiagnosticsCommand.
	if hasEffectiveTool(workflowEngineer, tools.GetDiagnosticsToolName) {
		t.Fatalf("workflow-engineer must not have %q", tools.GetDiagnosticsToolName)
	}

	assertFeatureDeliveryAgentSteps(t, workflow)
	assertFeatureDeliveryEvidenceGates(t, root, workflow)
	assertFeatureDeliveryPreflightGate(t, workflow)
	if workflow.Delivery == nil || workflow.Delivery.Base != "dev" {
		t.Fatalf("feature-delivery base = %#v, want dev", workflow.Delivery)
	}

	assertFeatureDeliveryReviewFeedbackChannel(t, workflow)
	assertFeatureDeliveryReviewPriorFindingsBindings(t, workflow)
	assertFeatureDeliveryFindingsBindingsCapped(t, workflow)
	assertFeatureDeliveryReviewPanel(t, workflow)
	// assertFeatureDeliveryIntegrationGate(t, workflow) — disabled on the fast
	// debug path: review_integration is commented out of feature-delivery.toml.
	// Restore with the step (see docs/development/debug-cut.md).
	assertFeatureDeliverySchemasRequireInspected(t, base, filepath.Join(root, "internal", "workflows", "testdata"))
	assertFeatureDeliveryTemplatesInstructPRMetadata(t, root)
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

func assertFeatureDeliveryEvidenceGates(t *testing.T, root string, workflow definition.WorkflowFile) {
	t.Helper()
	want := map[string]string{
		"test_validate": "go-test",
		"verify":        "go-verify",
		"code_validate": "go-final",
	}
	profiles, err := config.LoadWorkspaceVerifiers(root)
	if err != nil {
		t.Fatalf("load workspace verifiers: %v", err)
	}
	wantCommands := map[string][]config.VerifierCommand{
		"go-test": {
			{Check: "go-test", Program: "go", Args: []string{"test", "./..."}},
		},
		"go-verify": {
			{Check: "go-vet", Program: "go", Args: []string{"vet", "./..."}},
			{Check: "go-build", Program: "go", Args: []string{"build", "./..."}},
		},
		"go-final": {
			{Check: "go-test-race", Program: "go", Args: []string{"test", "-race", "./..."}},
		},
	}
	for id, verifierName := range want {
		step := featureDeliveryStep(t, workflow, id)
		if step.Kind != "evidence_gate" || step.Verifier != verifierName {
			t.Fatalf("step %q = kind %q, verifier %q; want evidence_gate and %q", id, step.Kind, step.Verifier, verifierName)
		}
		profile, ok := profiles[verifierName]
		if !ok {
			t.Fatalf("step %q verifier %q has no [verifiers.%s] table in .mivia/mivia.toml", id, verifierName, verifierName)
		}
		if !profile.GoModuleBaseline {
			t.Fatalf("verifier %q must set go_module_baseline = true", verifierName)
		}
		if !reflect.DeepEqual(profile.Commands, wantCommands[verifierName]) {
			t.Fatalf("verifier %q commands = %#v, want %#v", verifierName, profile.Commands, wantCommands[verifierName])
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

// assertFeatureDeliveryPreflightGate pins the project-agnostic final gate:
// preflight_validate runs the repository's own gate command inside the
// verifier sandbox (bare executable + argv, never a shell string), its
// failure routes to a dedicated repair agent that receives the failed
// evidence, and the repair feeds back through review so the run converges.
// Without it the workflow's final gate is a fixed Go-only check that cannot
// see the project's real pre-push gate.
func assertFeatureDeliveryPreflightGate(t *testing.T, workflow definition.WorkflowFile) {
	t.Helper()
	gate := featureDeliveryStep(t, workflow, "preflight_validate")
	if gate.Kind != "evidence_gate" {
		t.Fatalf("step preflight_validate kind = %q, want evidence_gate", gate.Kind)
	}
	if gate.Verifier != "" {
		t.Fatalf("step preflight_validate must declare a command, not verifier %q", gate.Verifier)
	}
	if gate.Command == nil {
		t.Fatal("step preflight_validate must declare a sandboxed command")
	}
	if gate.Command.Check != "invariants" || gate.Command.Program != "python3" ||
		len(gate.Command.Args) != 1 || gate.Command.Args[0] != "scripts/validate_invariants.py" {
		t.Fatalf("step preflight_validate command = %#v; want check=invariants, program=python3, args=[scripts/validate_invariants.py]", gate.Command)
	}

	repair := featureDeliveryStep(t, workflow, "repair_preflight")
	if repair.Kind != "agent" || repair.Agent != "workflow-engineer" || repair.Skill != "workflow-feature-delivery" {
		t.Fatalf("step repair_preflight = kind %q agent %q skill %q; want agent workflow-engineer workflow-feature-delivery", repair.Kind, repair.Agent, repair.Skill)
	}
	foundEvidence := false
	for _, cb := range repair.Context {
		if cb.From == "steps.preflight_validate.output" && cb.As == "failed_evidence" && cb.MaxBytes == 32768 {
			foundEvidence = true
			break
		}
	}
	if !foundEvidence {
		t.Fatal("step repair_preflight must bind steps.preflight_validate.output as failed_evidence")
	}

	assertTransition(t, workflow, "code_validate", "preflight_validate", "succeeded")
	assertTransition(t, workflow, "preflight_validate", "preflight_structure", "succeeded")
	assertTransition(t, workflow, "preflight_validate", "repair_preflight", "failed")
	assertTransition(t, workflow, "repair_preflight", "review_panel", "succeeded")

	// preflight_structure runs the repository's own layout gate (check_go_structure
	// --strict --worktree) inside the sandbox so a change that violates the
	// project's structure policy is caught in-loop with a repair agent, not by
	// the delivery pre-commit hook hard-failing publication. --worktree scans
	// the filesystem: the sandbox git index is empty (git init, no add), so
	// --all would check zero files and a new untracked test file would be
	// invisible until delivery.
	structure := featureDeliveryStep(t, workflow, "preflight_structure")
	if structure.Kind != "evidence_gate" || structure.Verifier != "" || structure.Command == nil {
		t.Fatalf("step preflight_structure = kind %q verifier %q command %#v; want evidence_gate with a sandboxed command", structure.Kind, structure.Verifier, structure.Command)
	}
	if structure.Command.Check != "go-structure" || structure.Command.Program != "python3" ||
		len(structure.Command.Args) != 3 || structure.Command.Args[0] != "scripts/check_go_structure.py" ||
		structure.Command.Args[1] != "--strict" || structure.Command.Args[2] != "--worktree" {
		t.Fatalf("step preflight_structure command = %#v; want check=go-structure, program=python3, args=[scripts/check_go_structure.py --strict --worktree]", structure.Command)
	}
	structureRepair := featureDeliveryStep(t, workflow, "repair_preflight_structure")
	foundStructureEvidence := false
	for _, cb := range structureRepair.Context {
		if cb.From == "steps.preflight_structure.output" && cb.As == "failed_evidence" && cb.MaxBytes == 32768 {
			foundStructureEvidence = true
			break
		}
	}
	if !foundStructureEvidence {
		t.Fatal("step repair_preflight_structure must bind steps.preflight_structure.output as failed_evidence")
	}
	assertTransition(t, workflow, "preflight_structure", "success", "succeeded")
	assertTransition(t, workflow, "preflight_structure", "repair_preflight_structure", "failed")
	assertTransition(t, workflow, "repair_preflight_structure", "review_panel", "succeeded")
}

// assertTransition reports a failure when the workflow lacks a transition
// from step fromID to step toID whose match status is status.
func assertTransition(t *testing.T, workflow definition.WorkflowFile, fromID, toID, status string) {
	t.Helper()
	for _, tr := range workflow.Transitions {
		if tr.From == fromID && tr.To == toID && tr.Match.Status == status {
			return
		}
	}
	t.Fatalf("transition %s → %s (status %q) is missing", fromID, toID, status)
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
		// Fast debug path: plan_review/test_plan_review are commented out, so
		// plan/plan_tests no longer bind a reviewer; implement keeps the
		// review_panel channel.
		"implement": "steps.review_panel.output",
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

// assertFeatureDeliveryReviewPriorFindingsBindings pins the plan-v3
// convergence contract on the four review steps: each reviewer must bind its
// OWN prior output back into its prompt as prior_findings (optional,
// envelope-only, capped at 4096 bytes). Every reviewer is re-invoked on its
// repair loop back-edge (plan_review → plan → plan_review, test_plan_review →
// plan_tests → test_plan_review, review → implement → review,
// review_integration → implement → … → review_integration). Without the
// self-binding the reviewer regenerates its findings from identical context
// with no memory of the previous round, so it cannot reuse open finding ids
// verbatim or drop resolved ones, and the zero-progress gate cannot tell a
// no-progress loop from a converging one. The envelope-only 4096-byte cap
// keeps the ledger-refs directive honest: findings arrive as a reference
// envelope (artifact pointer + note), never the full inline payload, so the
// reviewer must read the prior round artifact back with workflow_inspect
// instead of re-deriving it from the prompt.
func assertFeatureDeliveryReviewPriorFindingsBindings(t *testing.T, workflow definition.WorkflowFile) {
	t.Helper()
	// review_panel replaced the single-reviewer agent_gate review step (Wave 7):
	// it is kind agent_panel, but carries the same self-binding contract as the
	// remaining agent_gate reviewers.
	// Fast debug path: plan_review, test_plan_review, and review_integration
	// are commented out of the workflow, so review_panel is the only reviewer
	// left and the only step carrying the self-binding contract.
	kinds := map[string]string{
		"review_panel": "agent_panel",
	}
	for id, wantKind := range kinds {
		step := featureDeliveryStep(t, workflow, id)
		if step.Kind != wantKind {
			t.Fatalf("review step %q kind = %q, want %q", id, step.Kind, wantKind)
		}
		found := false
		for _, cb := range step.Context {
			if cb.From == "steps."+id+".output" && cb.As == "prior_findings" && cb.Optional && cb.EnvelopeOnly && cb.MaxBytes == 4096 {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("review step %q must bind %q as prior_findings (optional, envelope_only, max_bytes = 4096); "+
				"without it the reviewer is blind to its own prior round", id, "steps."+id+".output")
		}
	}
}

// assertFeatureDeliveryFindingsBindingsCapped pins the ledger-refs directive
// on the repair-feedback bindings: every binding that feeds a reviewer's
// findings back into an agent step (review_findings on plan/plan_tests/
// implement, integration_findings on implement) must be optional, envelope-only
// and capped at 4096 bytes, so findings always arrive as a ledger reference
// envelope and the agent reads the full artifact with workflow_inspect rather
// than re-deriving it from an inline payload.
func assertFeatureDeliveryFindingsBindingsCapped(t *testing.T, workflow definition.WorkflowFile) {
	t.Helper()
	// Fast debug path: the plan-phase review gates and the integration gate
	// are commented out, so their feedback channels are gone; implement keeps
	// the review_panel channel.
	want := map[string]map[string]string{
		// agent step -> finding channel -> reviewer step whose output feeds it.
		"implement": {"review_findings": "review_panel"},
	}
	for stepID, channels := range want {
		step := featureDeliveryStep(t, workflow, stepID)
		for as, reviewer := range channels {
			found := false
			for _, cb := range step.Context {
				if cb.From == "steps."+reviewer+".output" && cb.As == as && cb.Optional && cb.EnvelopeOnly && cb.MaxBytes == 4096 {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("step %q must bind %q as %s (optional, envelope_only, max_bytes = 4096)", stepID, "steps."+reviewer+".output", as)
			}
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

// featureDeliveryContractSchemas are the shared schema filenames that
// must stay pinned across every checked-in copy of the delivery workflow's
// schema directory. The verifier catalogue and presentation tests read the
// mirror under internal/workflows/testdata/schemas, so a weakened mirror must
// fail the contract just like a weakened primary.
var featureDeliveryContractSchemas = []string{
	"plan-v1.json",
	"review-v1.json",
	"change-summary-v1.json",
	"verification-v1.json",
}

// inspectedContractSchemas are the delivery output schemas that must require
// a non-empty inspected array on every copy. verification-v1.json is pinned
// byte-for-byte but is host evidence (status + checks), so it carries no
// inspected array.
var inspectedContractSchemas = []string{
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
		assertFeatureDeliverySchemaCopyCarriesFindingsContract(tb, base)
		assertFeatureDeliverySchemaCopyRequiresPRMetadata(tb, base)
	}
}

// assertFeatureDeliverySchemaCopyRequiresInspected verifies that one copy of
// the delivery workflow's output schemas requires an inspected array with at
// least one entry and a non-empty files_changed. This forces the agent and
// reviewer to cite workspace paths they read before making claims about the
// source, and stops a BLOCKED/no-op summary from validating as success.
func assertFeatureDeliverySchemaCopyRequiresInspected(tb schemaContractTB, base string) {
	tb.Helper()
	for _, name := range inspectedContractSchemas {
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
	// The probe output satisfies every other constraint (including the required
	// pr_title/pr_summary and addressed_findings fields) so the rejection is
	// specifically the empty files_changed, not a missing PR-metadata field.
	noopOutput := `{"summary":"BLOCKED: no changes to implement","files_changed":[],"inspected":["internal/cli/feature_delivery_contract_test.go"],"addressed_findings":[],"pr_title":"feat: no-op change","pr_summary":"This change does nothing. It only tests the schema."}`
	if _, err := compiled.ValidateJSONBytes([]byte(noopOutput)); err == nil {
		tb.Fatalf("schema change-summary-v1.json in %s must reject empty files_changed (no-op output validated)", base)
	}
}

// assertFeatureDeliverySchemaCopyCarriesFindingsContract verifies the plan-v3
// findings contract in the delivery output schemas: the plan and
// change-summary outputs declare addressed_findings (the ids of every prior
// finding the agent claims to have addressed, empty when none), and the review
// output declares rich findings — each finding item carries an id that the
// reviewer reuses verbatim across rounds and that the controller's
// zero-progress gate normalizes.
func assertFeatureDeliverySchemaCopyCarriesFindingsContract(tb schemaContractTB, base string) {
	tb.Helper()
	assertPlanChangeSummaryFindingsContract(tb, base)
	assertReviewRichFindingsContract(tb, base)
}

// assertPlanChangeSummaryFindingsContract verifies the addressed_findings
// contract in the plan and change-summary output schemas: both declare
// addressed_findings (the ids of every prior finding the agent claims to have
// addressed, empty when none), required so an output that omits it never
// validates — otherwise the zero-progress gate cannot tell "no findings to
// address" from "the agent forgot to report what it addressed".
func assertPlanChangeSummaryFindingsContract(tb schemaContractTB, base string) {
	tb.Helper()
	for _, name := range []string{"plan-v1.json", "change-summary-v1.json"} {
		raw := readSchemaBytes(tb, base, name)
		var schema struct {
			Required   []string `json:"required"`
			Properties map[string]struct {
				Type  string `json:"type"`
				Items struct {
					Type      string `json:"type"`
					MinLength int    `json:"minLength"`
				} `json:"items"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			tb.Fatalf("parse schema %q from %s: %v", name, base, err)
		}
		prop, ok := schema.Properties["addressed_findings"]
		if !ok || prop.Type != "array" || prop.Items.Type != "string" || prop.Items.MinLength < 1 {
			tb.Fatalf("schema %q in %s: addressed_findings must be an array of non-empty strings", name, base)
		}
		// The findings contract is not optional: a plan or change-summary
		// output that omits addressed_findings must not validate, or the
		// zero-progress gate cannot tell "no findings to address" from "the
		// agent forgot to report what it addressed".
		requiresFindings := false
		for _, r := range schema.Required {
			if r == "addressed_findings" {
				requiresFindings = true
			}
		}
		if !requiresFindings {
			tb.Fatalf("schema %q in %s must require addressed_findings", name, base)
		}
	}
}

// assertReviewRichFindingsContract verifies the review-v1 rich-findings
// contract: findings must be an array whose items each require an id that the
// reviewer reuses verbatim across rounds (the zero-progress gate normalizes
// the R<round>- prefix), plus the review contract's three parts — the
// concrete claim, the cited evidence, and the exact required change — so a
// findings item is usable by the agent and by the convergence gate, not a
// bare severity/reason label.
func assertReviewRichFindingsContract(tb schemaContractTB, base string) {
	tb.Helper()
	raw := readSchemaBytes(tb, base, "review-v1.json")
	var review struct {
		Properties map[string]struct {
			Type  string `json:"type"`
			Items struct {
				Type       string   `json:"type"`
				Required   []string `json:"required"`
				Properties map[string]struct {
					Type      string `json:"type"`
					MinLength int    `json:"minLength"`
				} `json:"properties"`
			} `json:"items"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &review); err != nil {
		tb.Fatalf("parse schema review-v1.json from %s: %v", base, err)
	}
	findings, ok := review.Properties["findings"]
	if !ok || findings.Type != "array" {
		tb.Fatalf("schema review-v1.json in %s: findings must be an array", base)
	}
	requiresID := false
	for _, r := range findings.Items.Required {
		if r == "id" {
			requiresID = true
		}
	}
	if !requiresID {
		tb.Fatalf("schema review-v1.json in %s: findings items must require id (rich findings)", base)
	}
	idProp, ok := findings.Items.Properties["id"]
	if !ok || idProp.Type != "string" || idProp.MinLength < 1 {
		tb.Fatalf("schema review-v1.json in %s: findings item id must be a non-empty string", base)
	}
	// Rich findings: every finding carries an id the reviewer reuses verbatim
	// across rounds (the zero-progress gate normalizes the R<round>- prefix),
	// plus the review contract's three parts — the concrete claim, the cited
	// evidence, and the exact required change — so a findings item is usable
	// by the agent and by the convergence gate, not a bare severity/reason
	// label.
	rich := map[string]bool{"id": true, "claim": false, "evidence": false, "required": false}
	for _, r := range findings.Items.Required {
		if _, ok := rich[r]; ok {
			rich[r] = true
		}
	}
	for field, required := range rich {
		if !required {
			tb.Fatalf("schema review-v1.json in %s: findings items must require %s (rich findings)", base, field)
		}
		prop, ok := findings.Items.Properties[field]
		if !ok || prop.Type != "string" || prop.MinLength < 1 {
			tb.Fatalf("schema review-v1.json in %s: findings item %s must be a non-empty string", base, field)
		}
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
