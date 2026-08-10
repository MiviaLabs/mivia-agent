package compiler

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

// TestCompile_DeliveryReentryStepReachable pins the admission rule for
// delivery re-entry steps: delivery runs after the success terminal, outside
// the step graph, so a step reached only through delivery.on_failure or
// delivery.on_pr_metadata_failure is not a graph orphan. The shipped
// feature-delivery workflow relies on this for its repair_pr_metadata step
// (reached only via delivery.on_pr_metadata_failure, then feeding back
// through review).
func TestCompile_DeliveryReentryStepReachable(t *testing.T) {
	wf := &definition.WorkflowFile{
		Name:        "delivery-reentry",
		Version:     1,
		InitialStep: "plan",
		Steps: []definition.Step{
			{ID: "plan", Kind: "agent", Agent: "planner"},
			{ID: "review", Kind: "agent_gate", Agent: "reviewer"},
			{ID: "repair_pr_metadata", Kind: "agent", Agent: "engineer", Template: "templates/repair-pr-metadata.md",
				Context: []definition.ContextBinding{
					{From: "delivery.failure", As: "delivery_hint", MaxBytes: 8192, Optional: true},
				}},
		},
		Transitions: []definition.Transition{
			{From: "plan", To: "review", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "review", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "repair_pr_metadata", To: "review", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
		Delivery: &definition.Delivery{
			Kind:                "pull_request",
			Mode:                "draft",
			Provider:            "github",
			Base:                "main",
			OnPRMetadataFailure: "repair_pr_metadata",
		},
	}
	if _, err := Compile(wf); err != nil {
		t.Fatalf("delivery re-entry step must compile: %v", err)
	}
}

// TestCompile_DeliveryOnFailureReentryStepReachable covers the sibling
// field: a step reached only through delivery.on_failure compiles the same
// way.
func TestCompile_DeliveryOnFailureReentryStepReachable(t *testing.T) {
	wf := &definition.WorkflowFile{
		Name:        "delivery-onfailure-reentry",
		Version:     1,
		InitialStep: "plan",
		Steps: []definition.Step{
			{ID: "plan", Kind: "agent", Agent: "planner"},
			{ID: "repair", Kind: "agent", Agent: "engineer",
				Context: []definition.ContextBinding{
					{From: "delivery.failure", As: "delivery_hint", MaxBytes: 8192, Optional: true},
				}},
		},
		Transitions: []definition.Transition{
			{From: "plan", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "repair", To: "plan", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
		Delivery: &definition.Delivery{
			Kind:      "pull_request",
			Mode:      "draft",
			Provider:  "github",
			Base:      "main",
			OnFailure: "repair",
		},
	}
	if _, err := Compile(wf); err != nil {
		t.Fatalf("delivery on_failure re-entry step must compile: %v", err)
	}
}

// TestCompile_DeliveryReentryDoesNotMaskTrueOrphans is the negative control:
// a step that no transition and no delivery field reaches is still reported
// unreachable.
func TestCompile_DeliveryReentryDoesNotMaskTrueOrphans(t *testing.T) {
	wf := &definition.WorkflowFile{
		Name:        "delivery-reentry-negative",
		Version:     1,
		InitialStep: "plan",
		Steps: []definition.Step{
			{ID: "plan", Kind: "agent", Agent: "planner"},
			{ID: "orphan", Kind: "agent", Agent: "engineer"},
		},
		Transitions: []definition.Transition{
			{From: "plan", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
		Delivery: &definition.Delivery{
			Kind:     "pull_request",
			Mode:     "draft",
			Provider: "github",
			Base:     "main",
		},
	}
	_, err := Compile(wf)
	if err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("orphan step must stay unreachable, got %v", err)
	}
}
