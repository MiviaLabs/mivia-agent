package controller

import (
	"context"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// resolutionRunner is the AgentStepRunner used by NewResolutionController.
// Resolution operations (Approve/Reject) never execute steps: they route the
// human decision through the workflow's transitions and the ledger. This
// runner exists only to satisfy NewLinearController's contract and is never
// invoked.
type resolutionRunner struct{}

func (resolutionRunner) RunStep(context.Context, AgentStepRequest) (AgentStepResult, error) {
	return AgentStepResult{}, fmt.Errorf("resolution controller cannot execute steps")
}

// NewResolutionController builds a controller for host resolution operations
// (approve/reject) on an existing run. It carries the admitted workflow and
// inputs but no step runtimes: Approve/Reject only read the workflow's
// transitions and write to the ledger.
func NewResolutionController(repo workflowledger.Repository, wf *definition.CompiledWorkflow, runID string, snapshot []byte, inputs map[string]any) (*LinearController, error) {
	return NewLinearController(repo, resolutionRunner{}, wf, map[string]StepRuntime{}, inputs, runID, snapshot)
}
