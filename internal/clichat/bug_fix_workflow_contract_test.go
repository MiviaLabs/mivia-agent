package clichat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

// TestBugFixWorkflowInputCapsContract is the regression for the over-budget
// task/scope bindings: inputs.task and inputs.scope (and every step context
// binding that forwards them) must stay within the engine's step-context
// render budget. The previous value (1048576 = 1 MiB) exceeded the controller's
// 256 KiB render cap, so every agent step in the workflow failed to render and
// the run could never start.
//
// 16000 matches the review recommendation and keeps the full task text
// visible to every step that needs it.
const bugFixTaskScopeMaxBytes = 16000

func TestBugFixWorkflowInputCapsContract(t *testing.T) {
	root := committedWorkflowRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, ".mivia", "workflows", "bug-fix.toml"))
	if err != nil {
		t.Fatalf("read committed bug-fix workflow: %v", err)
	}
	if strings.Contains(string(raw), "max_bytes = 1048576") {
		t.Fatalf("bug-fix.toml still contains an over-budget max_bytes = 1048576 binding")
	}
	workflow, _, err := definition.ParseWorkflowTOML(raw, "bug-fix.toml")
	if err != nil {
		t.Fatalf("parse committed bug-fix workflow: %v", err)
	}
	compiled, err := definition.Compile(&workflow)
	if err != nil {
		t.Fatalf("compile committed bug-fix workflow: %v", err)
	}
	for _, name := range []string{"task", "scope"} {
		inp, ok := workflow.Inputs[name]
		if !ok {
			t.Fatalf("committed bug-fix workflow is missing input %q", name)
		}
		if inp.MaxBytes > bugFixTaskScopeMaxBytes {
			t.Fatalf("input %q max_bytes = %d, want <= %d", name, inp.MaxBytes, bugFixTaskScopeMaxBytes)
		}
	}
	for _, step := range compiled.Steps {
		for _, binding := range step.Context {
			if binding.From == "inputs.task" || binding.From == "inputs.scope" {
				if binding.MaxBytes > bugFixTaskScopeMaxBytes {
					t.Fatalf("step %q binding %q -> %q max_bytes = %d, want <= %d", step.ID, binding.From, binding.As, binding.MaxBytes, bugFixTaskScopeMaxBytes)
				}
				if binding.MaxBytes == 0 {
					t.Fatalf("step %q binding %q -> %q has no max_bytes cap", step.ID, binding.From, binding.As)
				}
			}
		}
	}
}
