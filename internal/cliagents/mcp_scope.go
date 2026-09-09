package cliagents

import (
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

// AuthorizedAgentTools adds discovered MCP tool names for the agent's selected
// servers. The server selection is the authority. An agent file does not need
// to repeat volatile remote tool names in its tools list.
//
// It refuses any name in the agent's EffectiveDenylist, because this function
// GRANTS authority: EffectiveTools has already had every denial removed by
// applyToolPolicy, and the MCP loop below runs after that, so without the
// check it puts denied names back.
//
// Nothing downstream reliably catches that. At ScopeRoot scopeAdmits KEEPS a
// denied name the allowlist carries - deliberately, on the stated grounds
// that "the agent's effective set already excludes these names at resolve
// time", which this loop made false. At ScopeSpawned it refuses only names in
// the ExtraDenylist its caller passed, and two of the three spawned callers
// pass none. So the denial has to be applied here, and it has to travel on
// the AGENT: several callers have no access to the operator's config, and a
// delegated subagent merges its own MCP server's tools into the authority
// registry after root scope has already run.
func AuthorizedAgentTools(agent *agents.ResolvedAgent, registry *tools.Registry) []string {
	if agent == nil {
		return nil
	}
	if registry == nil {
		return append([]string(nil), agent.EffectiveTools...)
	}
	// The agent's own disallowed_tools belong here as well, and they have no
	// second line of defence: the operator's list reaches ScopedRegistry as
	// ExtraDenylist, but disallowed_tools never does - the only thing that
	// ever excluded it was EffectiveTools, which the MCP loop bypasses.
	denied := tools.MandatoryDenylistSet(agent.EffectiveDenylist...)
	allowed := make(map[string]struct{}, len(agent.EffectiveTools))
	for _, name := range agent.EffectiveTools {
		allowed[name] = struct{}{}
	}
	for _, tool := range registry.List() {
		if isMCPServerTool(tool.Name(), agent) && !denied[tool.Name()] {
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
// internal/mcp.EncodeToolName produces. Shared by AuthorizedAgentTools
// (server selection grants AUTHORITY over its tools) and the core tool tier
// (server selection also exempts its tools from deferral - see
// withMCPServerToolsAlwaysCore in tool_tiers.go): both need the same rule
// because these names are derived at runtime from what a server reports,
// rather than being listed in an agent file.
//
// They are NOT unguessable. This comment used to say the name is "a runtime
// hash, never something an operator or agent file can spell out ahead of
// time", and that was the stated justification for granting authority without
// re-checking any denylist. EncodeToolName writes a plain, reversible hex
// encoding of the remote name (internal/mcp/config.go): deterministic, stable
// across runs, and exactly what the tool listing displays. An operator can
// write one in a denylist, so AuthorizedAgentTools has to honour it.
func isMCPServerTool(name string, agent *agents.ResolvedAgent) bool {
	if agent == nil {
		return false
	}
	return isMCPServerToolForServers(name, agent.EffectiveMCPServers)
}

// isMCPServerToolForServers is isMCPServerTool against an explicit server
// scope: an agent's EffectiveMCPServers, or - for the root/no-agent-selected
// identity, which owns no ResolvedAgent - the config's global server set that
// SetupSessionMCPTools attached to the registry (see identityMCPServerScope
// in tool_tiers.go).
func isMCPServerToolForServers(name string, serverIDs []string) bool {
	for _, serverID := range serverIDs {
		if strings.HasPrefix(name, "mcp__"+serverID+"__") {
			return true
		}
	}
	return false
}

// WorkflowMCPServers returns the MCP server IDs referenced by workflow steps,
// in registry order. Only agents actually used in a step are included.
// See cli/workflow_run_build.go for the caller.
func WorkflowMCPServers(wf *definition.CompiledWorkflow, registry *agents.AgentRegistry) []string {
	if wf == nil || registry == nil {
		return nil
	}
	seen := make(map[string]struct{})
	for _, step := range wf.Steps {
		addWorkflowAgentMCPServers(seen, registry, step.Agent)
		if step.Panel != nil {
			for _, member := range step.Panel.Members {
				addWorkflowAgentMCPServers(seen, registry, member.Agent)
			}
		}
	}
	out := make([]string, 0, len(seen))
	for _, serverID := range registryMCPServerOrder(registry) {
		if _, ok := seen[serverID]; ok {
			out = append(out, serverID)
		}
	}
	return out
}

// addWorkflowAgentMCPServers adds the named agent's MCP servers to seen.
func addWorkflowAgentMCPServers(seen map[string]struct{}, registry *agents.AgentRegistry, name string) {
	if name == "" {
		return
	}
	agent, ok := registry.Get(name)
	if !ok {
		return
	}
	for _, serverID := range agent.EffectiveMCPServers {
		seen[serverID] = struct{}{}
	}
}

// registryMCPServerOrder returns MCP server IDs in the order agents were
// registered, deduplicating across agents.
func registryMCPServerOrder(registry *agents.AgentRegistry) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, agent := range registry.List() {
		for _, serverID := range agent.EffectiveMCPServers {
			if _, ok := seen[serverID]; ok {
				continue
			}
			seen[serverID] = struct{}{}
			out = append(out, serverID)
		}
	}
	return out
}
