package cli

import (
	"fmt"
	"slices"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

var workflowWritePathDenylist = []string{
	".mivia/mivia.toml",
	".mivia/agents",
	".mivia/policy",
	".mivia/rules",
	".mivia/skills",
	".mivia/workflows",
	".git",
	"go.mod",
	"go.sum",
	"go.work",
}

var panelReviewerTools = []string{"find_references", "glob", "grep", "list_dir", "read_file"}

func validatePanelAgentTools(agent agents.ResolvedAgent, skillName string, opts SessionDispatcherOpts, synthesizer bool) error {
	authority := opts.authority()
	if authority == nil {
		return fmt.Errorf("panel agent %q has no runtime tool registry", agent.Name)
	}
	surface := tools.ScopedRegistry(authority, tools.ScopeOptions{
		Mode: tools.ScopeSpawned, Allowlist: agents.AllowlistSet(agent.EffectiveTools),
	})
	if !slices.Contains(agent.DisallowedTools, toolPostMessage) {
		return fmt.Errorf("panel agent %q must disallow post_message", agent.Name)
	}
	if synthesizer && !agent.AllowEmptyTools {
		return fmt.Errorf("review-synthesizer must declare allow_empty_tools = true")
	}
	if skillName != "" {
		if opts.SkillReg == nil {
			return fmt.Errorf("panel agent %q has no skill registry", agent.Name)
		}
		skill, ok := opts.SkillReg.Get(skillName)
		if !ok {
			return fmt.Errorf("panel agent %q references unknown skill %q", agent.Name, skillName)
		}
		if synthesizer && (len(skill.Tools) != 0 || len(skill.Resources) != 0) {
			return fmt.Errorf("review-synthesizer skill %q must not declare tools or resources", skillName)
		}
		if len(skill.Resources) != 0 {
			activation, err := skill.Activate()
			if err != nil {
				return err
			}
			defer activation.Close()
			surface, err = injectSkillResourceTool(surface, activation)
			if err != nil {
				return err
			}
		}
	}
	injectBaselineMessaging(authority, surface, opts.Config, messagingDisallowed(agent.DisallowedTools))
	names := make([]string, 0, len(surface.List()))
	for _, tool := range surface.List() {
		names = append(names, tool.Name())
	}
	slices.Sort(names)
	want := []string{}
	if !synthesizer {
		want = slices.Clone(panelReviewerTools)
	}
	if !slices.Equal(names, want) {
		return fmt.Errorf("panel agent %q final runtime tools = %v, want %v", agent.Name, names, want)
	}
	return nil
}

func workflowDefaultRegistry(root string, res *config.Resolved) (*tools.Registry, error) {
	ws, err := workspace.Open(root)
	if err != nil {
		return nil, err
	}
	tc := res.Tools
	return tools.NewDefaultRegistry(tools.DefaultOptions{
		Workspace: ws, TavilyAPIKey: res.TavilyAPIKey,
		RunAllowlist: tc.RunAllowlist, RunAllowlistOnly: tc.RunAllowlistOnly,
		RunBlocklist: tc.RunBlocklist, DisableTools: tc.DisableTools,
		EnvAllowlist: tc.EnvAllowlist, EnvAllowlistOnly: tc.EnvAllowlistOnly,
		EnvBlocklist: tc.EnvBlocklist, EnvAllowKeywordBlocklist: tc.EnvAllowKeywordBlocklist,
		RunTimeoutSec: tc.RunTimeoutSec, MaxReadBytes: tc.MaxReadBytes,
		MaxWriteKB: tc.MaxWriteKB, MaxOutputBytes: tc.MaxOutputBytes,
		MaxListDirEntries: tc.MaxListDirEntries, MaxToolResultBytes: tc.MaxToolResultBytes,
		MaxTavilyResponseBytes: tc.MaxTavilyResponseBytes, MaxFetchKB: tc.MaxFetchKB,
		MemoryBackstopBytes: tc.MemoryBackstopMB << 20,
		SecretPathPatterns:  tc.SecretPathPatterns, SecretPathExceptions: tc.SecretPathExceptions,
		WritePathDenylist:    workflowWritePathDenylist,
		SearchIgnorePatterns: tc.SearchIgnorePatterns,
	}), nil
}

func workflowWriteAuthority(wf *compiler.CompiledWorkflow, registry *agents.AgentRegistry, authority *tools.Registry, extraDenylist []string) (bool, error) {
	writeCapable := false
	seen := make(map[string]bool)
	for _, step := range wf.Steps {
		if step.Kind != "agent" && step.Kind != "agent_gate" {
			continue
		}
		if seen[step.Agent] {
			continue
		}
		seen[step.Agent] = true
		agent, ok := registry.Get(step.Agent)
		if !ok {
			return false, fmt.Errorf("workflow step %q references unknown agent %q", step.ID, step.Agent)
		}
		surface := tools.ScopedRegistry(authority, tools.ScopeOptions{
			Mode: tools.ScopeSpawned, Allowlist: agents.AllowlistSet(agent.EffectiveTools),
			ExtraDenylist: extraDenylist,
		})
		if _, ok := surface.Get(tools.RunCommandToolName); ok {
			return false, fmt.Errorf("workflow agent %q may not use run_command", agent.Name)
		}
		for _, tool := range surface.List() {
			if surface.Capability(tool.Name(), nil).Class == tools.ExecutionWrite {
				writeCapable = true
			}
		}
	}
	return writeCapable, nil
}
