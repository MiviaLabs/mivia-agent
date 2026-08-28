// Package cli implements mivia command handlers.
package clichat

import (
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	cliagents "github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// defaultSystemPrompt is the short prompt for plain chat mode (no tools).
// Must stay project/language-generic (any workspace). See rule 60.
const defaultSystemPrompt = `You are mivia, a local CLI coding agent by MiviaLabs.
You help with software work in the current workspace (any language or stack).
Be concise, technical, and concrete. Prefer small actionable steps and real commands/code.
When unsure, say what is unverified. Do not invent files or test results.`

// buildAgentPrompt returns the compiled agent-mode system prompt: the
// compiled general-orchestrator prompt owned by internal/agents, where every
// compiled agent prompt lives (the built-in agents ship from the same
// constants). It is the fallback for ANY workspace and carries only what the
// agent needs to operate; per-tool mechanics live in each tool's
// Description(), and process/lifecycle policy lives in the workspace's own
// skills and agent files, which replace or extend this at session setup.
func buildAgentPrompt(cfg config.SubagentConfig) string {
	_ = cfg
	return agents.BuiltInOrchestratorPrompt
}

// rootPromptForSession composes the INITIAL root system prompt: the compiled
// fallback - or a customized [chat].system_prompt, which skips the fallback
// entirely - plus, for tool-bearing sessions only, the additive subagent
// roster. The roster is environment fact and must reach customized prompts
// too. Never fold this into buildAgentPrompt: that function stays
// registry-free so its language-genericity test and byte budget stay pure.
func rootPromptForSession(useTools bool, res *config.Resolved, registry *agents.AgentRegistry) string {
	prompt := res.SystemPrompt
	if strings.TrimSpace(prompt) == "" {
		if useTools {
			prompt = buildAgentPrompt(res.Subagents)
		} else {
			prompt = defaultSystemPrompt
		}
	}
	if !useTools {
		return prompt
	}
	return cliagents.RootSystemPromptWithRoster(prompt, registry)
}
