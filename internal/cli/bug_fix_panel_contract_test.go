package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/template"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
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
	if err := compiler.ValidateAgentReferences(&workflow, root); err != nil {
		t.Fatalf("validate committed bug-fix workflow agents: %v", err)
	}
	if err := compiler.ValidateSchemaReferences(&workflow, base); err != nil {
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
	if err := compiler.ValidateAgentSkillReferences(compiled, loaded.Registry, skillRegistry); err != nil {
		t.Fatalf("validate committed bug-fix workflow agent skills: %v", err)
	}

	assertBugFixReviewPanel(t, workflow)
	assertBugFixPanelFeedback(t, workflow)
	assertBugFixPanelLimits(t, workflow)
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

// assertBugFixReviewPanel pins the bug-fix review_panel step: kind
// agent_panel, review-synthesizer aggregation with the shared synthesis
// template and schema, three independent panel-reviewer members (correctness,
// security, integration) with distinct provider/model pairs and one panel
// skill each, the require_all policy with distinct bindings, and the
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
	if step.Panel.FailurePolicy != "require_all" {
		t.Fatalf("step review_panel failure_policy = %q, want require_all", step.Panel.FailurePolicy)
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

// TestBugFixPanelMemberTemplatesRenderWithoutRound is a regression test
// mirroring the feature-delivery one: buildPanelAttempt renders each member's
// template via contextForStep directly, never through agentStepRequest's
// inputs.round injection - panel members never receive a round input, unlike
// agent/agent_gate steps. A member template that unconditionally references
// {{ inputs.round }} (copied from an agent_gate review template without
// checking this) fails template.Render on every dispatch, so the panel could
// never actually run. This renders each committed bug-fix member template
// with exactly the inputs/evidence shape buildPanelAttempt supplies (task and
// scope only; no round) and fails if any binding the template references is
// missing.
func TestBugFixPanelMemberTemplatesRenderWithoutRound(t *testing.T) {
	root := committedWorkflowRoot(t)
	workflow, base := loadCommittedBugFixWorkflow(t, root)
	step := bugFixStep(t, workflow, "review_panel")
	if step.Panel == nil {
		t.Fatal("step review_panel must declare [steps.panel]")
	}
	// Mirrors buildPanelAttempt: inputs holds only what the step's own
	// context binds from "inputs.*" (here, task and scope); evidence holds
	// every "steps.*.output" binding resolved to a placeholder string.
	// Neither map ever carries "round".
	inputs := map[string]any{"task": "example task", "scope": "internal/cli"}
	evidence := map[string]any{
		"plan":           "example fix plan",
		"findings":       "example confirmed findings",
		"implementation": "example implementation summary",
		"prior_findings": "",
		"touched_files":  `["a.go"]`,
	}
	for _, member := range step.Panel.Members {
		templateBytes, err := readWorkflowRef(base, member.Template, template.MaxTemplateBytes)
		if err != nil {
			t.Fatalf("panel member %q: read template %q: %v", member.ID, member.Template, err)
		}
		if _, err := template.Render(string(templateBytes), inputs, evidence, definition.MaxEvidenceBindingBytes, template.DefaultMaxRenderedBytes); err != nil {
			t.Fatalf("panel member %q: render template %q without a round input: %v", member.ID, member.Template, err)
		}
	}
}

// TestBugFixPanelMembersAdmit is the live-shaped regression test for the
// enabled agent_panel review gate: every committed bug-fix panel member
// (panel-reviewer with panel-bug-audit / panel-secure-change /
// panel-architecture-review) must pass validatePanelAgentTools, the exact
// admission check workflow_run runs before a run starts, and so must the
// review-synthesizer. It mirrors the feature-delivery panel admission test
// (resource-less panel skills + global MCP servers from .mivia/mivia.toml).
func TestBugFixPanelMembersAdmit(t *testing.T) {
	root := committedWorkflowRoot(t)
	workflow, _ := loadCommittedBugFixWorkflow(t, root)
	step := bugFixStep(t, workflow, "review_panel")
	if step.Panel == nil {
		t.Fatal("step review_panel must declare [steps.panel]")
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
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
	// Mirrors the live codegraph/context7 surface the loaded agents inherit
	// from .mivia/mivia.toml ([mcp] servers with global = true).
	for _, name := range []string{
		"mcp__codegraph__x636f646567726170685f6578706c6f7265",
		"mcp__context7__x71756572792d646f6373",
		"mcp__context7__x7265736f6c7665722d6c6962726172792d6964",
	} {
		registry.Register(namedTool{name: name})
	}
	opts := SessionDispatcherOpts{Registry: registry, AuthorityRegistry: registry, Config: config.DefaultSubagentConfig, SkillReg: skillRegistry}
	for _, member := range step.Panel.Members {
		agent, ok := loaded.Registry.Get(member.Agent)
		if !ok {
			t.Fatalf("panel member %q references unknown agent %q", member.ID, member.Agent)
		}
		if err := validatePanelAgentTools(agent, member.Skill, opts, false); err != nil {
			t.Fatalf("panel member %q (%s/%s, skill %q) must admit: %v", member.ID, member.Provider, member.Model, member.Skill, err)
		}
	}
	synthesizer, ok := loaded.Registry.Get(step.Agent)
	if !ok {
		t.Fatalf("panel step references unknown synthesizer %q", step.Agent)
	}
	if err := validatePanelAgentTools(synthesizer, step.Skill, opts, true); err != nil {
		t.Fatalf("review-synthesizer must admit: %v", err)
	}
}
