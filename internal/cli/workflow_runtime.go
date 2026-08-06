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
