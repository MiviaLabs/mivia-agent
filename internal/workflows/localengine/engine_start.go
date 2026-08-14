package localengine

import (
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// newRunController pins admission inputs before it creates a workflow run.
func (e *Engine) newRunController(compiled *compiler.CompiledWorkflow, raw []byte, baseDir string, inputs map[string]any, inputSnapshot map[string]string, runID, invocationKey string) (*controller.LinearController, controller.Admission, error) {
	// A stacking run EXECUTES the synthesized graph: decompose and
	// chunk_plan_validate must be admitted like declared steps - schemas
	// loaded, runtimes built with a routing digest - or the controller would
	// refuse a digest-less synthesized step. Synthesis is idempotent, so
	// non-stacking workflows pass through unchanged.
	compiled, err := compiler.SynthesizeStacking(compiled)
	if err != nil {
		return nil, controller.Admission{}, err
	}
	schemas, err := loadOutputSchemas(baseDir, compiled)
	if err != nil {
		return nil, controller.Admission{}, err
	}
	templates, bindings, err := loadPanelSnapshotAssets(baseDir, compiled, schemas, e.AgentRegistry)
	if err != nil {
		return nil, controller.Admission{}, err
	}
	steps, err := buildStepRuntimesFromSnapshot(compiled, schemas)
	if err != nil {
		return nil, controller.Admission{}, err
	}
	snapshot, err := workflowledger.MarshalSnapshot(newRunSnapshot(compiled, raw, inputSnapshot, schemas, templates, bindings))
	if err != nil {
		return nil, controller.Admission{}, err
	}
	ctrl, err := controller.NewLinearController(e.ctrlRepo(), e.runner(), compiled, steps, inputs, runID, snapshot)
	if err != nil {
		return nil, controller.Admission{}, fmt.Errorf("new workflow controller: %w", err)
	}
	if err := ctrl.SetPanelLimiter(e.panelLimiter()); err != nil {
		return nil, controller.Admission{}, err
	}
	return ctrl, controller.Admission{InvocationKey: invocationKey, BaseRef: "main", BaseCommit: "test-base", WorktreeName: "workflow-" + runID, InputDigest: workflowledger.InputDigest(inputSnapshot)}, nil
}

func newRunSnapshot(compiled *compiler.CompiledWorkflow, raw []byte, inputs map[string]string, schemas, templates map[string]workflowledger.RefSnapshot, bindings map[string]workflowledger.PanelBindingSnapshot) workflowledger.Snapshot {
	snapshot := workflowledger.Snapshot{SchemaVersion: workflowledger.SnapshotSchemaVersion, DefinitionTOML: append([]byte(nil), raw...), DefinitionDigest: compiled.Digest, Inputs: inputs, Schemas: schemas, Templates: templates, PanelBindings: bindings}
	if compiled.Delivery != nil && compiled.DeliveryActive() {
		snapshot.Delivery = &workflowledger.DeliverySnapshot{Mode: compiled.Delivery.Mode, Provider: compiled.Delivery.Provider, Base: compiled.Delivery.Base}
	}
	return snapshot
}
