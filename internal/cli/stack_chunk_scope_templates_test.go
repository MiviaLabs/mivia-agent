package cli

// Pins the template half of the chunk-scope fix (live finding,
// smoke-stack-3chunk-v3): a chunk-mode run starts at implement with the FULL
// original task text, so the implement template and every review-panel
// member must receive and render the chunk's own plan slice (chunk_scope),
// and the implement template must instruct the agent to stay inside it.

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/template"
)

const exampleChunkScope = `{"id":"c2","title":"Add internal/pathutil SplitExt","files":["internal/pathutil/pathutil.go","internal/pathutil/pathutil_test.go"]}`

func chunkScopeBinding(t *testing.T, step definition.Step) definition.ContextBinding {
	t.Helper()
	for _, b := range step.Context {
		if b.From == "inputs.chunk_plan" && b.As == "chunk_scope" {
			if !b.Optional {
				t.Fatalf("step %q chunk_scope binding must be optional (plan/single runs carry no chunk_plan)", step.ID)
			}
			return b
		}
	}
	t.Fatalf("step %q has no context binding from inputs.chunk_plan as chunk_scope", step.ID)
	return definition.ContextBinding{}
}

// TestImplementTemplateRendersChunkScope: the implement step binds
// inputs.chunk_plan as chunk_scope, and the template renders the slice with
// an explicit only-this-chunk instruction; with an empty scope (plan/single
// mode) it still renders.
func TestImplementTemplateRendersChunkScope(t *testing.T) {
	root := committedWorkflowRoot(t)
	workflow, base := loadCommittedFeatureDeliveryWorkflow(t, root)
	step := featureDeliveryStep(t, workflow, "implement")
	chunkScopeBinding(t, step)
	templateBytes, err := readWorkflowRef(base, step.Template, template.MaxTemplateBytes)
	if err != nil {
		t.Fatalf("read template %q: %v", step.Template, err)
	}
	inputs := map[string]any{"task": "whole feature task", "chunk_scope": exampleChunkScope}
	evidence := map[string]any{
		"plan": "example plan", "test_plan": "example test plan",
		"review_findings": "", "integration_findings": "",
	}
	rendered, err := template.Render(string(templateBytes), inputs, evidence, definition.MaxEvidenceBindingBytes, template.DefaultMaxRenderedBytes)
	if err != nil {
		t.Fatalf("render implement template with chunk scope: %v", err)
	}
	if !strings.Contains(rendered, "internal/pathutil/pathutil.go") {
		t.Fatalf("rendered implement template must show the chunk's declared files:\n%s", rendered)
	}
	if !strings.Contains(rendered, "ONLY") {
		t.Fatalf("rendered implement template must carry an explicit only-this-chunk instruction:\n%s", rendered)
	}
	inputs["chunk_scope"] = ""
	if _, err := template.Render(string(templateBytes), inputs, evidence, definition.MaxEvidenceBindingBytes, template.DefaultMaxRenderedBytes); err != nil {
		t.Fatalf("render implement template without chunk scope: %v", err)
	}
}

// TestPanelMembersReceiveChunkScope: every review_panel member binds the
// chunk scope so reviewers grade scope fit against the chunk, not the whole
// task, and each member template renders with it.
func TestPanelMembersReceiveChunkScope(t *testing.T) {
	root := committedWorkflowRoot(t)
	workflow, base := loadCommittedFeatureDeliveryWorkflow(t, root)
	step := featureDeliveryStep(t, workflow, "review_panel")
	chunkScopeBinding(t, step)
	inputs := map[string]any{"task": "example task", "chunk_scope": exampleChunkScope}
	evidence := map[string]any{
		"plan": "example plan", "test_plan": "example test plan",
		"implementation": "example implementation summary",
		"prior_findings": "", "touched_files": `["a.go"]`,
	}
	for _, member := range step.Panel.Members {
		templateBytes, err := readWorkflowRef(base, member.Template, template.MaxTemplateBytes)
		if err != nil {
			t.Fatalf("panel member %q: read template: %v", member.ID, err)
		}
		if _, err := template.Render(string(templateBytes), inputs, evidence, definition.MaxEvidenceBindingBytes, template.DefaultMaxRenderedBytes); err != nil {
			t.Fatalf("panel member %q: render with chunk scope: %v", member.ID, err)
		}
	}
}

// TestReviewIntegrationTemplateRendersChunkScope pins the fix for a live
// finding: review_integration had no chunk_scope binding (unlike implement
// and review_panel), so it graded a chunk's diff against the WHOLE task
// spec and repeatedly raised "missing sibling packages" - a finding that can
// never be fixed (implement correctly refuses to touch sibling files), so
// reviewMadeNoProgress's identical-findings-across-rounds guard killed every
// affected chunk run (confirmed live across three separate stacked
// deliveries on 2026-08-15).
func TestReviewIntegrationTemplateRendersChunkScope(t *testing.T) {
	root := committedWorkflowRoot(t)
	workflow, base := loadCommittedFeatureDeliveryWorkflow(t, root)
	step := featureDeliveryStep(t, workflow, "review_integration")
	chunkScopeBinding(t, step)
	templateBytes, err := readWorkflowRef(base, step.Template, template.MaxTemplateBytes)
	if err != nil {
		t.Fatalf("read template %q: %v", step.Template, err)
	}
	inputs := map[string]any{"task": "whole feature task", "chunk_scope": exampleChunkScope, "round": "1"}
	evidence := map[string]any{
		"plan": "example plan", "test_plan": "example test plan",
		"implementation": "example implementation summary",
		"review":         "", "prior_findings": "",
	}
	rendered, err := template.Render(string(templateBytes), inputs, evidence, definition.MaxEvidenceBindingBytes, template.DefaultMaxRenderedBytes)
	if err != nil {
		t.Fatalf("render review_integration template with chunk scope: %v", err)
	}
	if !strings.Contains(rendered, "internal/pathutil/pathutil.go") {
		t.Fatalf("rendered review_integration template must show the chunk's declared files:\n%s", rendered)
	}
	inputs["chunk_scope"] = ""
	if _, err := template.Render(string(templateBytes), inputs, evidence, definition.MaxEvidenceBindingBytes, template.DefaultMaxRenderedBytes); err != nil {
		t.Fatalf("render review_integration template without chunk scope: %v", err)
	}
}
