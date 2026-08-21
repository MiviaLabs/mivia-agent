package cli

import (
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/composition"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/mcp"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

func sessionMCPConfig(res *config.Resolved) config.MCPConfig {
	if res == nil {
		return config.MCPConfig{}
	}
	return res.MCP
}

func setupSessionMCPTools(registry *tools.Registry, cfg *config.Resolved, selected *agents.ResolvedAgent) (*mcp.Manager, func(), error) {
	var serverIDs []string
	if selected != nil {
		serverIDs = selected.EffectiveMCPServers
	}
	return composition.AttachMCPServers(registry, sessionMCPConfig(cfg), redactionPolicyOf(cfg), serverIDs)
}

func addMCPTools(registry *tools.Registry, cfg *config.Resolved, serverIDs []string) (func(), error) {
	_, cleanup, err := composition.AttachMCPServers(registry, sessionMCPConfig(cfg), redactionPolicyOf(cfg), serverIDs)
	if err != nil {
		return nil, err
	}
	return cleanup, nil
}

func redactionPolicyOf(cfg *config.Resolved) *redact.Policy {
	if cfg == nil {
		return nil
	}
	return cfg.RedactionPolicy
}

func ensureSelectedMCPTools(state *agentSessionState, selected agents.ResolvedAgent) error {
	if state == nil {
		return nil
	}
	return composition.MergeMCPTools(state.ToolBase, state.MCPManager, selected.EffectiveMCPServers)
}

func ensureMCPServerTools(registry *tools.Registry, manager *mcp.Manager) func([]string) error {
	var mu sync.Mutex
	return func(ids []string) error {
		mu.Lock()
		defer mu.Unlock()
		return composition.MergeMCPTools(registry, manager, ids)
	}
}
