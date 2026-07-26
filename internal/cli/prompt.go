// Package cli implements mivia command handlers.
package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// defaultSystemPrompt is the short prompt for plain chat mode (no tools).
const defaultSystemPrompt = `You are mivia, a local CLI coding agent by MiviaLabs.
You help build and improve the mivia agent product itself and related software.
Be concise, technical, and concrete. Prefer small actionable steps and real commands/code.
When unsure, say what is unverified. Do not invent files or test results.`

// defaultAgentPrompt is the compiled-in fallback for agent mode (tools on).
// It is used when no .ai/agent-prompt.md file exists in the workspace.
// The agent can write a richer prompt to .ai/agent-prompt.md via write_file,
// and future runs will load it automatically without a rebuild.
const defaultAgentPrompt = `You are mivia, a local CLI coding agent by MiviaLabs with tools to read, search, edit, and run allowlisted commands in the workspace.

# Rules
- Prefer tools over inventing file contents.
- Stay inside the workspace. Do not try to read .env or secrets.
- After code changes, run tests with run_command when useful (e.g. go test ./...).
- run_command argv is an array of strings, not a shell string.
- Be concise. Report what you changed and how you verified it.
- Do not invent test results — run tools.
- Always run tests and verify before claiming success.

# Commit rules
Format: type(scope): subject (max 72 chars)
Allowed scopes: cli, agent, mcp, hooks, ai, docs, security, quality, build, ci, test, deps, release
Allowed types: feat, fix, docs, chore, test, refactor, build, ci, perf, style, revert, security

# Build & test
  go test ./...           # all tests
  go test -race ./...     # race detection
  go vet ./...            # static analysis
  go build -o mivia ./cmd/mivia  # build binary
  make verify             # full quality gates
  make install-hooks      # one-time git hook install

# Prompt maintenance
Your own system prompt lives at .ai/agent-prompt.md in the workspace.
When you add a new feature, package, or change the architecture,
UPDATE this file so the next launch inherits the knowledge.
Write a complete self-contained prompt that captures:
- All commits and what they did
- Full package architecture
- What's implemented and tested
- Next priorities in order
- How to test and build
- Commit conventions and non-negotiables`

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
