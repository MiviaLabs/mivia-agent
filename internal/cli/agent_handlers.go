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

// agentSessionContext carries the selected agent and gate for every dispatcher
// construction path (startup and model switch). Plan 07 reuses this boundary
// rather than reconstructing handlers separately.
type agentSessionContext struct {
	Global   config.AgentsGlobal
	Selected *agents.ResolvedAgent
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

// applyRootAgentScope intersects the final session registry with the selected
// agent's effective tools using ScopeRoot (privileged/delegation tools kept).
// Must run AFTER NewSessionDispatcher has registered session tools.
func applyRootAgentScope(sess *chat.Session, selected *agents.ResolvedAgent, extraDenylist []string) {
	if sess == nil || sess.Tools == nil || selected == nil {
		return
	}
	kept, disabled := agents.IntersectWithRegistry(selected.EffectiveTools, sess.Tools)
	if len(disabled) > 0 {
		fmt.Fprintf(os.Stderr, "warning: agent %q: disabled tools omitted from registry: %s\n",
			selected.Name, strings.Join(disabled, ", "))
	}
	sess.Tools = tools.ScopedRegistry(sess.Tools, tools.ScopeOptions{
		Mode:          tools.ScopeRoot,
		Allowlist:     agents.AllowlistSet(kept),
		ExtraDenylist: extraDenylist,
	})
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

// applySelectedAgentPrompt replaces the session system prompt when the
// selected agent defines one.
func applySelectedAgentPrompt(sess *chat.Session, res *config.Resolved, selected *agents.ResolvedAgent) {
	if selected == nil || strings.TrimSpace(selected.SystemPrompt) == "" {
		return
	}
	if res != nil {
		res.SystemPrompt = selected.SystemPrompt
	}
	if sess != nil {
		sess.SystemPrompt = selected.SystemPrompt
	}
}
