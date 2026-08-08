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
		for _, serverID := range agent.EffectiveMCPServers {
			if strings.HasPrefix(tool.Name(), "mcp__"+serverID+"__") {
				allowed[tool.Name()] = struct{}{}
				break
			}
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
