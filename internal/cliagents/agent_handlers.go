package cliagents

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// AgentSessionContext is a value snapshot for dispatcher construction
// (startup and model switch). Mutable session state lives in agentSessionState.
type AgentSessionContext struct {
	Global   config.AgentsGlobal
	Selected *agents.ResolvedAgent
	Registry *agents.AgentRegistry
	// AllowProjectSkills is true when workspace skill handlers may register.
	AllowProjectSkills bool
}

// applyWorkspacePromptGate strips untrusted workspace system prompts when the
// user gate is off. User config prompts always load.
func ApplyWorkspacePromptGate(res *config.Resolved, global config.AgentsGlobal) {
	if res == nil || global.LoadWorkspaceConfig {
		return
	}
	userPath := config.UserConfigPath()
	if userPath == "" || res.ConfigPath == "" {
		return
	}
	if sameConfigPath(res.ConfigPath, userPath) {
		return
	}
	res.SystemPrompt = ""
	res.Subagents.SystemPrompt = ""
}

func sameConfigPath(a, b string) bool {
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		ra = filepath.Clean(a)
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		rb = filepath.Clean(b)
	}
	return filepath.Clean(ra) == filepath.Clean(rb)
}

// scopedRootRegistry intersects a registry with the selected agent's effective
// tools using ScopeRoot (privileged/delegation tools kept). It returns the
// scoped registry and the agent's tool names this build could not honour.
//
// It reports rather than prints: this runs on EVERY surface build, including
// the ones a tool admission performs mid-turn, and a raw stderr write while the
// TUI owns the terminal corrupts the rendered frame. Only the attach and
// /agent entry points turn the report into a diagnostic, via
// warnDisabledAgentTools.
func ScopedRootRegistry(registry *tools.Registry, selected *agents.ResolvedAgent, extraDenylist []string) (*tools.Registry, []string) {
	if registry == nil {
		return registry, nil
	}
	if selected == nil {
		// No agent selected, so there is no allowlist to apply - but the
		// operator's denylist still must be. This used to return the registry
		// untouched, which made mandatory_tool_denylist a no-op in the
		// DEFAULT session (no `default` agent and no --agent, `--agent
		// mivia`, or `/agent mivia` all land here). A nil Allowlist means "no
		// agent filter", not "no filtering at all".
		return tools.ScopedRegistry(registry, tools.ScopeOptions{
			Mode:          tools.ScopeRoot,
			ExtraDenylist: extraDenylist,
		}), nil
	}
	kept, disabled := agents.IntersectWithRegistry(AuthorizedAgentTools(selected, registry), registry)
	return tools.ScopedRegistry(registry, tools.ScopeOptions{
		Mode:          tools.ScopeRoot,
		Allowlist:     agents.AllowlistSet(kept),
		ExtraDenylist: extraDenylist,
	}), disabled
}

// disabledForAgent lists the selected agent's tool names a registry cannot
// offer. /agent is an entry point, so it may report them; the surface builds it
// triggers stay silent because they also run mid-turn, under the TUI.
func DisabledForAgent(selected *agents.ResolvedAgent, base *tools.Registry) []string {
	if selected == nil {
		return nil
	}
	_, disabled := agents.IntersectWithRegistry(selected.EffectiveTools, base)
	return disabled
}

// warnDisabledAgentTools reports the selected agent's tool names the live
// registry cannot offer. Call it only from a session entry point the operator
// initiated (attach, /agent), never from a surface rebuild.
func WarnDisabledAgentTools(selected *agents.ResolvedAgent, disabled []string) {
	if selected == nil || len(disabled) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "warning: agent %q: disabled tools omitted from registry: %s\n",
		selected.Name, strings.Join(disabled, ", "))
}

// warnAdvertisedToolsTruncated reports when the admissible union exceeded
// tools.MaxAdvertisedTools and was truncated (plan tools-advertising/01). No
// silent caps: a dropped tool is still authorized and executable once
// admitted, but the operator should know it will never be advertised for this
// binding.
func warnAdvertisedToolsTruncated(selected *agents.ResolvedAgent, dropped int) {
	if dropped <= 0 {
		return
	}
	name := ""
	if selected != nil {
		name = selected.Name
	}
	fmt.Fprintf(os.Stderr, "warning: agent %q: %d tool(s) exceed the %d-tool advertising cap and will not be advertised until a future binding\n",
		name, dropped, tools.MaxAdvertisedTools)
}

// filterSkillRegistryForGate omits project-origin skills when the workspace
// gate is off. User skills remain registered.
func FilterSkillRegistryForGate(skillReg *skills.Registry, allowProject bool) *skills.Registry {
	if skillReg == nil || allowProject {
		return skillReg
	}
	out := skills.NewRegistry()
	for _, def := range skillReg.List() {
		if def.Origin == skills.OriginProject {
			continue
		}
		_ = out.Register(def)
	}
	return out
}

// applySelectedAgent applies the selected agent's prompt and turn budget to
// the session. max_turns: nil leaves the session default; 0 means unlimited.
//
// Runs even when selected is nil (plan 77: found via a live smoke test that
// a bare `mivia chat` with no --agent never reached this far otherwise -
// chat_command.go's own fallback prompt resolution runs BEFORE the memory
// store opens and is hardcoded to no injection, so this call, right after
// configureChatWorkspace, is the only site that recomposes the root
// session's prompt with the real memory block for a no-agent session).
func ApplySelectedAgentPrompt(sess *chat.Session, res *config.Resolved, selected *agents.ResolvedAgent, state *AgentSessionState) {
	if sess == nil {
		return
	}
	prompt, maxSteps := sess.AgentSettings()
	if selected != nil {
		if strings.TrimSpace(selected.SystemPrompt) != "" {
			prompt = selected.SystemPrompt
		}
		if selected.MaxTurns != nil {
			maxSteps = *selected.MaxTurns
		}
	}
	sess.SetAgentSettings(prompt, maxSteps, CoreMemoryBlockForState(state))
	if res != nil {
		res.SystemPrompt = prompt
	}
}

// CurrentAgentName implements current agent name.
func CurrentAgentName(state *AgentSessionState) string {
	if state == nil {
		return ""
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.Selected == nil {
		return ""
	}
	return state.Selected.Name
}

func formatAgentAvailable(reg *agents.AgentRegistry) string {
	if reg == nil || reg.Len() == 0 {
		return "(none)"
	}
	rows := make([]string, 0, reg.Len())
	for _, name := range reg.Names() {
		a, ok := reg.Get(name)
		if ok {
			rows = append(rows, name+"("+string(a.Provenance.Source)+")")
		}
	}
	return strings.Join(rows, ", ")
}

// FormatAgentSet implements format agent set.
func FormatAgentSet(name string) string {
	return "agent set to " + name
}

// FormatAgentCurrent implements format agent current.
func FormatAgentCurrent(name string, reg *agents.AgentRegistry) string {
	if name == "" {
		name = "(compiled default)"
	}
	return fmt.Sprintf("current agent=%s\nusage: /agent <name>\navailable: %s", name, formatAgentAvailable(reg))
}
