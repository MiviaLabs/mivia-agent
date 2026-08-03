package agents

import (
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// LoadResolveOptions is the Layer-B input for agent discovery + resolve.
type LoadResolveOptions struct {
	SkillNames         map[string]struct{}
	SkillCatalogue     map[string]SkillCatalogueEntry
	AllowProjectSkills bool
}

// LoadAndResolve discovers agent files and resolves them into an immutable
// registry. Layer-B entry point used by the CLI; does not build dispatchers.
func LoadAndResolve(workspaceRoot string, skillNames map[string]struct{}) (*AgentRegistry, config.AgentsGlobal, []string, error) {
	return LoadAndResolveOpts(workspaceRoot, LoadResolveOptions{SkillNames: skillNames})
}

// LoadAndResolveOpts is LoadAndResolve with skill-allowlist catalogue options.
func LoadAndResolveOpts(workspaceRoot string, o LoadResolveOptions) (*AgentRegistry, config.AgentsGlobal, []string, error) {
	global, err := config.LoadAgentsGlobal(workspaceRoot)
	if err != nil {
		return nil, config.AgentsGlobal{}, nil, err
	}
	files, discWarnings, err := config.DiscoverAgentFilesTolerant(workspaceRoot, global.LoadWorkspaceConfig)
	if err != nil {
		return nil, global, nil, err
	}
	inputs := make([]ResolveInput, 0, len(files))
	for _, f := range files {
		inputs = append(inputs, ResolveInput{
			Name:   f.Name,
			Source: f.Source,
			Path:   f.Path,
			Spec:   f.Spec,
		})
	}
	allowProject := global.LoadWorkspaceConfig
	if o.SkillCatalogue != nil {
		// Explicit catalogue path: caller owns the gate decision.
		allowProject = o.AllowProjectSkills
	}
	opts := ResolveOptions{
		Global:             global,
		KnownTools:         knownToolSet(tools.DeclaredToolNames()),
		SkillNames:         o.SkillNames,
		ReservedHandlers:   subagents.ReservedHandlerNames(),
		SkillCatalogue:     o.SkillCatalogue,
		AllowProjectSkills: allowProject,
		TolerantWorkspace:  true,
	}
	reg, resolveWarnings, err := ResolveAll(inputs, opts)
	if err != nil {
		return nil, global, nil, err
	}
	warnings := append(append([]string{}, global.Warnings...), discWarnings...)
	warnings = append(warnings, resolveWarnings...)
	return reg, global, warnings, nil
}

// Select returns the named agent or an error listing available names.
func Select(reg *AgentRegistry, name string) (ResolvedAgent, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return ResolvedAgent{}, fmt.Errorf("agent name is empty")
	}
	if reg == nil {
		return ResolvedAgent{}, fmt.Errorf("unknown agent %q (no agents loaded)", name)
	}
	agent, ok := reg.Get(name)
	if !ok {
		available := reg.Names()
		if len(available) == 0 {
			return ResolvedAgent{}, fmt.Errorf("unknown agent %q (no agents loaded)", name)
		}
		return ResolvedAgent{}, fmt.Errorf("unknown agent %q (available: %s)", name, strings.Join(available, ", "))
	}
	return agent, nil
}

// ValidateAgainstCatalogue reports whether toolName is a catalogue typo.
// Returns fatal error for unknown names. Disabled tools are not checked here;
// use IntersectWithRegistry for the live registry.
func ValidateAgainstCatalogue(toolName string, known map[string]struct{}) error {
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return fmt.Errorf("tool name is empty")
	}
	if known == nil {
		known = knownToolSet(tools.DeclaredToolNames())
	}
	if _, ok := known[toolName]; !ok {
		return fmt.Errorf("unknown tool %q (not in catalogue)", toolName)
	}
	return nil
}
