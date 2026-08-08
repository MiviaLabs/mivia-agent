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

func setupSessionMCPTools(registry *tools.Registry, cfg *config.Resolved, selected *agents.ResolvedAgent) (*mcp.Manager, func(), error) {
	manager, err := newMCPManager(cfg)
	if err != nil {
		return nil, nil, err
	}
	if selected != nil {
		err = registerMCPTools(registry, manager, selected.EffectiveMCPServers)
	}
	if err != nil {
		if manager != nil {
			_ = manager.Close()
		}
		return nil, nil, err
	}
	return manager, func() {
		if manager != nil {
			_ = manager.Close()
		}
	}, nil
}

func addRootMCPTools(registry *tools.Registry, cfg *config.Resolved, selected *agents.ResolvedAgent) (func(), error) {
	manager, err := newMCPManager(cfg)
	if err != nil || manager == nil {
		return func() {}, err
	}
	if selected != nil {
		err = registerMCPTools(registry, manager, selected.EffectiveMCPServers)
	}
	if err != nil {
		_ = manager.Close()
		return nil, err
	}
	return func() { _ = manager.Close() }, nil
}

func newMCPManager(cfg *config.Resolved) (*mcp.Manager, error) {
	if cfg == nil || !cfg.MCP.Enabled {
		return nil, nil
	}
	return mcp.NewManager(cfg.MCP, mcp.ManagerOptions{RedactionPolicy: cfg.RedactionPolicy})
}

func addMCPTools(registry *tools.Registry, cfg *config.Resolved, serverIDs []string) (func(), error) {
	manager, err := newMCPManager(cfg)
	if err != nil {
		return nil, err
	}
	if manager == nil {
		return func() {}, nil
	}
	if err := registerMCPTools(registry, manager, serverIDs); err != nil {
		_ = manager.Close()
		return nil, err
	}
	return func() { _ = manager.Close() }, nil
}

func registerMCPTools(registry *tools.Registry, manager *mcp.Manager, serverIDs []string) error {
	if registry == nil || manager == nil || len(serverIDs) == 0 {
		return nil
	}
	wrappers, err := manager.EnsureServers(context.Background(), serverIDs)
	if err != nil {
		return err
	}
	for _, wrapper := range wrappers {
		if _, exists := registry.Get(wrapper.Name()); exists {
			if manager.OwnsTool(wrapper.Name()) {
				continue
			}
			return fmt.Errorf("MCP tool %q collides with registry", wrapper.Name())
		}
	}
	for _, wrapper := range wrappers {
		if _, exists := registry.Get(wrapper.Name()); exists {
			continue
		}
		registry.Register(wrapper)
	}
	return nil
}

func ensureSelectedMCPTools(state *agentSessionState, selected agents.ResolvedAgent) error {
	if state == nil {
		return nil
	}
	return registerMCPTools(state.ToolBase, state.MCPManager, selected.EffectiveMCPServers)
}

func ensureMCPServerTools(registry *tools.Registry, manager *mcp.Manager) func([]string) error {
	return func(ids []string) error { return registerMCPTools(registry, manager, ids) }
}
