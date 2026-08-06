package cli

import (
	"encoding/json"
	"fmt"
	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/template"
	"path/filepath"
)

func loadWorkflowRuntimes(root, base string, wf *compiler.CompiledWorkflow, registry *agents.AgentRegistry, prior *workflowledger.Snapshot) (map[string]controller.StepRuntime, workflowledger.Snapshot, error) {
	if base == "" {
		base = filepath.Join(root, ".mivia", "workflows")
	}
	result := make(map[string]controller.StepRuntime)
	snapshot := workflowledger.Snapshot{SchemaVersion: workflowledger.SnapshotSchemaVersion, DefinitionDigest: wf.Digest, Agents: map[string]workflowledger.AgentSnapshot{}, Schemas: map[string]workflowledger.RefSnapshot{}, Templates: map[string]workflowledger.RefSnapshot{}}
	for _, step := range wf.Steps {
		if !definition.ValidStepKinds[step.Kind] {
			return nil, snapshot, fmt.Errorf("workflow step %q has unsupported kind %q", step.ID, step.Kind)
		}
		tmpl, schema, tmplBytes, schemaBytes, err := loadStepReferences(base, step, prior)
		if err != nil {
			return nil, snapshot, err
		}
		if step.Template != "" {
			snapshot.Templates[step.Template] = workflowledger.RefSnapshot{Digest: digestBytes(tmplBytes), Bytes: append([]byte(nil), tmplBytes...)}
		}
		if step.OutputSchema != "" {
			snapshot.Schemas[step.OutputSchema] = workflowledger.RefSnapshot{Digest: digestBytes(schemaBytes), Bytes: append([]byte(nil), schemaBytes...)}
		}
		if step.Kind != "agent" && step.Kind != "agent_gate" {
			continue
		}
		agent, ok := registry.Get(step.Agent)
		if !ok {
			return nil, snapshot, fmt.Errorf("workflow step %q references unknown agent %q", step.ID, step.Agent)
		}
		digest, err := agent.DefinitionDigest()
		if err != nil {
			return nil, snapshot, err
		}
		if prior != nil {
			pinned, ok := prior.Agents[agent.Name]
			if !ok || pinned.Digest != digest {
				return nil, snapshot, fmt.Errorf("agent %q changed since workflow admission", agent.Name)
			}
		}
		result[step.ID] = controller.StepRuntime{Agent: agent, Digest: digest, Template: tmpl, Schema: schema}
		snapshot.Agents[agent.Name] = workflowledger.AgentSnapshot{Digest: digest}
	}
	if prior == nil && wf.DeliveryActive() {
		snapshot.Delivery = &workflowledger.DeliverySnapshot{
			Mode: wf.Delivery.Mode, Provider: wf.Delivery.Provider, Base: wf.Delivery.Base,
		}
	}
	return result, snapshot, nil
}

func loadStepReferences(base string, step definition.Step, prior *workflowledger.Snapshot) (string, map[string]any, []byte, []byte, error) {
	if prior != nil {
		t := prior.Templates[step.Template]
		s := prior.Schemas[step.OutputSchema]
		if step.Template != "" && (t.Digest == "" || digestBytes(t.Bytes) != t.Digest) {
			return "", nil, nil, nil, fmt.Errorf("snapshot template %q digest is invalid", step.Template)
		}
		if step.OutputSchema != "" && (s.Digest == "" || digestBytes(s.Bytes) != s.Digest) {
			return "", nil, nil, nil, fmt.Errorf("snapshot schema %q digest is invalid", step.OutputSchema)
		}
		var schema map[string]any
		if len(s.Bytes) > 0 && json.Unmarshal(s.Bytes, &schema) != nil {
			return "", nil, nil, nil, fmt.Errorf("snapshot schema %q is invalid", step.OutputSchema)
		}
		return string(t.Bytes), schema, t.Bytes, s.Bytes, nil
	}
	var templateBytes []byte
	var err error
	if step.Template != "" {
		templateBytes, err = readWorkflowRef(base, step.Template, template.MaxTemplateBytes)
		if err != nil {
			return "", nil, nil, nil, err
		}
	}
	var schema map[string]any
	if step.OutputSchema != "" {
		data, err := readWorkflowRef(base, step.OutputSchema, definition.MaxWorkflowFileBytes)
		if err != nil {
			return "", nil, nil, nil, err
		}
		if err := json.Unmarshal(data, &schema); err != nil {
			return "", nil, nil, nil, fmt.Errorf("schema %q is invalid: %w", step.OutputSchema, err)
		}
		return string(templateBytes), schema, templateBytes, data, nil
	}
	return string(templateBytes), schema, templateBytes, nil, nil
}
