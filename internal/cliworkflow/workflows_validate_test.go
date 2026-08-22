package cliworkflow

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

// TestValidateTemplateBindingsAllowsSyntheticRound is the regression guard for
// a validator false positive: the controller injects inputs.round into a
// loop-bound step from the loop's durable counter, never from a declared
// context binding, so requiring a declaration reported a workflow that runs
// correctly as invalid.
func TestValidateTemplateBindingsAllowsSyntheticRound(t *testing.T) {
	step := definition.Step{ID: "review", Kind: "agent_gate"}
	wf := &definition.CompiledWorkflow{
		Steps:       []definition.Step{step},
		Transitions: []definition.Transition{{From: "review", To: "implement", Loop: "review_repair"}},
	}
	if err := validateWorkflowTemplateBindings(wf, step, "round {{ inputs.round }}"); err != nil {
		t.Fatalf("loop-bound step rejected inputs.round: %v", err)
	}

	// A step with no loop back-edge never receives round, so reading it must
	// still be reported rather than silently allowed.
	lone := &definition.CompiledWorkflow{
		Steps:       []definition.Step{step},
		Transitions: []definition.Transition{{From: "review", To: "next"}},
	}
	err := validateWorkflowTemplateBindings(lone, step, "round {{ inputs.round }}")
	if err == nil {
		t.Fatal("a step outside a loop must not be allowed to read inputs.round")
	}
	if !strings.Contains(err.Error(), "inputs.round") {
		t.Errorf("error %q does not name the missing binding", err)
	}
}

// TestValidateTemplateBindingsAcceptsDeliveryFailure pins the host-injected
// context source: a step that declares delivery.failure may read it as
// evidence at admission, mirroring the controller's runtime binding.
func TestValidateTemplateBindingsAcceptsDeliveryFailure(t *testing.T) {
	step := definition.Step{ID: "repair", Kind: "agent", Context: []definition.ContextBinding{
		{From: "delivery.failure", As: "delivery_hint", MaxBytes: 4096, Optional: true},
	}}
	wf := &definition.CompiledWorkflow{Steps: []definition.Step{step}}
	if err := validateWorkflowTemplateBindings(wf, step, "hint={{ evidence.delivery_hint }}"); err != nil {
		t.Fatalf("delivery.failure binding rejected at admission: %v", err)
	}
}

func TestStepIsLoopBound(t *testing.T) {
	step := definition.Step{ID: "review"}
	if stepIsLoopBound(nil, step) {
		t.Error("nil workflow must not report a loop")
	}
	wf := &definition.CompiledWorkflow{Transitions: []definition.Transition{
		{From: "other", To: "x", Loop: "other_repair"},
		{From: "review", To: "implement"},
	}}
	if stepIsLoopBound(wf, step) {
		t.Error("a step whose own edge has no loop must not report one")
	}
	wf.Transitions = append(wf.Transitions, definition.Transition{From: "review", To: "implement", Loop: "review_repair"})
	if !stepIsLoopBound(wf, step) {
		t.Error("a step owning a loop back-edge must report one")
	}
}

// TestValidateWorkflowVerifiersAcceptsCommandForm is the regression guard for
// the second false positive: the validator looked up the catalogue name for
// every evidence_gate, so a gate declared with the command form, which is how
// a repository supplies its own project-specific gate, was always invalid.
func TestValidateWorkflowVerifiersAcceptsCommandForm(t *testing.T) {
	wf := &definition.CompiledWorkflow{Steps: []definition.Step{{
		ID:      "preflight",
		Kind:    "evidence_gate",
		Command: &definition.StepCommand{Check: "invariants", Program: "python3", Args: []string{"scripts/x.py"}},
	}}}
	if err := validateWorkflowVerifiers(t.TempDir(), wf); err != nil {
		t.Fatalf("command-form evidence gate rejected: %v", err)
	}
}

func TestValidateWorkflowVerifiersRejectsBadCommandAndName(t *testing.T) {
	t.Run("non-bare program", func(t *testing.T) {
		wf := &definition.CompiledWorkflow{Steps: []definition.Step{{
			ID:      "preflight",
			Kind:    "evidence_gate",
			Command: &definition.StepCommand{Check: "c", Program: "/usr/bin/python3"},
		}}}
		if err := validateWorkflowVerifiers(t.TempDir(), wf); err == nil {
			t.Fatal("a path-qualified program must be refused")
		}
	})

	t.Run("unknown catalogue name", func(t *testing.T) {
		wf := &definition.CompiledWorkflow{Steps: []definition.Step{{
			ID: "gate", Kind: "evidence_gate", Verifier: "no-such-verifier",
		}}}
		if err := validateWorkflowVerifiers(t.TempDir(), wf); err == nil {
			t.Fatal("an unknown verifier name must still be refused")
		}
	})

	t.Run("non-gate steps are skipped", func(t *testing.T) {
		wf := &definition.CompiledWorkflow{Steps: []definition.Step{{ID: "plan", Kind: "agent"}}}
		if err := validateWorkflowVerifiers(t.TempDir(), wf); err != nil {
			t.Fatalf("an agent step must not need a verifier: %v", err)
		}
	})
}
