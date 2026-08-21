package cli

import (
	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

func workflowMCPServers(wf *definition.CompiledWorkflow, registry *agents.AgentRegistry) []string {
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
