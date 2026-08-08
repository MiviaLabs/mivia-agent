package cli

import (
	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
)

func workflowMCPServers(wf *compiler.CompiledWorkflow, registry *agents.AgentRegistry) []string {
	if wf == nil || registry == nil {
		return nil
	}
	seen := make(map[string]struct{})
	for _, step := range wf.Steps {
		if step.Agent == "" {
			continue
		}
		agent, ok := registry.Get(step.Agent)
		if !ok {
			continue
		}
		for _, serverID := range agent.EffectiveMCPServers {
			seen[serverID] = struct{}{}
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
