package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

// TestBugFixWorkflowPanelContract keeps the checked-in bug-fix workflow
// aligned with its checked-in agents, skills, references, and the panel
// review gate. It mirrors the feature-delivery contract test: compile the
// committed workflow, validate its agent/schema/skill references, and pin the
// review_panel step that now sits between implement and the post-panel review
// gate.
func TestBugFixWorkflowPanelContract(t *testing.T) {
	root := committedWorkflowRoot(t)
	workflow, base := loadCommittedBugFixWorkflow(t, root)

	compiled, err := compiler.Compile(&workflow)
	if err != nil {
		t.Fatalf("compile committed bug-fix workflow: %v", err)
	}
	if err := sliceErrors("workflow", compiler.ValidateAgentReferences(&workflow, root)); err != nil {
		t.Fatalf("validate committed bug-fix workflow agents: %v", err)
	}
	if err := sliceErrors("workflow", compiler.ValidateSchemaReferences(&workflow, base)); err != nil {
		t.Fatalf("validate committed bug-fix workflow schemas: %v", err)
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
		t.Fatalf("load committed bug-fix workflow skills: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("load committed bug-fix workflow skills warnings: %v", warnings)
	}
	loaded, err := loadAgentDefinitions(root, "", skillRegistry)
	if err != nil {
		t.Fatalf("load committed bug-fix workflow agents: %v", err)
	}
	if err := sliceErrors("workflow", compiler.ValidateAgentSkillReferences(compiled, loaded.Registry, skillRegistry)); err != nil {
		t.Fatalf("validate committed bug-fix workflow agent skills: %v", err)
	}

	// Fast debug path: the review layers (review_panel, review, perf_verify)
	// are commented out of bug-fix.toml, so the workflow's shape is pinned by
	// the fast-path assertions instead. Restore the three panel assertions
	// (assertBugFixReviewPanel / assertBugFixPanelFeedback /
	// assertBugFixPanelLimits) when the review gates come back.
	assertBugFixFastPathShape(t, workflow)
}

func loadCommittedBugFixWorkflow(t *testing.T, root string) (definition.WorkflowFile, string) {
	t.Helper()
	base := filepath.Join(root, ".mivia", "workflows")
	raw, err := os.ReadFile(filepath.Join(base, "bug-fix.toml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow, _, err := definition.ParseWorkflowTOML(raw, "bug-fix.toml")
	if err != nil {
		t.Fatalf("parse committed bug-fix workflow: %v", err)
	}
	return workflow, base
}

func bugFixStep(t *testing.T, workflow definition.WorkflowFile, id string) definition.Step {
	t.Helper()
	for _, step := range workflow.Steps {
		if step.ID == id {
			return step
		}
	}
	t.Fatalf("bug-fix step %q is missing", id)
	return definition.Step{}
}

// assertBugFixFastPathShape pins the temporary fast debug shape of
// bug-fix.toml: the LLM review layers (review_panel, review, perf_verify) are
// commented out, so implement and every repair step route straight to the
// first evidence gate (test_validate). When the review gates are restored,
// replace this with the panel assertions and re-enable the panel member
// tests below.
func assertBugFixFastPathShape(t *testing.T, workflow definition.WorkflowFile) {
	t.Helper()
	for _, gone := range []string{"review_panel", "review", "perf_verify"} {
		for _, step := range workflow.Steps {
			if step.ID == gone {
				t.Fatalf("step %q must be absent while the fast debug path is active", gone)
			}
		}
	}
	assertTransition(t, workflow, "implement", "test_validate", "succeeded")
	for _, repair := range []string{
		"repair_tests", "repair_verify", "repair_final",
		"repair_preflight", "repair_preflight_structure", "repair_pr_metadata",
	} {
		assertTransition(t, workflow, repair, "test_validate", "succeeded")
	}
}

// assertBugFixReviewPanel pins the bug-fix review_panel step: kind
// agent_panel, review-synthesizer aggregation with the shared synthesis
// template and schema, three independent panel-reviewer members (correctness,
// security, integration) with distinct provider/model pairs and one panel
// skill each, the allow_partial policy with distinct bindings, and the
// transitions that make it the code-review gate: implement -> review_panel ->
// review, review_panel -> implement on changes_requested. Without this pin a
// change could drop a member, weaken require_distinct_bindings, or
// reintroduce a shared provider/model pair without any test failing.
func assertBugFixReviewPanel(t *testing.T, workflow definition.WorkflowFile) {
	t.Helper()
	step := bugFixStep(t, workflow, "review_panel")
	if step.Kind != "agent_panel" {
		t.Fatalf("step review_panel kind = %q, want agent_panel", step.Kind)
	}
	if step.Agent != "review-synthesizer" || step.Skill != "review-synthesis" {
		t.Fatalf("step review_panel agent/skill = %q/%q; want review-synthesizer/review-synthesis", step.Agent, step.Skill)
	}
	if step.Template != "templates/review-panel-synthesis.md" {
		t.Fatalf("step review_panel template = %q, want templates/review-panel-synthesis.md", step.Template)
	}
	if step.OutputSchema != "schemas/review-panel-v1.json" {
		t.Fatalf("step review_panel output schema = %q, want schemas/review-panel-v1.json", step.OutputSchema)
	}
	if step.Panel == nil {
		t.Fatal("step review_panel must declare [steps.panel]")
	}
	if step.Panel.FailurePolicy != "allow_partial" {
		t.Fatalf("step review_panel failure_policy = %q, want allow_partial", step.Panel.FailurePolicy)
	}
	if !step.Panel.RequireDistinctBindings {
		t.Fatal("step review_panel require_distinct_bindings must be true")
	}
	wantMembers := map[string]struct{ provider, model, skill, template string }{
		"correctness": {"deepseek", "deepseek-v4-flash", "panel-bug-audit", "templates/bugfix-panel-correctness.md"},
		"security":    {"openrouter", "tencent/hy3-preview", "panel-secure-change", "templates/bugfix-panel-security.md"},
		"integration": {"zai", "glm-5-turbo", "panel-architecture-review", "templates/bugfix-panel-integration.md"},
	}
	if len(step.Panel.Members) != len(wantMembers) {
		t.Fatalf("step review_panel has %d members, want %d", len(step.Panel.Members), len(wantMembers))
	}
	seenPairs := map[string]struct{}{}
	for _, m := range step.Panel.Members {
		want, ok := wantMembers[m.ID]
		if !ok {
			t.Fatalf("step review_panel has unexpected member id %q", m.ID)
		}
		if m.Agent != "panel-reviewer" {
			t.Fatalf("panel member %q agent = %q, want panel-reviewer", m.ID, m.Agent)
		}
		if m.Provider != want.provider || m.Model != want.model {
			t.Fatalf("panel member %q provider/model = %q/%q, want %q/%q", m.ID, m.Provider, m.Model, want.provider, want.model)
		}
		if m.Skill != want.skill {
			t.Fatalf("panel member %q skill = %q, want %q", m.ID, m.Skill, want.skill)
		}
		if m.Template != want.template {
			t.Fatalf("panel member %q template = %q, want %q", m.ID, m.Template, want.template)
		}
		if m.OutputSchema != "schemas/panel-review-v1.json" {
			t.Fatalf("panel member %q output schema = %q, want schemas/panel-review-v1.json", m.ID, m.OutputSchema)
		}
		pair := m.Provider + "/" + m.Model
		if _, dup := seenPairs[pair]; dup {
			t.Fatalf("panel member %q duplicates provider/model pair %q", m.ID, pair)
		}
		seenPairs[pair] = struct{}{}
	}
	assertTransition(t, workflow, "implement", "review_panel", "succeeded")
	assertTransition(t, workflow, "review_panel", "review", "succeeded")
	assertTransition(t, workflow, "review_panel", "implement", "succeeded")
}

// assertBugFixPanelFeedback pins the panel's feedback channels: the
// post-panel review gate consumes the panel report so it is not blind to the
// panel verdict, implement receives the panel report on panel repair
// iterations (envelope-only, capped) so the rework loop is not blind, every
// repair loop feeds back through the panel so a reworked fix is re-reviewed
// before it can proceed, and the changes_requested loop is bounded like the
// other bug-fix loops.
func assertBugFixPanelFeedback(t *testing.T, workflow definition.WorkflowFile) {
	t.Helper()
	review := bugFixStep(t, workflow, "review")
	foundReview := false
	for _, cb := range review.Context {
		if cb.From == "steps.review_panel.output" && cb.As == "review" && cb.Optional {
			foundReview = true
			break
		}
	}
	if !foundReview {
		t.Fatal("step review must bind steps.review_panel.output as review (optional)")
	}
	implement := bugFixStep(t, workflow, "implement")
	foundPanel := false
	for _, cb := range implement.Context {
		if cb.From == "steps.review_panel.output" && cb.As == "panel_findings" && cb.Optional && cb.EnvelopeOnly && cb.MaxBytes == 4096 {
			foundPanel = true
			break
		}
	}
	if !foundPanel {
		t.Fatal("step implement must bind steps.review_panel.output as panel_findings (optional, envelope_only, max_bytes = 4096)")
	}
	for _, repair := range []string{
		"repair_tests", "repair_verify", "repair_final",
		"repair_preflight", "repair_preflight_structure", "repair_pr_metadata",
	} {
		assertTransition(t, workflow, repair, "review_panel", "succeeded")
	}
	foundPanelLoop := false
	for _, tr := range workflow.Transitions {
		if tr.From == "review_panel" && tr.To == "implement" && tr.Match.Status == "succeeded" && tr.Loop == "panel_repair" && tr.MaxIterations == 8 {
			foundPanelLoop = true
			break
		}
	}
	if !foundPanelLoop {
		t.Fatal("transition review_panel -> implement must be a bounded loop (loop = panel_repair, max_iterations = 8)")
	}
}

// assertBugFixPanelLimits pins the panel failure loop bound: review_panel
// re-enters itself on member/synthesis failure, and the re-entry count is
// capped by [limits] max_on_failure_reentries like feature-delivery's panel,
// so a consistently failing panel settles the run instead of looping forever.
func assertBugFixPanelLimits(t *testing.T, workflow definition.WorkflowFile) {
	t.Helper()
	step := bugFixStep(t, workflow, "review_panel")
	if step.OnFailure != "review_panel" {
		t.Fatalf("step review_panel on_failure = %q, want review_panel (self-loop)", step.OnFailure)
	}
	if workflow.Limits.MaxOnFailureReentries != 3 {
		t.Fatalf("limits max_on_failure_reentries = %d, want 3", workflow.Limits.MaxOnFailureReentries)
	}
}

// TestBugFixPanelMemberTemplatesRenderWithoutRound and TestBugFixPanelMembersAdmit
// are DISABLED on the fast debug path: review_panel is commented out of
// bug-fix.toml (see docs/development/debug-cut.md). They guard the panel
// member templates rendering without an inputs.round injection and
// validatePanelAgentTools admission for every member plus the synthesizer.
// Restore them with the panel; the live bodies are in git history at the
// last commit before the cut (HEAD moves, this SHA does not):
// git show ce7538ad:internal/cli/bug_fix_panel_contract_test.go
