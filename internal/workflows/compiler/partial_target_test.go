package compiler

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

// TestCompile_PartialTargetRequiresLoop pins the admission rule: a loop
// exhaustion escape (partial_target) only makes sense on a named loop, so a
// transition that declares one without a loop is rejected at compile time.
func TestCompile_PartialTargetRequiresLoop(t *testing.T) {
	wf := &definition.WorkflowFile{
		Name: "partial-no-loop", Version: 1, InitialStep: "s",
		Steps: []definition.Step{
			{ID: "s", Kind: "agent", Agent: "go-engineer"},
			{ID: "deliver", Kind: "agent", Agent: "go-engineer"},
		},
		Transitions: []definition.Transition{
			{From: "s", To: "s", Match: definition.MatchCriteria{Status: "failed"}, PartialTarget: "deliver"},
			{From: "s", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
	}
	_, err := Compile(wf)
	if err == nil {
		t.Fatal("expected error: partial_target requires a loop")
	}
	if !strings.Contains(err.Error(), "partial_target requires a loop") {
		t.Errorf("error %q should mention 'partial_target requires a loop'", err.Error())
	}
}

// TestCompile_PartialTargetMustBeDeclaredStep pins the admission rule: a
// partial_target names the step the run routes to when the loop exhausts, so
// it must be a declared step, not a typo.
func TestCompile_PartialTargetMustBeDeclaredStep(t *testing.T) {
	wf := &definition.WorkflowFile{
		Name: "partial-unknown-step", Version: 1, InitialStep: "s",
		Steps: []definition.Step{{ID: "s", Kind: "agent", Agent: "go-engineer"}},
		Transitions: []definition.Transition{
			{From: "s", To: "s", Match: definition.MatchCriteria{Status: "failed"}, Loop: "repair", MaxIterations: 2, PartialTarget: "missing"},
			{From: "s", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
	}
	_, err := Compile(wf)
	if err == nil {
		t.Fatal("expected error: partial_target must be a declared step")
	}
	if !strings.Contains(err.Error(), "is not a declared step") {
		t.Errorf("error %q should mention 'is not a declared step'", err.Error())
	}
}
