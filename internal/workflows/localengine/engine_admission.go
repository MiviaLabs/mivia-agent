package localengine

// Admission helpers for starting a workflow run: workflow loading and input
// validation, including the engine-reserved stacking inputs (decision D3).

import (
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// loadAndValidateWorkflow loads and compiles the workflow, extends the input
// contract with the engine-reserved stacking inputs when the workflow is
// stacking, and validates the run's inputs against the effective contract.
func (e *Engine) loadAndValidateWorkflow(req ledger.StartRequest) (*definition.CompiledWorkflow, []byte, string, map[string]any, map[string]string, error) {
	compiled, raw, baseDir, err := e.loadWorkflow(req.Workflow)
	if err != nil {
		return nil, nil, "", nil, nil, err
	}
	applyStackingInputs(compiled)
	inputs, inputSnapshot, err := validateInputs(req.Inputs, compiled.Inputs)
	if err != nil {
		return nil, nil, "", nil, nil, err
	}
	return compiled, raw, baseDir, inputs, inputSnapshot, nil
}

// applyStackingInputs extends a compiled stacking workflow's input contract
// with the engine-reserved inputs (D3) BEFORE the engine's own input
// validation. The run graph is synthesized later, inside the controller, so a
// chunk-mode run's reserved inputs (stack_mode, chunk, pr_base, stack_part,
// chunk_plan) would otherwise be rejected here as unknown workflow inputs
// before admission ever saw them. Merging the definitions keeps the contract
// additive and leaves the compile-time digest untouched: the reserved defs are
// added post-compile, exactly as SynthesizeStacking does for the run graph.
func applyStackingInputs(compiled *definition.CompiledWorkflow) {
	definition.MergeStackingInputs(compiled)
}
