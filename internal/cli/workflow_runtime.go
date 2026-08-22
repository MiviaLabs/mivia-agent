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
	cliagents "github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/providerregistry"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

type preparedWorkflowRuntime struct {
	Steps    map[string]controller.StepRuntime
	Snapshot []byte
}

func prepareWorkflowRuntime(root, refBase string, wf *definition.CompiledWorkflow, registry *agents.AgentRegistry, prior *workflowledger.Snapshot, priorRaw []byte, definitionTOML []byte, inputSnapshot map[string]string, dispatcherOpts SessionDispatcherOpts) (preparedWorkflowRuntime, error) {
	steps, snapshot, err := loadWorkflowRuntimes(root, refBase, wf, registry, prior)
	if err != nil {
		return preparedWorkflowRuntime{}, err
	}
	if err := resolveWorkflowPanelSynthesisBindings(wf, registry, prior, snapshot, dispatcherOpts); err != nil {
		return preparedWorkflowRuntime{}, err
	}
	if err := authorizeWorkflowPanelBindings(wf, registry, snapshot, prior != nil, dispatcherOpts); err != nil {
		return preparedWorkflowRuntime{}, err
	}
	schemaBytes := make(map[string][]byte, len(snapshot.Schemas))
	for name, ref := range snapshot.Schemas {
		schemaBytes[name] = ref.Bytes
	}
	if err := sliceErrors("workflow", definition.ValidateSchemaReferenceBytes(&definition.WorkflowFile{Steps: wf.Steps}, schemaBytes)); err != nil {
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
		pinned.ProviderName, pinned.Model = binding.ProviderName, binding.Model
		snapshot.Agents[runtime.Agent.Name] = pinned
		runtime.ProviderName, runtime.Model = binding.ProviderName, binding.Model
		steps[stepID] = runtime
	}
	if prior != nil {
		// A resume must carry the STORED admission bytes verbatim: the
		// controller compares them byte-for-byte against the ledger record
		// (StartNew), and the run row's SnapshotDigest was computed over
		// them. Re-marshalling the decoded struct here would couple resume
		// to round-trip fidelity and break whenever the in-memory prior was
		// deliberately adjusted (--accept-verifier-change rewrites its pins
		// for verification only, never for the durable record).
		if len(priorRaw) == 0 {
			return preparedWorkflowRuntime{}, fmt.Errorf("workflow resume snapshot bytes are missing")
		}
		return preparedWorkflowRuntime{Steps: steps, Snapshot: append([]byte(nil), priorRaw...)}, nil
	}
	snapshot.DefinitionTOML = append([]byte(nil), definitionTOML...)
	snapshot.Inputs = cloneStringMap(inputSnapshot)
	digest, err := config.MCPConfigDigest(dispatcherOpts.MCP)
	if err != nil {
		return preparedWorkflowRuntime{}, err
	}
	snapshot.MCPConfigDigest = digest
	// Snapshot contains only JSON-safe field types.
	data, _ := workflowledger.MarshalSnapshot(snapshot)
	return preparedWorkflowRuntime{Steps: steps, Snapshot: data}, nil
}

func authorizeWorkflowPanelBindings(wf *definition.CompiledWorkflow, registry *agents.AgentRegistry, snapshot workflowledger.Snapshot, resume bool, opts SessionDispatcherOpts) error {
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
			if agent.Name != "panel-reviewer" || cliagents.DeclaredBinding(agent) {
				return fmt.Errorf("panel binding %q requires provider-neutral panel-reviewer agent", key)
			}
			if strings.TrimSpace(binding.ProviderName) == "" || strings.TrimSpace(binding.Model) == "" {
				return fmt.Errorf("panel binding %q has an incomplete provider/model pair", key)
			}
			if _, ok := providerregistry.Lookup(binding.ProviderName); !ok {
				return fmt.Errorf("panel binding %q uses unknown provider %q", key, binding.ProviderName)
			}
			resolved, err := cliagents.ResolvePinnedAgentBinding(agent, opts, binding.ProviderName, binding.Model)
			if err != nil {
				return fmt.Errorf("panel binding %q is not authorized: %w", key, err)
			}
			if resolved.Completer == nil {
				return fmt.Errorf("panel binding %q has no usable completer", key)
			}
			if err := validatePanelAgentTools(agent, member.Skill, opts, false); err != nil {
				return fmt.Errorf("panel binding %q: %w", key, err)
			}
			if resume && (binding.ProviderName != member.Provider || binding.Model != member.Model) {
				return fmt.Errorf("panel binding %q changed since workflow admission; recover with: restore the panel member provider/model to its admitted pair or start a fresh run", key)
			}
		}
		synthesizer, ok := registry.Get(step.Agent)
		if !ok {
			return fmt.Errorf("panel step %q references unknown synthesizer %q", step.ID, step.Agent)
		}
		if synthesizer.Name != "review-synthesizer" || cliagents.DeclaredBinding(synthesizer) {
			return fmt.Errorf("panel step %q requires provider-neutral review-synthesizer agent", step.ID)
		}
		if err := validatePanelAgentTools(synthesizer, step.Skill, opts, true); err != nil {
			return fmt.Errorf("panel step %q: %w", step.ID, err)
		}
	}
	return nil
}

// resolveWorkflowPanelSynthesisBindings resolves and stores the provider and
// model each agent_panel step's review-synthesizer runs on. D4 requires the
// synthesizer to declare no provider or model of its own; it follows the
// admitted session binding exactly like any other undeclared-binding agent
// (see resolveAgentBinding). Unlike a panel member's binding, this pair is
// not a workflow-declared override: it is resolved here, once, and then
// pinned like any other agent snapshot so resume reauthorizes the exact pair
// instead of drifting to a new session default.
//
// It stores the result under the reserved member key "<step-id>/synthesis"
// in Snapshot.PanelBindings. A real member ID can never equal "synthesis"
// (panel_types.go's validateInitial rejects it), so this key cannot collide
// with a declared member binding.
func resolveWorkflowPanelSynthesisBindings(wf *definition.CompiledWorkflow, registry *agents.AgentRegistry, prior *workflowledger.Snapshot, snapshot workflowledger.Snapshot, opts SessionDispatcherOpts) error {
	for _, step := range wf.Steps {
		if step.Kind != "agent_panel" || step.Panel == nil {
			continue
		}
		key := step.ID + "/synthesis"
		synthesizer, ok := registry.Get(step.Agent)
		if !ok {
			return fmt.Errorf("panel step %q references unknown synthesizer %q", step.ID, step.Agent)
		}
		if synthesizer.Name != "review-synthesizer" || cliagents.DeclaredBinding(synthesizer) {
			return fmt.Errorf("panel step %q requires provider-neutral review-synthesizer agent", step.ID)
		}
		digest, err := synthesizer.DefinitionDigest()
		if err != nil {
			return err
		}
		var binding agentBinding
		if prior != nil {
			pinned, ok := prior.PanelBindings[key]
			if !ok {
				return fmt.Errorf("panel synthesis binding %q is missing", key)
			}
			binding, err = cliagents.ResolvePinnedAgentBinding(synthesizer, opts, pinned.ProviderName, pinned.Model)
		} else {
			binding, err = cliagents.ResolveAgentBinding(synthesizer, opts)
		}
		if err != nil {
			return fmt.Errorf("panel synthesis binding %q is not authorized: %w", key, err)
		}
		if binding.Completer == nil {
			return fmt.Errorf("panel synthesis binding %q has no usable completer", key)
		}
		next := workflowledger.PanelBindingSnapshot{
			StepID: step.ID, MemberID: "synthesis", AgentName: synthesizer.Name, AgentDigest: digest,
			ProviderName: binding.ProviderName, Model: binding.Model,
			TemplateDigest: digestBytes(snapshot.Templates[step.Template].Bytes),
			SchemaDigest:   digestBytes(snapshot.Schemas[step.OutputSchema].Bytes),
		}
		if prior != nil {
			pinned := prior.PanelBindings[key]
			pinned.SkillDigest = ""
			if pinned != next {
				return fmt.Errorf("panel synthesis binding %q changed since workflow admission; recover with: restore the agent and reference content to its admitted state or start a fresh run", key)
			}
		}
		snapshot.PanelBindings[key] = next
	}
	return nil
}

func workflowRuntimeBinding(agent agents.ResolvedAgent, pinned workflowledger.AgentSnapshot, resume bool, opts SessionDispatcherOpts) (agentBinding, error) {
	if resume {
		return cliagents.ResolvePinnedAgentBinding(agent, opts, pinned.ProviderName, pinned.Model)
	}
	return cliagents.ResolveAgentBinding(agent, opts)
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
