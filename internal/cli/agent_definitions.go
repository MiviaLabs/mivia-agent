package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
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
	// Pre-merge dual-origin catalogue so user skills win over workspace
	// shadowing when resolving agent skills allowlists (plan 06).
	catalogue, catWarnings := buildSkillCatalogue(workspaceRoot)
	// Prefer gate from user config (same as LoadAndResolve).
	globalPreview, _ := config.LoadAgentsGlobal(workspaceRoot)
	reg, global, warnings, err := agents.LoadAndResolveOpts(workspaceRoot, agents.LoadResolveOptions{
		SkillNames:         skillNames,
		SkillCatalogue:     catalogue,
		AllowProjectSkills: globalPreview.LoadWorkspaceConfig,
	})
	if err != nil {
		return agentLoadResult{}, err
	}
	warnings = append(catWarnings, warnings...)
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

// buildSkillCatalogue scans user and project skill roots separately so both
// origins are visible for allowlist trust decisions (project cannot silently
// replace a user skill binding).
func buildSkillCatalogue(workspaceRoot string) (map[string]agents.SkillCatalogueEntry, []string) {
	var warnings []string
	out := make(map[string]agents.SkillCatalogueEntry)
	add := func(dir string, origin skills.Origin) {
		if strings.TrimSpace(dir) == "" {
			return
		}
		reg, w, err := skills.LoadMarkdownSources([]skills.Source{{Dir: dir, Origin: origin}}, skills.LoadOptions{})
		if err != nil {
			warnings = append(warnings, "skip skill catalogue source")
			return
		}
		warnings = append(warnings, w...)
		if reg == nil {
			return
		}
		for _, def := range reg.List() {
			e := out[def.Name]
			switch origin {
			case skills.OriginUser:
				e.User = true
			case skills.OriginProject:
				e.Project = true
			}
			out[def.Name] = e
		}
	}
	add(workspace.UserSkillsDir(), skills.OriginUser)
	root := workspaceRoot
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	add(workspace.SkillsDir(root), skills.OriginProject)
	return out, warnings
}

// warnHookLoad surfaces lifecycle-hook diagnostics at startup, not in debug
// output. A silently ignored hook is how someone concludes hooks are broken.
func warnHookLoad(warnings []string) {
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
}

func warnAgentLoad(warnings []string) {
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
}
