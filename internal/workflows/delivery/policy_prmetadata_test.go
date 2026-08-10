package delivery

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

// newCompiledPRMetadataWorkflow builds a compiled workflow whose delivery
// section carries the PR-metadata policy fields.
func newCompiledPRMetadataWorkflow(t *testing.T, prTitlePolicy, onFailure, onPRMetadataFailure string) *compiler.CompiledWorkflow {
	t.Helper()
	wf := &definition.WorkflowFile{
		Name: "pr-metadata-fields", Version: 1, InitialStep: "plan",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Steps: []definition.Step{
			{ID: "plan", Kind: "agent", Agent: "planner",
				Context: []definition.ContextBinding{
					{From: "delivery.failure", As: "delivery_hint", MaxBytes: 8192, Optional: true},
				}},
			{ID: "review", Kind: "agent_gate", Agent: "reviewer",
				Context: []definition.ContextBinding{
					{From: "delivery.failure", As: "delivery_hint", MaxBytes: 8192, Optional: true},
				}},
		},
		Transitions: []definition.Transition{
			{From: "plan", To: "review", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "review", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
		Delivery: &definition.Delivery{
			Kind:                  "pull_request",
			Mode:                  "draft",
			Provider:              "github",
			Base:                  "main",
			TitleTemplate:         "feat: {{ inputs.task }}",
			CommitMessageTemplate: "feat: {{ inputs.task }}",
			PRTitlePolicy:         prTitlePolicy,
			OnFailure:             onFailure,
			OnPRMetadataFailure:   onPRMetadataFailure,
		},
	}
	cw, err := compiler.Compile(wf)
	if err != nil {
		t.Fatalf("compiling fixture workflow: %v", err)
	}
	return cw
}

func TestFromCompiledPRMetadataFields(t *testing.T) {
	t.Run("pr_title_policy flows through", func(t *testing.T) {
		cw := newCompiledPRMetadataWorkflow(t, "policy/pr-title.toml", "", "")
		p, ok := FromCompiled(cw)
		if !ok {
			t.Fatal("ok = false")
		}
		if p.PRTitlePolicyPath != "policy/pr-title.toml" {
			t.Errorf("PRTitlePolicyPath = %q, want policy/pr-title.toml", p.PRTitlePolicyPath)
		}
	})

	t.Run("empty pr_title_policy stays empty", func(t *testing.T) {
		cw := newCompiledPRMetadataWorkflow(t, "", "", "")
		p, ok := FromCompiled(cw)
		if !ok {
			t.Fatal("ok = false")
		}
		if p.PRTitlePolicyPath != "" {
			t.Errorf("PRTitlePolicyPath = %q, want empty", p.PRTitlePolicyPath)
		}
	})

	t.Run("on_pr_metadata_failure defaults to on_failure", func(t *testing.T) {
		cw := newCompiledPRMetadataWorkflow(t, "", "review", "")
		p, ok := FromCompiled(cw)
		if !ok {
			t.Fatal("ok = false")
		}
		if p.OnPRMetadataFailure != "review" {
			t.Errorf("OnPRMetadataFailure = %q, want the on_failure default review", p.OnPRMetadataFailure)
		}
	})

	t.Run("explicit on_pr_metadata_failure wins", func(t *testing.T) {
		cw := newCompiledPRMetadataWorkflow(t, "", "review", "plan")
		p, ok := FromCompiled(cw)
		if !ok {
			t.Fatal("ok = false")
		}
		if p.OnPRMetadataFailure != "plan" {
			t.Errorf("OnPRMetadataFailure = %q, want the explicit value plan", p.OnPRMetadataFailure)
		}
	})
}
