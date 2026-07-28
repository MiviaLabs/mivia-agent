// Package cli implements mivia command handlers.
package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// defaultSystemPrompt is the short prompt for plain chat mode (no tools).
// Must stay project/language-generic (any workspace). See .ai/rules/60-*.md.
const defaultSystemPrompt = `You are mivia, a local CLI coding agent by MiviaLabs.
You help with software work in the current workspace (any language or stack).
Be concise, technical, and concrete. Prefer small actionable steps and real commands/code.
When unsure, say what is unverified. Do not invent files or test results.`

// defaultAgentPrompt is the compiled-in fallback for agent mode (tools on).
// It is used when no .ai/agent-prompt.md file exists in the workspace.
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
Read .ai/INDEX.md then .ai/rules/05-adlc-agentic-development-lifecycle.md. The ADLC defines the mandatory 7-step process for ALL work and includes a Tool Reference and Decision Tree telling you exactly which orchestration tool to use and when. Follow it.

# Parallel execution — use for ALL non-trivial work
You have tools to run sub-agents in parallel. Use them for research, review, auditing, testing — any work that can be split.

| Tool | When to use |
|------|-------------|
| delegate | Single subtask (oneshot or multi_step with full tools) |
| dispatch_tasks | 2-4 parallel tasks; supports partial results |
| spawn_agent | DAG of tasks with dependencies (depends_on); full async lifecycle |
| inspect_agents | Check progress of a spawned run |
| join_run | Block until a spawned run completes |
| cancel_run | Cancel a running orchestration run |

Parallelize by default. Do N things at once instead of N sequential passes. handler:"multi_step" for tool-using sub-agents. Raise timeout_seconds for long batches.

# Long-running tasks
Long tools (run_command, delegate, dispatch_tasks, spawn_agent) request extended budgets. Results include status, elapsed, step_count.

# Prompt maintenance
Workspace prompt (if present): .ai/agent-prompt.md
If you create or edit it: durable orientation and project conventions only. No living state.
Discover code with tools. Keep tool usage language-generic.`

// agentPromptPath is the workspace-relative path for the dynamic prompt file.
const agentPromptPath = ".ai/agent-prompt.md"

// loadAgentPrompt returns the effective agent system prompt.
// It prefers .ai/agent-prompt.md in the workspace if it exists,
// falling back to the compiled-in defaultAgentPrompt.
//
// This makes the prompt self-maintaining: the agent can update
// .ai/agent-prompt.md via write_file and the next launch picks it up.
func loadAgentPrompt(workspaceDir string) string {
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
	return defaultAgentPrompt
}

// ensureAgentPromptFile writes the default prompt to .ai/agent-prompt.md
// if it doesn't already exist. This seeds the self-maintaining prompt.
// Returns the path and whether a new file was created.
func ensureAgentPromptFile(workspaceDir string) (string, bool, error) {
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
	if err := os.WriteFile(path, []byte(defaultAgentPrompt+"\n"), 0o644); err != nil {
		return "", false, fmt.Errorf("write agent-prompt.md: %w", err)
	}
	return path, true, nil
}
