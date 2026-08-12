package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// effectiveWorkflowWriteDenylist is the write-path blocklist for workflow
// agent steps: the built-in defaults (config.DefaultWritePathBlocklist: .git
// and .mivia/mivia.toml) plus the project's [tools] write_path_blocklist
// additions, minus the project's [tools] write_path_blocklist_remove
// removals. The defaults are removable only by explicit opt-out, because
// unblocking .git or .mivia/mivia.toml is a trust decision: the config file
// carries the blocklist itself, and Git metadata carries history and hooks.
// Composing here (instead of in resolveToolsConfig) guarantees the defaults
// even for a directly-constructed config.Resolved; duplicate entries are
// harmless because the matcher is membership-based.
func effectiveWorkflowWriteDenylist(res *config.Resolved) []string {
	var additions, removals []string
	if res != nil {
		additions = res.Tools.WritePathBlocklist
		removals = res.Tools.WritePathBlocklistRemove
	}
	list := append(slices.Clone(config.DefaultWritePathBlocklist), additions...)
	if len(removals) == 0 {
		return list
	}
	removed := make(map[string]bool, len(removals))
	for _, r := range removals {
		removed[r] = true
	}
	return slices.DeleteFunc(list, func(entry string) bool { return removed[entry] })
}

var panelReviewerTools = []string{"find_references", "glob", "grep", "list_dir", "read_file"}

func validatePanelAgentTools(agent agents.ResolvedAgent, skillName string, opts SessionDispatcherOpts, synthesizer bool) error {
	authority := opts.authority()
	if authority == nil {
		return fmt.Errorf("panel agent %q has no runtime tool registry", agent.Name)
	}
	surface := tools.ScopedRegistry(authority, tools.ScopeOptions{
		Mode: tools.ScopeSpawned, Allowlist: agents.AllowlistSet(authorizedAgentTools(&agent, authority)),
	})
	if !slices.Contains(agent.DisallowedTools, toolPostMessage) {
		return fmt.Errorf("panel agent %q must disallow post_message", agent.Name)
	}
	if synthesizer && !agent.AllowEmptyTools {
		return fmt.Errorf("review-synthesizer must declare allow_empty_tools = true")
	}
	// skillHasResources mirrors the injectSkillResourceTool branch below so
	// the expected tool set can include the scoped reader exactly when the
	// runtime surface does.
	skillHasResources := false
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
			skillHasResources = true
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
		// A member skill that declares resources gets the host-injected
		// scoped reader (injectSkillResourceTool) in its runtime surface, so
		// the expected set must carry read_skill_resource too. The committed
		// panel skills are deliberately resource-less JSON-only skills, so
		// this branch stays a generic capability check rather than a panel
		// requirement (the original interactive skills all shipped
		// resources.toml, which is how the gate refused runs on first live use).
		if skillHasResources {
			want = append(want, tools.SkillResourceToolName)
		}
	}
	// MCP tools follow the agent's selected servers for panel members and the
	// review-synthesizer alike: the project marks codegraph and context7
	// global, so workflow agents run with them, and the synthesizer is
	// allowed those read-only MCP tools (it still carries no local tools).
	// The expected set must include every MCP tool the runtime grants, or a
	// live panel can never admit - the synthesizer's second live failure was
	// exactly this: want stayed [] while its surface held the mcp__ tools.
	for _, name := range authorizedAgentTools(&agent, authority) {
		if strings.HasPrefix(name, "mcp__") {
			want = append(want, name)
		}
	}
	slices.Sort(want)
	if !slices.Equal(names, want) {
		return fmt.Errorf("panel agent %q final runtime tools = %v, want %v", agent.Name, names, want)
	}
	return nil
}

// workflowDefaultRegistry builds the tool registry that workflow step agents
// may hold. DiagnosticsCommand is deliberately NOT mapped here: workflow
// steps run no commands by design - the workflow's own evidence gates execute
// checks in the verifier sandbox ("Do not run commands" is in the
// workflow-engineer system prompt, and feature_delivery_contract_test.go pins
// run_command and get_diagnostics absent from its toolset). If this ever
// changes, workflowWriteAuthority must classify get_diagnostics as
// write-capable exactly like run_command (an allowlisted program can write
// anywhere).
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
		WritePathDenylist:    effectiveWorkflowWriteDenylist(res),
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
			Mode: tools.ScopeSpawned, Allowlist: agents.AllowlistSet(authorizedAgentTools(&agent, authority)),
			ExtraDenylist: extraDenylist,
		})
		for _, tool := range surface.List() {
			// run_command is not ExecutionWrite-class (a bare argv is an
			// external program), but a shell program can write anywhere, so a
			// workflow that grants it is write-capable: it must run in an
			// isolated worktree and clear the review gate before delivery.
			// Mirrors tools.WorkspaceWriteCapable. get_diagnostics is the same
			// class (it runs an allowlisted external program): if it is ever
			// granted to a workflow step, it must be added here too.
			if tool.Name() == tools.RunCommandToolName || surface.Capability(tool.Name(), nil).Class == tools.ExecutionWrite {
				writeCapable = true
			}
		}
	}
	return writeCapable, nil
}
