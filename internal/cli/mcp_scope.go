package cli

import (
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// authorizedAgentTools adds discovered MCP tool names for the agent's selected
// servers. The server selection is the authority. An agent file does not need
// to repeat volatile remote tool names in its tools list.
func authorizedAgentTools(agent *agents.ResolvedAgent, registry *tools.Registry) []string {
	if agent == nil {
		return nil
	}
	if registry == nil {
		return append([]string(nil), agent.EffectiveTools...)
	}
	allowed := make(map[string]struct{}, len(agent.EffectiveTools))
	for _, name := range agent.EffectiveTools {
		allowed[name] = struct{}{}
	}
	for _, tool := range registry.List() {
		if isMCPServerTool(tool.Name(), agent) {
			allowed[tool.Name()] = struct{}{}
		}
	}
	out := make([]string, 0, len(allowed))
	for _, tool := range registry.List() {
		if _, ok := allowed[tool.Name()]; ok {
			out = append(out, tool.Name())
		}
	}
	return out
}

// isMCPServerTool reports whether name is a tool discovered from one of
// agent's selected MCP servers - the "mcp__<serverID>__x<hex>" encoding
// internal/mcp.EncodeToolName produces. Shared by authorizedAgentTools
// (server selection grants AUTHORITY over its tools) and the core tool tier
// (server selection also exempts its tools from deferral - see
// withMCPServerToolsAlwaysCore in tool_tiers.go): both need the same rule
// because an MCP tool's name is a runtime hash, never something an operator
// or agent file can spell out ahead of time.
func isMCPServerTool(name string, agent *agents.ResolvedAgent) bool {
	if agent == nil {
		return false
	}
	for _, serverID := range agent.EffectiveMCPServers {
		if strings.HasPrefix(name, "mcp__"+serverID+"__") {
			return true
		}
	}
	return false
}
