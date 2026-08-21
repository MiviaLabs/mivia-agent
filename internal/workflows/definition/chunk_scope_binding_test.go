package definition

// Pins the compiler half of the chunk-scope fix (live finding,
// smoke-stack-3chunk-v3): a stacking-enabled workflow must be able to bind
// the engine-injected reserved input chunk_plan (optional - only chunk-mode
// admissions carry it) so the implement template can render the chunk's own
// scope slice. Workflows without stacking, and mandatory bindings, stay
// rejected: the input never exists there.

import (
	"testing"
)

func TestCompile_AllowsOptionalChunkPlanBindingWhenStacking(t *testing.T) {
	wf := newMinimalWorkflow("chunk-plan-ok")
	wf.Stacking = &Stacking{PlanStep: "plan", ImplementStep: "plan"}
	wf.Steps[0].Context = []ContextBinding{{From: "inputs.chunk_plan", As: "chunk_scope", Optional: true}}
	if _, err := Compile(wf); err != nil {
		t.Fatalf("Compile rejected an optional chunk_plan binding on a stacking workflow: %v", err)
	}
}

func TestCompile_RejectsChunkPlanBindingWithoutStacking(t *testing.T) {
	wf := newMinimalWorkflow("chunk-plan-no-stacking")
	wf.Steps[0].Context = []ContextBinding{{From: "inputs.chunk_plan", As: "chunk_scope", Optional: true}}
	assertCompileError(t, wf, "no stacking", "references unknown input")
}

func TestCompile_RejectsMandatoryChunkPlanBinding(t *testing.T) {
	wf := newMinimalWorkflow("chunk-plan-mandatory")
	wf.Stacking = &Stacking{PlanStep: "plan", ImplementStep: "plan"}
	wf.Steps[0].Context = []ContextBinding{{From: "inputs.chunk_plan", As: "chunk_scope"}}
	assertCompileError(t, wf, "mandatory", "must be optional")
}
