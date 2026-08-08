package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/providerregistry"
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
	if err := authorizeWorkflowPanelBindings(wf, registry, snapshot, prior != nil, dispatcherOpts); err != nil {
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
		digest, err := config.MCPConfigDigest(dispatcherOpts.MCP)
		if err != nil {
			return preparedWorkflowRuntime{}, err
		}
		snapshot.MCPConfigDigest = digest
	}
	// Snapshot contains only JSON-safe field types.
	data, _ := workflowledger.MarshalSnapshot(snapshot)
	return preparedWorkflowRuntime{Steps: steps, Snapshot: data}, nil
}

func authorizeWorkflowPanelBindings(wf *compiler.CompiledWorkflow, registry *agents.AgentRegistry, snapshot workflowledger.Snapshot, resume bool, opts SessionDispatcherOpts) error {
	if wf == nil {
		return nil
	}
	for _, step := range wf.Steps {
		if step.Kind != "agent_panel" || step.Panel == nil {
			continue
		}
		if !opts.AllowWorkspaceAgentProviders {
			return fmt.Errorf("panel step %q requires allow_workspace_agent_providers = true in user configuration", step.ID)
		}
		for _, member := range step.Panel.Members {
			key := step.ID + "/" + member.ID
			binding, ok := snapshot.PanelBindings[key]
			if !ok {
				return fmt.Errorf("panel binding %q is missing", key)
			}
			agent, ok := registry.Get(member.Agent)
			if !ok {
				return fmt.Errorf("panel binding %q references unknown agent %q", key, member.Agent)
			}
			if agent.Name != "panel-reviewer" || declaredBinding(agent) {
				return fmt.Errorf("panel binding %q requires provider-neutral panel-reviewer agent", key)
			}
			if strings.TrimSpace(binding.ProviderName) == "" || strings.TrimSpace(binding.Model) == "" {
				return fmt.Errorf("panel binding %q has an incomplete provider/model pair", key)
			}
			if _, ok := providerregistry.Lookup(binding.ProviderName); !ok {
				return fmt.Errorf("panel binding %q uses unknown provider %q", key, binding.ProviderName)
			}
			resolved, err := resolvePinnedAgentBinding(agent, opts, binding.ProviderName, binding.Model)
			if err != nil {
				return fmt.Errorf("panel binding %q is not authorized: %w", key, err)
			}
			if resolved.completer == nil {
				return fmt.Errorf("panel binding %q has no usable completer", key)
			}
			if err := validatePanelAgentTools(agent, member.Skill, opts, false); err != nil {
				return fmt.Errorf("panel binding %q: %w", key, err)
			}
			if resume && (binding.ProviderName != member.Provider || binding.Model != member.Model) {
				return fmt.Errorf("panel binding %q changed since workflow admission", key)
			}
		}
		synthesizer, ok := registry.Get(step.Agent)
		if !ok {
			return fmt.Errorf("panel step %q references unknown synthesizer %q", step.ID, step.Agent)
		}
		if synthesizer.Name != "review-synthesizer" || declaredBinding(synthesizer) {
			return fmt.Errorf("panel step %q requires provider-neutral review-synthesizer agent", step.ID)
		}
		if err := validatePanelAgentTools(synthesizer, step.Skill, opts, true); err != nil {
			return fmt.Errorf("panel step %q: %w", step.ID, err)
		}
	}
	return nil
}

func workflowRuntimeBinding(agent agents.ResolvedAgent, pinned workflowledger.AgentSnapshot, resume bool, opts SessionDispatcherOpts) (agentBinding, error) {
	if resume {
		return resolvePinnedAgentBinding(agent, opts, pinned.ProviderName, pinned.Model)
	}
	return resolveAgentBinding(agent, opts)
}

func cloneStringMap(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

// readWorkflowRef reads one workflow-relative reference (template or schema)
// with symlink rejection, a size cap, and no path escape.
func readWorkflowRef(base, ref string, max int) ([]byte, error) {
	clean := filepath.Clean(ref)
	if clean == "." || filepath.IsAbs(ref) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return nil, fmt.Errorf("workflow reference %q escapes its directory", ref)
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Lstat(clean)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("workflow reference %q is not a regular file", ref)
	}
	file, err := root.Open(clean)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(max)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > max {
		return nil, fmt.Errorf("workflow reference %q exceeds %d bytes", ref, max)
	}
	return data, nil
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
