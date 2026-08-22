package cliworkflow

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// wireProgressNoopRunner is an AgentStepRunner that is never invoked by the
// coverage scenario (a controller Start only admits the run), so it can safely
// fail if it is ever called.
type wireProgressNoopRunner struct{}

func (wireProgressNoopRunner) RunStep(context.Context, controller.AgentStepRequest) (controller.AgentStepResult, error) {
	return controller.AgentStepResult{}, errors.New("wireProgressNoopRunner must never run a step")
}

// compileWireProgressWorkflow builds the smallest compiled workflow a
// LinearController can admit, enough to exercise the controller wiring paths
// without dispatching any agent step.
func compileWireProgressWorkflow(t *testing.T) *definition.CompiledWorkflow {
	t.Helper()
	wf := &definition.WorkflowFile{
		Version: 1, Name: "wire-progress", InitialStep: "one",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Steps: []definition.Step{
			{ID: "one", Kind: "agent", Agent: "one"},
		},
		Transitions: []definition.Transition{
			{From: "one", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
	}
	compiled, err := definition.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

// TestWireCLIWorkflowProgressRefusesStartedController covers the best-effort
// degradation path of WireCLIWorkflowProgress: when the controller has already
// started (its run admitted), SetProgressSink refuses the sink and the wiring
// helper logs a single disabled-progress line instead of failing the run.
func TestWireCLIWorkflowProgressRefusesStartedController(t *testing.T) {
	wf := compileWireProgressWorkflow(t)
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := controller.NewLinearController(repo, wireProgressNoopRunner{}, wf, nil, nil, "wfr-wire-progress", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	WireCLIWorkflowProgress(&WorkflowControllerBuild{Controller: ctrl}, &buf)
}
