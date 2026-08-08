package agents

import (
	"fmt"
	"slices"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func resolveMCPServers(in ResolveInput, parent *ResolvedAgent, cfg config.MCPConfig) ([]string, error) {
	known := make(map[string]config.MCPServerConfig, len(cfg.Servers))
	for _, server := range cfg.Servers {
		known[server.ID] = server
	}
	if in.Spec.MCPServers == nil {
		if parent != nil {
			return slices.Clone(parent.EffectiveMCPServers), nil
		}
		if !cfg.Enabled {
			return nil, nil
		}
		out := make([]string, 0, len(cfg.Servers))
		for _, server := range cfg.Servers {
			if server.Global {
				out = append(out, server.ID)
			}
		}
		return out, nil
	}

	allowedByParent := make(map[string]bool)
	if parent != nil {
		for _, id := range parent.EffectiveMCPServers {
			allowedByParent[id] = true
		}
	}
	seen := make(map[string]bool, len(*in.Spec.MCPServers))
	out := make([]string, 0, len(*in.Spec.MCPServers))
	for _, id := range *in.Spec.MCPServers {
		if !canonicalMCPID(id) {
			return nil, fmt.Errorf("agent %q: MCP server ID %q is not canonical", in.Name, id)
		}
		if seen[id] {
			return nil, fmt.Errorf("agent %q: duplicate MCP server %q", in.Name, id)
		}
		seen[id] = true
		server, ok := known[id]
		if !ok || !cfg.Enabled {
			return nil, fmt.Errorf("agent %q: MCP server %q is unknown or disabled", in.Name, id)
		}
		if parent != nil && !allowedByParent[id] {
			return nil, fmt.Errorf("agent %q: MCP server %q is not allowed by parent", in.Name, id)
		}
		if parent == nil && in.Source == config.AgentSourceWorkspace && !server.Global {
			return nil, fmt.Errorf("agent %q: workspace root cannot select non-global MCP server %q", in.Name, id)
		}
		out = append(out, id)
	}
	return out, nil
}

func canonicalMCPID(id string) bool {
	if len(id) == 0 || len(id) > 63 || id[0] < 'a' || id[0] > 'z' {
		return false
	}
	for _, r := range id[1:] {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}
