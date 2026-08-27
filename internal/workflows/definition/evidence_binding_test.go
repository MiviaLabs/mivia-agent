package definition

import (
	"strings"
	"testing"
)

// --- Prior-step evidence binding size validation tests ---

// newTwoStepWorkflow returns a linear two-step workflow whose second step
// binds the first step's output into context.
func newTwoStepWorkflow(name string, bindings []ContextBinding) *WorkflowFile {
	return &WorkflowFile{
		Name:        name,
		Version:     1,
		InitialStep: "plan",
		Steps: []Step{
			{ID: "plan", Kind: "agent", Agent: "planner"},
			{ID: "implement", Kind: "agent", Agent: "implementer", Context: bindings},
		},
		Transitions: []Transition{
			{From: "plan", To: "implement", Match: MatchCriteria{Status: "succeeded"}},
			{From: "implement", To: "success", Match: MatchCriteria{Status: "succeeded"}},
		},
	}
}

// TestCompile_EvidenceBindingMaxBytes guards the runtime cap on prior-step
// evidence: the executor limits a single steps.<id>.output binding to
// MaxEvidenceBindingBytes (32KiB), so the compiler must reject
// larger requests at admission instead of letting them fail every run.
func TestCompile_EvidenceBindingMaxBytes(t *testing.T) {
	t.Run("steps output binding above 32KiB is rejected", func(t *testing.T) {
		wf := newTwoStepWorkflow("evidence-too-large", []ContextBinding{
			{From: "steps.plan.output", As: "plan_result", MaxBytes: 60000},
		})
		_, err := Compile(wf)
		if err == nil {
			t.Fatal("expected compile error for steps output binding max_bytes=60000, got nil")
		}
		if !strings.Contains(err.Error(), "max_bytes") {
			t.Errorf("error %q should mention max_bytes", err.Error())
		}
		if !strings.Contains(err.Error(), "steps.") {
			t.Errorf("error %q should mention the steps. binding source", err.Error())
		}
	})

	t.Run("steps output binding at exactly 32KiB compiles", func(t *testing.T) {
		wf := newTwoStepWorkflow("evidence-at-cap", []ContextBinding{
			{From: "steps.plan.output", As: "plan_result", MaxBytes: MaxEvidenceBindingBytes},
		})
		if _, err := Compile(wf); err != nil {
			t.Fatalf("expected steps output binding max_bytes=%d to compile: %v", MaxEvidenceBindingBytes, err)
		}
	})

	t.Run("inputs binding above 32KiB still compiles", func(t *testing.T) {
		wf := newTwoStepWorkflow("input-at-60000", []ContextBinding{
			{From: "inputs.spec", As: "spec", MaxBytes: 60000},
		})
		wf.Inputs = map[string]InputDef{"spec": {Type: "string"}}
		if _, err := Compile(wf); err != nil {
			t.Fatalf("expected inputs binding max_bytes=60000 to compile: %v", err)
		}
	})
}

// TestCompileForResumeEvidenceBindingMaxBytes guards the resume admission
// policy: a run admitted under an earlier policy whose snapshot carries a
// steps.*.output binding above MaxEvidenceBindingBytes must still resume (the
// runtime cap on actual output bytes remains the real safety bound). The
// evidence-cap check is admission-only, like the cycle check.
func TestCompileForResumeEvidenceBindingMaxBytes(t *testing.T) {
	wf := newTwoStepWorkflow("evidence-resume-policy", []ContextBinding{
		{From: "steps.plan.output", As: "plan_result", MaxBytes: 60000},
	})

	t.Run("fresh Compile still rejects the oversized evidence binding", func(t *testing.T) {
		_, err := Compile(wf)
		if err == nil {
			t.Fatal("expected Compile to reject steps output binding max_bytes=60000, got nil")
		}
		if !strings.Contains(err.Error(), "max_bytes") {
			t.Errorf("error %q should mention max_bytes", err.Error())
		}
	})

	t.Run("CompileForResume accepts a previously admitted oversized evidence binding", func(t *testing.T) {
		cw, err := CompileForResume(wf)
		if err != nil {
			t.Fatalf("CompileForResume rejected an admitted definition: %v", err)
		}
		if cw.Digest == "" {
			t.Fatal("CompileForResume produced an empty digest")
		}
	})
}
