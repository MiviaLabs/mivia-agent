package cli

import (
	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

type preparedWorkflowRuntime struct {
	Steps    map[string]controller.StepRuntime
	Snapshot []byte
}

func prepareWorkflowRuntime(root, refBase string, wf *compiler.CompiledWorkflow, registry *agents.AgentRegistry, prior *workflowledger.Snapshot, definitionTOML []byte, inputSnapshot map[string]string, dispatcherOpts SessionDispatcherOpts) (preparedWorkflowRuntime, error) {
	steps, snapshot, err := loadWorkflowRuntimes(root, refBase, wf, registry, prior)
	if err != nil {
		return preparedWorkflowRuntime{}, err
	}
	schemaBytes := make(map[string][]byte, len(snapshot.Schemas))
	for name, ref := range snapshot.Schemas {
		schemaBytes[name] = ref.Bytes
	}
	if err := compiler.ValidateSchemaReferenceBytes(&definition.WorkflowFile{Steps: wf.Steps}, schemaBytes); err != nil {
		return preparedWorkflowRuntime{}, err
	}
	for stepID, runtime := range steps {
		pinned := snapshot.Agents[runtime.Agent.Name]
		if prior != nil {
			pinned = prior.Agents[runtime.Agent.Name]
		}
		binding, err := workflowRuntimeBinding(runtime.Agent, pinned, prior != nil, dispatcherOpts)
		if err != nil {
			return preparedWorkflowRuntime{}, err
		}
		pinned.ProviderName, pinned.Model = binding.providerName, binding.model
		snapshot.Agents[runtime.Agent.Name] = pinned
		runtime.ProviderName, runtime.Model = binding.providerName, binding.model
		steps[stepID] = runtime
	}
	if prior != nil {
		snapshot = *prior
	} else {
		snapshot.DefinitionTOML = append([]byte(nil), definitionTOML...)
		snapshot.Inputs = cloneStringMap(inputSnapshot)
	}
	// Snapshot contains only JSON-safe field types.
	data, _ := workflowledger.MarshalSnapshot(snapshot)
	return preparedWorkflowRuntime{Steps: steps, Snapshot: data}, nil
}

func workflowRuntimeBinding(agent agents.ResolvedAgent, pinned workflowledger.AgentSnapshot, resume bool, opts SessionDispatcherOpts) (agentBinding, error) {
	if resume {
		return resolvePinnedAgentBinding(agent, opts, pinned.ProviderName, pinned.Model)
	}
	return resolveAgentBinding(agent, opts)
}
