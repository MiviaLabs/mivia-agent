package cliworkflow

// Snapshot validation for the resume path. Split out of workflow_resume.go so
// that file stays under the go-structure line cap; these functions validate an
// admitted snapshot and carry no resume flow.

import (
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func ValidateWorkflowResumeSnapshot(run workflowledger.RunSnapshot, raw []byte) (workflowledger.Snapshot, *definition.CompiledWorkflow, map[string]any, error) {
	if run.SnapshotDigest == "" || run.SnapshotDigest != workflowledger.SnapshotDigest(raw) {
		return workflowledger.Snapshot{}, nil, nil, fmt.Errorf("workflow snapshot digest does not match the admitted snapshot")
	}
	snapshot, err := workflowledger.UnmarshalSnapshot(raw)
	if err != nil {
		return workflowledger.Snapshot{}, nil, nil, err
	}
	if err := snapshot.Validate(); err != nil {
		return workflowledger.Snapshot{}, nil, nil, err
	}
	if run.InputDigest == "" || run.InputDigest != workflowledger.InputDigest(snapshot.Inputs) {
		return workflowledger.Snapshot{}, nil, nil, fmt.Errorf("workflow input digest does not match the admitted inputs")
	}
	wf, _, err := definition.ParseWorkflowTOML(snapshot.DefinitionTOML, run.WorkflowName+".toml")
	if err != nil {
		return workflowledger.Snapshot{}, nil, nil, err
	}
	// Resume is recovery, not admission: the definition was already admitted,
	// so the unbounded-cycle admission check must not strand an in-flight run.
	compiled, err := definition.CompileForResume(&wf)
	if err != nil {
		return workflowledger.Snapshot{}, nil, nil, err
	}
	// The engine-reserved stacking inputs (D3) were merged into the admitted
	// contract, so resume accepts them too (a no-op for non-stacking runs).
	definition.MergeStackingInputs(compiled)
	// The two RECORDED digests must agree: the run row and its snapshot must
	// describe one admission, not two.
	// They are deliberately NOT compared against compiled.Digest. That digest
	// is sha256 over the marshalled Go struct, so it moves whenever the
	// definition types gain a field, even when the workflow text does not
	// change by one byte. Comparing against it asserts that this binary hashes
	// the definition the way the admitting binary did, which is a fact about
	// the binary, not about the definition. Every in-flight run then fails to
	// resume after an upgrade, and resume is the recovery path an upgrade must
	// survive.
	// Dropping the comparison loses no integrity. The definition text is
	// already proven: run.SnapshotDigest pins the whole snapshot above, the
	// snapshot carries definition_toml, and resume parses THAT text rather
	// than any file on disk. The text cannot differ from the admitted text.
	if snapshot.DefinitionDigest != run.WorkflowDigest {
		return workflowledger.Snapshot{}, nil, nil, fmt.Errorf("workflow definition digest does not match the admitted definition")
	}
	if err := validateWorkflowSnapshotReferences(compiled, snapshot); err != nil {
		return workflowledger.Snapshot{}, nil, nil, err
	}
	if snapshot.Delivery != nil {
		if compiled.Delivery == nil ||
			compiled.Delivery.Mode != snapshot.Delivery.Mode ||
			compiled.Delivery.Provider != snapshot.Delivery.Provider ||
			compiled.Delivery.Base != snapshot.Delivery.Base {
			return workflowledger.Snapshot{}, nil, nil, fmt.Errorf("snapshot delivery policy does not match the admitted definition")
		}
	}
	inputs := make(map[string]any, len(snapshot.Inputs))
	for key, value := range snapshot.Inputs {
		def, ok := compiled.Inputs[key]
		if !ok {
			return workflowledger.Snapshot{}, nil, nil, fmt.Errorf("snapshot contains unknown workflow input %q", key)
		}
		parsed, parseErr := parseWorkflowInputValue(value, def.Type)
		if parseErr != nil {
			return workflowledger.Snapshot{}, nil, nil, parseErr
		}
		inputs[key] = parsed
	}
	return snapshot, compiled, inputs, nil
}

func validateWorkflowSnapshotReferences(wf *definition.CompiledWorkflow, snapshot workflowledger.Snapshot) error {
	schemas := make(map[string][]byte, len(snapshot.Schemas))
	for name, ref := range snapshot.Schemas {
		if ref.Digest == "" || DigestBytes(ref.Bytes) != ref.Digest {
			return fmt.Errorf("snapshot schema %q digest is invalid", name)
		}
		schemas[name] = ref.Bytes
	}
	for _, step := range wf.Steps {
		// Agent-less steps (human_gate, evidence_gate) have no agent
		// admission to pin; only agent-bearing steps are checked.
		if step.Agent == "" {
			continue
		}
		agent, ok := snapshot.Agents[step.Agent]
		if !ok || agent.Digest == "" {
			return fmt.Errorf("snapshot agent %q admission is incomplete", step.Agent)
		}
		if step.Template != "" {
			ref, ok := snapshot.Templates[step.Template]
			if !ok || ref.Digest == "" || DigestBytes(ref.Bytes) != ref.Digest {
				return fmt.Errorf("snapshot template %q digest is invalid", step.Template)
			}
		}
	}
	return SliceErrorsFunc("workflow", definition.ValidateSchemaReferenceBytes(&definition.WorkflowFile{Steps: wf.Steps}, schemas))
}
