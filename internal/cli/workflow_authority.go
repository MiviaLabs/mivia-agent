package cli

import (
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

var workflowWritePathDenylist = []string{
	workspace.Namespace + "/mivia.toml",
	workspace.Namespace + "/agents",
	workspace.Namespace + "/policy",
	workspace.Namespace + "/rules",
	workspace.Namespace + "/skills",
	workspace.Namespace + "/workflows",
	".git",
	"go.mod",
	"go.sum",
	"go.work",
}

func workflowDefaultRegistry(root string, res *config.Resolved) (*tools.Registry, error) {
	ws, err := workspace.Open(root)
	if err != nil {
		return nil, err
	}
	tc := res.Tools
	return tools.NewDefaultRegistry(tools.DefaultOptions{
		Workspace: ws, TavilyAPIKey: res.TavilyAPIKey,
		RunAllowlist: tc.RunAllowlist, RunAllowlistOnly: tc.RunAllowlistOnly,
		RunBlocklist: tc.RunBlocklist, DisableTools: tc.DisableTools,
		EnvAllowlist: tc.EnvAllowlist, EnvAllowlistOnly: tc.EnvAllowlistOnly,
		EnvBlocklist: tc.EnvBlocklist, EnvAllowKeywordBlocklist: tc.EnvAllowKeywordBlocklist,
		RunTimeoutSec: tc.RunTimeoutSec, MaxReadBytes: tc.MaxReadBytes,
		MaxWriteKB: tc.MaxWriteKB, MaxOutputBytes: tc.MaxOutputBytes,
		MaxListDirEntries: tc.MaxListDirEntries, MaxToolResultBytes: tc.MaxToolResultBytes,
		MaxTavilyResponseBytes: tc.MaxTavilyResponseBytes, MaxFetchKB: tc.MaxFetchKB,
		MemoryBackstopBytes: tc.MemoryBackstopMB << 20,
		SecretPathPatterns:  tc.SecretPathPatterns, SecretPathExceptions: tc.SecretPathExceptions,
		WritePathDenylist:    workflowWritePathDenylist,
		SearchIgnorePatterns: tc.SearchIgnorePatterns,
	}), nil
}

func workflowWriteAuthority(wf *compiler.CompiledWorkflow, registry *agents.AgentRegistry, authority *tools.Registry, extraDenylist []string) (bool, error) {
	writeCapable := false
	seen := make(map[string]bool)
	for _, step := range wf.Steps {
		if step.Kind != "agent" && step.Kind != "agent_gate" {
			continue
		}
		if seen[step.Agent] {
			continue
		}
		seen[step.Agent] = true
		agent, ok := registry.Get(step.Agent)
		if !ok {
			return false, fmt.Errorf("workflow step %q references unknown agent %q", step.ID, step.Agent)
		}
		surface := tools.ScopedRegistry(authority, tools.ScopeOptions{
			Mode: tools.ScopeSpawned, Allowlist: agents.AllowlistSet(agent.EffectiveTools),
			ExtraDenylist: extraDenylist,
		})
		if _, ok := surface.Get(tools.RunCommandToolName); ok {
			return false, fmt.Errorf("workflow agent %q may not use run_command", agent.Name)
		}
		for _, tool := range surface.List() {
			if surface.Capability(tool.Name(), nil).Class == tools.ExecutionWrite {
				writeCapable = true
			}
		}
	}
	return writeCapable, nil
}
