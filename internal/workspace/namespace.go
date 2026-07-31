package workspace

import (
	"os"
	"path/filepath"
)

// Namespace is the tool-scoped directory mivia owns beneath a root. Under a
// workspace root it holds project control and runtime files; under the user's
// home directory it holds user-level config and env files.
//
// It is deliberately tool-scoped. The generic name mivia used before this
// belongs to no tool in particular, so claiming it collided with every other
// agent that assumed the same convention and gave users no way to tell whose
// files were whose. The directory is ordinary workspace content, readable and
// writable through the normal file tools like any other path.
//
// Nothing outside this file may name a namespace directory. Resolving through
// one place is what keeps the name changeable and keeps a second convention
// from growing back a call site at a time.
const Namespace = ".mivia"

// NamespacePath joins elem beneath the namespace directory in root. An empty
// root resolves relative to the process working directory.
func NamespacePath(root string, elem ...string) string {
	parts := append([]string{root, Namespace}, elem...)
	return filepath.Join(parts...)
}

// AgentPromptPath is the workspace system prompt. mivia never creates it;
// the user or the agent writes it, and mivia reads it when present.
func AgentPromptPath(root string) string {
	return NamespacePath(root, "agent-prompt.md")
}

// SkillsDir holds workspace skill definitions as <name>/SKILL.md.
func SkillsDir(root string) string { return NamespacePath(root, "skills") }

// UserSkillsDir holds user-level skill definitions. An unavailable home
// directory yields an empty path so callers can warn and continue without
// treating optional user customization as a startup failure.
func UserSkillsDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return SkillsDir(home)
}

// SessionsDir holds persisted chat sessions.
func SessionsDir(root string) string { return NamespacePath(root, "sessions") }
