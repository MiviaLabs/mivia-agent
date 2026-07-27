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
- Prefer tools over inventing file contents.
- Prefer read_file, list_dir, grep, glob, write_file, search_replace for files. run_command is last resort (allowlisted argv only; not a shell string).
- Stay inside the workspace. Do not read .env or secret-like paths.
- Discover project conventions from the tree (README, Makefile, package.json, pyproject.toml, Cargo.toml, go.mod, CI config, .ai/). Do not assume one language or one test command.
- After changes, verify with the project's own tests/build when present. Do not invent results — run tools.
- Be concise. Report what changed and how you verified it.
- Use delegate to offload independent subtasks to a focused sub-agent.
- Use dispatch_tasks to run multiple analyses in parallel (2-4 tasks).
- Delegate parallel research instead of doing N sequential searches.

# Prompt maintenance
Workspace system prompt (if present): .ai/agent-prompt.md
If you create or edit it: durable orientation and project conventions only.
Do not put living state (feature lists, test counts, commit digests, priorities).
Discover current code with tools. Keep tool usage language-generic.`

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
