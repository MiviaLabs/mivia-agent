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
	snapshot := workflowledger.Snapshot{SchemaVersion: workflowledger.SnapshotSchemaVersion, DefinitionDigest: wf.Digest, Agents: map[string]workflowledger.AgentSnapshot{}, PanelBindings: map[string]workflowledger.PanelBindingSnapshot{}, Schemas: map[string]workflowledger.RefSnapshot{}, Templates: map[string]workflowledger.RefSnapshot{}}
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
		if step.Kind == "agent_panel" && step.Panel != nil {
			if err := pinWorkflowAgent(step.ID, step.Agent, registry, prior, &snapshot); err != nil {
				return nil, snapshot, err
			}
			if err := loadPanelMemberBindings(base, step, registry, prior, &snapshot); err != nil {
				return nil, snapshot, err
			}
			continue
		}
		if step.Kind != "agent" && step.Kind != "agent_gate" {
			continue
		}
		agent, digest, err := workflowAgent(step.ID, step.Agent, registry, prior, &snapshot)
		if err != nil {
			return nil, snapshot, err
		}
		result[step.ID] = controller.StepRuntime{Agent: agent, Digest: digest, Template: tmpl, Schema: schema}
	}
	if prior == nil && wf.DeliveryActive() {
		snapshot.Delivery = &workflowledger.DeliverySnapshot{
			Mode: wf.Delivery.Mode, Provider: wf.Delivery.Provider, Base: wf.Delivery.Base,
		}
	}
	return result, snapshot, nil
}

func pinWorkflowAgent(stepID, agentName string, registry *agents.AgentRegistry, prior *workflowledger.Snapshot, snapshot *workflowledger.Snapshot) error {
	_, _, err := workflowAgent(stepID, agentName, registry, prior, snapshot)
	return err
}

func workflowAgent(stepID, agentName string, registry *agents.AgentRegistry, prior *workflowledger.Snapshot, snapshot *workflowledger.Snapshot) (agents.ResolvedAgent, string, error) {
	agent, ok := registry.Get(agentName)
	if !ok {
		return agents.ResolvedAgent{}, "", fmt.Errorf("workflow step %q references unknown agent %q", stepID, agentName)
	}
	digest, err := agent.DefinitionDigest()
	if err != nil {
		return agents.ResolvedAgent{}, "", err
	}
	if prior != nil {
		pinned, ok := prior.Agents[agent.Name]
		if !ok || pinned.Digest != digest {
			return agents.ResolvedAgent{}, "", fmt.Errorf("agent %q changed since workflow admission", agent.Name)
		}
	}
	snapshot.Agents[agent.Name] = workflowledger.AgentSnapshot{Digest: digest}
	return agent, digest, nil
}

func loadPanelMemberBindings(base string, step definition.Step, registry *agents.AgentRegistry, prior *workflowledger.Snapshot, snapshot *workflowledger.Snapshot) error {
	for _, member := range step.Panel.Members {
		memberStep := step
		memberStep.Template = member.Template
		memberStep.OutputSchema = member.OutputSchema
		_, _, templateBytes, schemaBytes, err := loadStepReferences(base, memberStep, prior)
		if err != nil {
			return fmt.Errorf("panel step %q member %q: %w", step.ID, member.ID, err)
		}
		if member.Template != "" {
			snapshot.Templates[member.Template] = workflowledger.RefSnapshot{Digest: digestBytes(templateBytes), Bytes: append([]byte(nil), templateBytes...)}
		}
		if member.OutputSchema != "" {
			snapshot.Schemas[member.OutputSchema] = workflowledger.RefSnapshot{Digest: digestBytes(schemaBytes), Bytes: append([]byte(nil), schemaBytes...)}
		}
		agent, ok := registry.Get(member.Agent)
		if !ok {
			return fmt.Errorf("panel step %q member %q references unknown agent %q", step.ID, member.ID, member.Agent)
		}
		digest, err := agent.DefinitionDigest()
		if err != nil {
			return err
		}
		key := step.ID + "/" + member.ID
		if _, exists := snapshot.PanelBindings[key]; exists {
			return fmt.Errorf("duplicate panel binding key %q", key)
		}
		binding := workflowledger.PanelBindingSnapshot{
			StepID: step.ID, MemberID: member.ID, AgentName: agent.Name, AgentDigest: digest,
			ProviderName: member.Provider, Model: member.Model,
			TemplateDigest: digestBytes(templateBytes), SchemaDigest: digestBytes(schemaBytes),
		}
		if prior != nil {
			pinned, ok := prior.PanelBindings[key]
			pinned.SkillDigest = ""
			if !ok || pinned != binding {
				return fmt.Errorf("panel binding %q changed since workflow admission", key)
			}
		}
		snapshot.PanelBindings[key] = binding
	}
	return nil
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
