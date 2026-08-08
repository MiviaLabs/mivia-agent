package cli

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

// agentSessionContext is a value snapshot for dispatcher construction
// (startup and model switch). Mutable session state lives in agentSessionState.
type agentSessionContext struct {
	Global   config.AgentsGlobal
	Selected *agents.ResolvedAgent
	Registry *agents.AgentRegistry
	// AllowProjectSkills is true when workspace skill handlers may register.
	AllowProjectSkills bool
}

// applyWorkspacePromptGate strips untrusted workspace system prompts when the
// user gate is off. User config prompts always load.
func applyWorkspacePromptGate(res *config.Resolved, global config.AgentsGlobal) {
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
func scopedRootRegistry(registry *tools.Registry, selected *agents.ResolvedAgent, extraDenylist []string) (*tools.Registry, []string) {
	if registry == nil || selected == nil {
		return registry, nil
	}
	// The scope allowlist can only contain tools the registry can actually
	// serve, so the disabled report must be derived from the agent's REQUESTED
	// tools instead: authorizedAgentTools is registry-filtered, and intersecting
	// it with the same registry always yields an empty disabled list, which made
	// the attach diagnostic (warnDisabledAgentTools at the attach entry point)
	// dead code. disabledForAgent compares the agent's effective tools against
	// the live registry - the exact report the operator needs at attach, and the
	// same derivation the /agent entry point already uses.
	kept, _ := agents.IntersectWithRegistry(authorizedAgentTools(selected, registry), registry)
	disabled := disabledForAgent(selected, registry)
	return tools.ScopedRegistry(registry, tools.ScopeOptions{
		Mode:          tools.ScopeRoot,
		Allowlist:     agents.AllowlistSet(kept),
		ExtraDenylist: extraDenylist,
	}), disabled
}

// disabledForAgent lists the selected agent's tool names a registry cannot
// offer. /agent is an entry point, so it may report them; the surface builds it
// triggers stay silent because they also run mid-turn, under the TUI.
func disabledForAgent(selected *agents.ResolvedAgent, base *tools.Registry) []string {
	if selected == nil {
		return nil
	}
	_, disabled := agents.IntersectWithRegistry(selected.EffectiveTools, base)
	return disabled
}

// warnDisabledAgentTools reports the selected agent's tool names the live
// registry cannot offer. Call it only from a session entry point the operator
// initiated (attach, /agent), never from a surface rebuild.
func warnDisabledAgentTools(selected *agents.ResolvedAgent, disabled []string) {
	if selected == nil || len(disabled) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "warning: agent %q: disabled tools omitted from registry: %s\n",
		selected.Name, strings.Join(disabled, ", "))
}

// filterSkillRegistryForGate omits project-origin skills when the workspace
// gate is off. User skills remain registered.
func filterSkillRegistryForGate(skillReg *skills.Registry, allowProject bool) *skills.Registry {
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
func applySelectedAgentPrompt(sess *chat.Session, res *config.Resolved, selected *agents.ResolvedAgent) {
	if selected == nil || sess == nil {
		return
	}
	prompt, maxSteps := sess.AgentSettings()
	if strings.TrimSpace(selected.SystemPrompt) != "" {
		prompt = selected.SystemPrompt
	}
	if selected.MaxTurns != nil {
		maxSteps = *selected.MaxTurns
	}
	sess.SetAgentSettings(prompt, maxSteps)
	if res != nil {
		res.SystemPrompt = prompt
	}
}
