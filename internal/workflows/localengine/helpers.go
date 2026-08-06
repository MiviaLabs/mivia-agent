package localengine

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

func buildStepRuntimes(wf *compiler.CompiledWorkflow) map[string]controller.StepRuntime {
	steps := make(map[string]controller.StepRuntime, len(wf.Steps))
	for _, step := range wf.Steps {
		if step.Kind != "agent" && step.Kind != "agent_gate" {
			continue
		}
		steps[step.ID] = controller.StepRuntime{
			Agent:  agents.ResolvedAgent{Name: step.Agent},
			Digest: "sha256:agent-" + step.Agent,
		}
	}
	return steps
}

func validateInputs(inputs map[string]any, defs map[string]definition.InputDef) (map[string]any, map[string]string, error) {
	if inputs == nil {
		inputs = map[string]any{}
	}
	values := make(map[string]any, len(inputs))
	snapshot := make(map[string]string, len(inputs))
	for key, value := range inputs {
		def, ok := defs[key]
		if !ok {
			return nil, nil, fmt.Errorf("unknown workflow input %q", key)
		}
		str, err := inputToString(value, def.Type)
		if err != nil {
			return nil, nil, fmt.Errorf("workflow input %q: %w", key, err)
		}
		if def.MaxBytes > 0 && len(str) > def.MaxBytes {
			return nil, nil, fmt.Errorf("workflow input %q exceeds %d bytes", key, def.MaxBytes)
		}
		values[key] = value
		if def.Type == "string" {
			if s, ok := value.(string); ok {
				values[key] = s
			}
		}
		snapshot[key] = str
	}
	for key, def := range defs {
		if def.Required {
			if _, ok := values[key]; !ok {
				return nil, nil, fmt.Errorf("required workflow input %q is missing", key)
			}
		}
	}
	return values, snapshot, nil
}

func inputToString(value any, typ string) (string, error) {
	if typ == "string" {
		s, ok := value.(string)
		if !ok {
			return "", fmt.Errorf("value is not a string")
		}
		return s, nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func resolveLocalIdentity(root, runID string) (baseRef, baseCommit, worktree string, err error) {
	if root == "" {
		return "", "", "", fmt.Errorf("no workspace")
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return "", "", "", err
	}
	return "main", "local-base", "workflow-" + runID, nil
}

func randomToken(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf))
}
