package cli

import (
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/template"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// assertFeatureDeliveryReviewPanel pins the Wave 7 panel review gate (D1-D17,
// plan 62): review_panel replaced the single-reviewer review step with three
// independent panel-reviewer members (distinct provider/model pairs, D4) and
// a review-synthesizer synthesis step. Without this pin, a change could drop
// a member, weaken require_distinct_bindings, or reintroduce a shared
// provider/model pair without any test failing.
func assertFeatureDeliveryReviewPanel(t *testing.T, workflow definition.WorkflowFile) {
	t.Helper()
	step := featureDeliveryStep(t, workflow, "review_panel")
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
		"correctness": {"llmgateway", "runware/deepseek-v4-flash", "panel-bug-audit", "templates/review-panel-correctness.md"},
		"security":    {"openrouter", "tencent/hy3-preview", "panel-secure-change", "templates/review-panel-security.md"},
		"integration": {"zai", "glm-5-turbo", "panel-architecture-review", "templates/review-panel-integration.md"},
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
	assertTransition(t, workflow, "review_panel", "review_integration", "succeeded")
	assertTransition(t, workflow, "review_panel", "implement", "succeeded")
}

// TestFeatureDeliveryPanelMemberTemplatesRenderWithoutRound is a regression
// test: buildPanelAttempt (internal/workflows/controller/panel_attempt.go)
// renders each member's template via contextForStep directly, never through
// agentStepRequest's inputs.round injection - panel members never receive a
// round input, unlike agent/agent_gate steps. A member template that
// unconditionally references {{ inputs.round }} (copied from an agent_gate
// review template without checking this) fails template.Render with
// "template binding \"inputs.round\" is missing" on every dispatch, so the
// panel could never actually run. This renders each committed member
// template with exactly the inputs/evidence shape buildPanelAttempt supplies
// (task only; no round) and fails if any binding the template references is
// missing.
func TestFeatureDeliveryPanelMemberTemplatesRenderWithoutRound(t *testing.T) {
	root := committedWorkflowRoot(t)
	workflow, base := loadCommittedFeatureDeliveryWorkflow(t, root)
	step := featureDeliveryStep(t, workflow, "review_panel")
	if step.Panel == nil {
		t.Fatal("step review_panel must declare [steps.panel]")
	}
	// Mirrors buildPanelAttempt: inputs holds only what the step's own
	// context binds from "inputs.*" (here, just task); evidence holds every
	// "steps.*.output" binding resolved to a placeholder string. Neither map
	// ever carries "round".
	inputs := map[string]any{"task": "example task", "chunk_scope": ""}
	evidence := map[string]any{
		"plan":           "example plan",
		"test_plan":      "example test plan",
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

// TestFeatureDeliveryPanelMembersAdmit is the live-shaped regression test for
// the enabled agent_panel review gate: every committed panel member
// (panel-reviewer with panel-bug-audit / panel-secure-change /
// panel-architecture-review) must pass validatePanelAgentTools, the exact
// admission check workflow_run runs before a run starts, and so must the
// review-synthesizer. The three panel skills are deliberately resource-less
// (JSON-only, no report-template), so each member's runtime surface is exactly
// the read-only panel toolset with no read_skill_resource reader; and the
// loaded agents inherit the project's global MCP servers (codegraph,
// context7), so the registry below mirrors that live surface with the same
// mcp__ tool names. When this test was added the gate refused every
// feature-delivery run at admission - first on the members ("...
// read_skill_resource], want [...]"), then, after that fix, on the synthesizer
// ("final runtime tools = [mcp__codegraph...], want []"); unit coverage used a
// resource-less skill and an MCP-less registry and never saw either.
func TestFeatureDeliveryPanelMembersAdmit(t *testing.T) {
	root := committedWorkflowRoot(t)
	workflow, _ := loadCommittedFeatureDeliveryWorkflow(t, root)
	step := featureDeliveryStep(t, workflow, "review_panel")
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
