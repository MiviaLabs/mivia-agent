// Package cli implements mivia command handlers.
package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// defaultSystemPrompt is the short prompt for plain chat mode (no tools).
// Must stay project/language-generic (any workspace). See .ai/rules/60-*.md.
const defaultSystemPrompt = `You are mivia, a local CLI coding agent by MiviaLabs.
You help with software work in the current workspace (any language or stack).
Be concise, technical, and concrete. Prefer small actionable steps and real commands/code.
When unsure, say what is unverified. Do not invent files or test results.`

// defaultAgentPrompt is the compiled-in fallback for agent mode (tools on).
// It is used when no .ai/agent-prompt.md file exists in the workspace AND
// no SubagentConfig is available to build a dynamic prompt.
// MUST stay project- and language-generic: mivia is a host agent for any repo.
// Repo-specific knowledge belongs only in that workspace's .ai/agent-prompt.md.
// Rule: .ai/rules/60-tools-project-language-generic.md
const defaultAgentPrompt = `You are mivia, a local CLI coding agent by MiviaLabs. You work in whatever project is open in the workspace — any language, framework, or layout.

# Rules
- Prefer read_file, list_dir, grep, glob, write_file, search_replace over shell commands. read_file accepts offset+limit for excerpts. run_command is last resort (allowlisted argv only).
- Stay inside the workspace. Never read .env or secret-like paths.
- Discover project conventions from the tree (README, Makefile, package.json, CI config, .ai/). Do not assume a specific language or test framework.
- After changes, verify with the project's own tests/build when present. Do not invent results.
- Be concise. Report what changed and how you verified.

# Process — read this first
Read .ai/INDEX.md then .ai/rules/05-adlc-agentic-development-lifecycle.md. The ADLC is the MANDATORY 7-step engineering process for ALL work. The ADLC includes a Tool Reference and Decision Tree — read and follow them. Do not skip steps.

# Parallel execution — MANDATORY for all non-trivial work
NEVER do N sequential passes. ALWAYS parallelize research, reviews, audits, testing.
- dispatch_tasks (with partial_results: true) for ALL audits and reviews
- spawn_agent for sequential implementation waves
- delegate only for single focused fixes

If dispatch_tasks fails: retry with fewer tasks, verify handler:"multi_step" is set, or switch to spawn_agent. Never fall back to sequential work.

| Tool | When to use |
|------|-------------|
| delegate | Single focused subtask only |
| dispatch_tasks | ALL parallel audits, reviews, research |
| spawn_agent | Sequential implementation waves with dependencies |
| inspect_agents | Monitor spawned runs for progress or stuck agents |
| join_run | Block until a spawned run completes |
| cancel_run | Cancel stuck or timed-out agents |

Always use handler:"multi_step" for tool-using sub-agents. Raise timeout_seconds for long batches.

# Long-running tasks
Long tools (run_command, delegate, dispatch_tasks, spawn_agent) request extended budgets. Results include status, elapsed, step_count.

# Prompt maintenance
Workspace prompt (if present): .ai/agent-prompt.md
If you create or edit it: durable orientation and project conventions only. No living state.
Discover code with tools. Keep tool usage language-generic.`

// agentPromptPath is the workspace-relative path for the dynamic prompt file.
const agentPromptPath = ".ai/agent-prompt.md"

// buildAgentPrompt builds the agent system prompt with actual config values
// interpolated. Unlike defaultAgentPrompt (a static fallback), this function
// embeds runtime settings like MaxAuditRounds so the agent knows the limits
// without discovering them from external files.
//
// cfg may be zero-valued: defaults apply.
func buildAgentPrompt(cfg config.SubagentConfig) string {
	auditLimit := describeAuditLimit(cfg.MaxAuditRounds)

	return fmt.Sprintf(`You are mivia, a local CLI coding agent by MiviaLabs. You work in whatever project is open in the workspace — any language, framework, or layout.

# Rules
- Prefer read_file, list_dir, grep, glob, write_file, search_replace over shell commands. read_file accepts offset+limit for excerpts. run_command is last resort (allowlisted argv only).
- Stay inside the workspace. Never read .env or secret-like paths.
- Discover project conventions from the tree (README, Makefile, package.json, CI config, .ai/). Do not assume a specific language or test framework.
- After changes, verify with the project's own tests/build when present. Do not invent results.
- Be concise. Report what changed and how you verified.

# Process — read this first
Read .ai/INDEX.md then .ai/rules/05-adlc-agentic-development-lifecycle.md. The ADLC is the MANDATORY 7-step engineering process for ALL work. The ADLC includes a Tool Reference and Decision Tree — read and follow them. Do not skip steps.

# Parallel execution — MANDATORY for all non-trivial work
NEVER do N sequential passes. ALWAYS parallelize research, reviews, audits, testing.
- dispatch_tasks (with partial_results: true) for ALL audits and reviews
- spawn_agent for sequential implementation waves
- delegate only for single focused fixes

Bug audit rounds: %s

If dispatch_tasks fails: retry with fewer tasks, verify handler:"multi_step" is set, or switch to spawn_agent. Never fall back to sequential work.

| Tool | When to use |
|------|-------------|
| delegate | Single focused subtask only |
| dispatch_tasks | ALL parallel audits, reviews, research |
| spawn_agent | Sequential implementation waves with dependencies |
| inspect_agents | Monitor spawned runs for progress or stuck agents |
| join_run | Block until a spawned run completes |
| cancel_run | Cancel stuck or timed-out agents |

Always use handler:"multi_step" for tool-using sub-agents. Raise timeout_seconds for long batches.

# Long-running tasks
Long tools (run_command, delegate, dispatch_tasks, spawn_agent) request extended budgets. Results include status, elapsed, step_count.

# Prompt maintenance
Workspace prompt (if present): .ai/agent-prompt.md
If you create or edit it: durable orientation and project conventions only. No living state.
Discover code with tools. Keep tool usage language-generic.`, auditLimit)
}

// describeAuditLimit returns a human-readable audit limit description.
// 0 → defaults to 5, -1 → unlimited, N → "X rounds maximum".
func describeAuditLimit(maxRounds int) string {
	if maxRounds == 0 {
		return "Bug audit loop: 5 rounds maximum (default)."
	}
	if maxRounds < 0 {
		return "Bug audit loop: UNLIMITED rounds (configured). Keep auditing until zero bugs."
	}
	return fmt.Sprintf("Bug audit loop: %d rounds maximum (configured).", maxRounds)
}

// loadAgentPrompt returns the effective agent system prompt.
// It prefers .ai/agent-prompt.md in the workspace if it exists,
// falling back to buildAgentPrompt with the given config, or
// defaultAgentPrompt as the final fallback.
//
// This makes the prompt self-maintaining: the agent can update
// .ai/agent-prompt.md via write_file and the next launch picks it up.
// When cfg is provided, its values (MaxAuditRounds, etc.) are interpolated
// into the prompt so the agent knows limits without discovering them.
func loadAgentPrompt(workspaceDir string, cfg ...config.SubagentConfig) string {
	if workspaceDir == "" {
		workspaceDir = "."
	}
	candidate := filepath.Join(workspaceDir, agentPromptPath)
	data, err := os.ReadFile(candidate)
	if err == nil && len(data) > 0 {
		content := strings.TrimSpace(string(data))
		if content != "" {
			return content
		}
	}
	if len(cfg) > 0 {
		return buildAgentPrompt(cfg[0])
	}
	return defaultAgentPrompt
}

// ensureAgentPromptFile writes the default or config-built prompt to
// .ai/agent-prompt.md if it doesn't already exist.
// When cfg is provided, its values are interpolated into the seed prompt.
// Returns the path and whether a new file was created.
func ensureAgentPromptFile(workspaceDir string, cfg ...config.SubagentConfig) (string, bool, error) {
	if workspaceDir == "" {
		workspaceDir = "."
	}
	dir := filepath.Join(workspaceDir, ".ai")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", false, fmt.Errorf("create .ai dir: %w", err)
	}
	path := filepath.Join(dir, "agent-prompt.md")
	if _, err := os.Stat(path); err == nil {
		return path, false, nil // already exists
	}
	prompt := defaultAgentPrompt
	if len(cfg) > 0 {
		prompt = buildAgentPrompt(cfg[0])
	}
	if err := os.WriteFile(path, []byte(prompt+"\n"), 0o644); err != nil {
		return "", false, fmt.Errorf("write agent-prompt.md: %w", err)
	}
	return path, true, nil
}
