package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// admissionNoopRunner is never invoked: the test only constructs the
// controller (admission); it never starts a run.
type admissionNoopRunner struct{}

func (admissionNoopRunner) RunStep(context.Context, controller.AgentStepRequest) (controller.AgentStepResult, error) {
	return controller.AgentStepResult{}, nil
}

// TestChunkAdmissionFeatureDeliveryWorkflow pins that the SHIPPED
// feature-delivery workflow admits a chunk-mode run (stack_mode=chunk with the
// reserved stack inputs). The workflow's implement and repair steps bind
// steps.plan.output and steps.plan_tests.output MANDATORILY; in chunk mode the
// plan phase ran in the parent run, so those bindings must admit and resolve
// absent at runtime (contextForStep's chunk-mode grace) instead of failing
// admission. This regression keeps the stacking engine usable with the shipped
// workflow: without it, every chunk run of feature-delivery is refused before
// it starts.
func TestChunkAdmissionFeatureDeliveryWorkflow(t *testing.T) {
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root = filepath.Dir(filepath.Dir(root)) // internal/cli -> repo root
	raw, err := os.ReadFile(filepath.Join(root, ".mivia", "workflows", "feature-delivery.toml"))
	if err != nil {
		t.Skipf("workflow not present: %v", err)
	}
	workflow, _, err := definition.ParseWorkflowTOML(raw, "feature-delivery.toml")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	compiled, err := compiler.Compile(&workflow)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	synth, err := compiler.SynthesizeStacking(compiled)
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	steps := map[string]controller.StepRuntime{}
	for _, s := range synth.Steps {
		steps[s.ID] = controller.StepRuntime{Agent: agents.ResolvedAgent{Name: "eng"}, Digest: "d" + s.ID}
	}
	inputs := map[string]any{
		"task": "x", "stack_mode": "chunk", "chunk": `{"id":"c1"}`, "pr_base": "main", "stack_part": "1/2",
	}
	ctrl, err := controller.NewLinearController(workflowledger.NewMemoryRepository(), admissionNoopRunner{}, synth, steps, inputs, "wfr-chunk-admit", []byte("snap"))
	if err != nil {
		t.Fatalf("chunk admission: %v", err)
	}
	// The synthesized run graph carries the engine steps, and the implement
	// step received the reserved chunk inputs as context bindings.
	if !ctrl.Workflow.StepIDs["decompose"] || !ctrl.Workflow.StepIDs["chunk_plan_validate"] {
		t.Fatal("synthesized run graph lacks decompose/chunk_plan_validate")
	}
	implement := findWorkflowStep(t, ctrl.Workflow, "implement")
	hasChunkBinding := false
	for _, b := range implement.Context {
		if b.From == "inputs.chunk" && b.As == "chunk" {
			hasChunkBinding = true
		}
	}
	if !hasChunkBinding {
		t.Fatal("implement step lacks the injected inputs.chunk context binding")
	}
}

func findWorkflowStep(t *testing.T, wf *compiler.CompiledWorkflow, id string) definition.Step {
	t.Helper()
	for _, s := range wf.Steps {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("step %q not found in the compiled workflow", id)
	return definition.Step{}
}
