package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
)

// agentLoadResult is Layer-B output: resolved definitions and the user gate.
type agentLoadResult struct {
	Registry *agents.AgentRegistry
	Global   config.AgentsGlobal
	Selected *agents.ResolvedAgent // nil when no agent selected
	Warnings []string
}

// loadAgentDefinitions discovers and resolves file-backed agents.
// skillReg may be nil (no skill collision check). When non-nil, agent names
// that collide with skills fail closed.
//
// When agentFlag is empty and a definition named config.DefaultAgentName
// ("mivia") exists, that definition is selected as the root session agent.
// That replaces the former .mivia/agent-prompt.md load path.
func loadAgentDefinitions(workspaceRoot, agentFlag string, skillReg *skills.Registry) (agentLoadResult, error) {
	skillNames := map[string]struct{}{}
	if skillReg != nil {
		for _, info := range skillReg.ListModelFacing(nil) {
			skillNames[info.Name] = struct{}{}
		}
	}
	reg, global, warnings, err := agents.LoadAndResolve(workspaceRoot, skillNames)
	if err != nil {
		return agentLoadResult{}, err
	}
	out := agentLoadResult{Registry: reg, Global: global, Warnings: warnings}
	agentFlag = strings.TrimSpace(agentFlag)
	if agentFlag == "" {
		if _, ok := reg.Get(config.DefaultAgentName); ok {
			agentFlag = config.DefaultAgentName
		} else {
			return out, nil
		}
	}
	selected, err := agents.Select(reg, agentFlag)
	if err != nil {
		return agentLoadResult{}, err
	}
	out.Selected = &selected
	return out, nil
}

func warnAgentLoad(warnings []string) {
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
}
