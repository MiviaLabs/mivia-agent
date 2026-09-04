package cliagents

import (
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/composition"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/mcp"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

func SessionMCPConfig(res *config.Resolved) config.MCPConfig {
	if res == nil {
		return config.MCPConfig{}
	}
	return res.MCP
}

// SetupSessionMCPTools attaches the session's MCP servers. When selected is
// nil (the root/no-agent-selected identity - the default session unless a
// workspace agent named config.DefaultAgentName exists or --agent was
// passed), it falls back to cfg's global server set: the root identity never
// runs through agents.Resolve, so it cannot pick up the same fallback
// resolveMCPServers applies to every resolved agent that names no
// mcp_servers of its own. Without this, a configured `global = true` MCP
// server's tools never reach the root session at all.
func SetupSessionMCPTools(registry *tools.Registry, cfg *config.Resolved, selected *agents.ResolvedAgent) (*mcp.Manager, func(), error) {
	serverIDs := SelectedOrGlobalMCPServers(cfg, selected)
	return composition.AttachMCPServers(registry, SessionMCPConfig(cfg), redactionPolicyOf(cfg), serverIDs)
}

// SelectedOrGlobalMCPServers returns selected's own MCP server scope, or
// cfg's global server set when no agent is selected. See SetupSessionMCPTools
// for why the root identity needs this fallback applied explicitly.
func SelectedOrGlobalMCPServers(cfg *config.Resolved, selected *agents.ResolvedAgent) []string {
	if selected != nil {
		return selected.EffectiveMCPServers
	}
	return SessionMCPConfig(cfg).GlobalServerIDs()
}

func AddMCPTools(registry *tools.Registry, cfg *config.Resolved, serverIDs []string) (func(), error) {
	_, cleanup, err := composition.AttachMCPServers(registry, SessionMCPConfig(cfg), redactionPolicyOf(cfg), serverIDs)
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

func ensureSelectedMCPTools(state *AgentSessionState, selected agents.ResolvedAgent) error {
	if state == nil {
		return nil
	}
	return composition.MergeMCPTools(state.ToolBase, state.MCPManager, selected.EffectiveMCPServers)
}

// ensureRootMCPTools merges every configured global MCP server into state's
// tool base before the session restores the root/no-agent-selected surface.
// A session that started with a named agent selected (--agent, or a
// workspace `mivia` definition) only ever merged that agent's own
// mcp_servers; a global server the agent did not name was never attached.
// Switching back to root must not leave it missing, so this mirrors
// ensureSelectedMCPTools with cfg's global server set instead of one agent's
// scope. A nil state (tools-off session, or a caller with no agent state) is
// a no-op, matching ensureSelectedMCPTools.
func ensureRootMCPTools(res *config.Resolved, state *AgentSessionState) error {
	if state == nil {
		return nil
	}
	return composition.MergeMCPTools(state.ToolBase, state.MCPManager, SessionMCPConfig(res).GlobalServerIDs())
}

func EnsureMCPServerTools(registry *tools.Registry, manager *mcp.Manager) func([]string) error {
	var mu sync.Mutex
	return func(ids []string) error {
		mu.Lock()
		defer mu.Unlock()
		return composition.MergeMCPTools(registry, manager, ids)
	}
}
