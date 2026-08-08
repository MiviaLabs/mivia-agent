package cli

import (
	"context"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/mcp"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

func sessionMCPConfig(res *config.Resolved) config.MCPConfig {
	if res == nil {
		return config.MCPConfig{}
	}
	return res.MCP
}

func addRootMCPTools(registry *tools.Registry, cfg *config.Resolved, selected *agents.ResolvedAgent) (func(), error) {
	if registry == nil || cfg == nil || selected == nil || len(selected.EffectiveMCPServers) == 0 {
		return func() {}, nil
	}
	return addMCPTools(registry, cfg, selected.EffectiveMCPServers)
}

func addMCPTools(registry *tools.Registry, cfg *config.Resolved, serverIDs []string) (func(), error) {
	if registry == nil || cfg == nil || len(serverIDs) == 0 {
		return func() {}, nil
	}
	manager, err := mcp.NewManager(cfg.MCP, mcp.ManagerOptions{RedactionPolicy: cfg.RedactionPolicy})
	if err != nil {
		return nil, err
	}
	wrappers, err := manager.EnsureServers(context.Background(), serverIDs)
	if err != nil {
		_ = manager.Close()
		return nil, err
	}
	for _, wrapper := range wrappers {
		if _, exists := registry.Get(wrapper.Name()); exists {
			_ = manager.Close()
			return nil, fmt.Errorf("MCP tool %q collides with registry", wrapper.Name())
		}
	}
	for _, wrapper := range wrappers {
		registry.Register(wrapper)
	}
	return func() { _ = manager.Close() }, nil
}
