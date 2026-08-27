package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// UserHomeDir returns the user-home directory. HOME is a cross-platform
// override for tests and portable automation.
func UserHomeDir() (string, error) {
	if home, ok := os.LookupEnv("HOME"); ok {
		home = strings.TrimSpace(home)
		if home == "" {
			return "", fmt.Errorf("HOME is empty")
		}
		return filepath.Abs(home)
	}
	return os.UserHomeDir()
}

// NamespacePath joins elem beneath the namespace directory in root. An empty
// root resolves relative to the process working directory.
func NamespacePath(root string, elem ...string) string {
	parts := append([]string{root, Namespace}, elem...)
	return filepath.Join(parts...)
}

// AgentsPath joins elem beneath the project's .agents/ directory in root.
// Workspace skills live under .agents/skills/ so the development surface
// (skills, subagent roles, memories, rules, doctrines) stays together and
// is editable without touching the binary's own runtime namespace. The
// remaining runtime state (sessions, runs, worktrees, memory, hooks,
// policies, workflow definitions, agent role TOMLs) keeps living under
// Namespace.
func AgentsPath(root string, elem ...string) string {
	parts := append([]string{root, ".agents"}, elem...)
	return filepath.Join(parts...)
}

// AgentsDir holds workspace agent definitions as <name>.md under .agents/agents/.
func AgentsDir(root string) string { return AgentsPath(root, "agents") }

// SkillsDir holds workspace skill definitions as <name>/SKILL.md. Skills
// live under the project's .agents/skills/ directory so the development
// surface is editable as ordinary workspace content and shares no path
// with the binary's own runtime namespace (.mivia).
func SkillsDir(root string) string { return AgentsPath(root, "skills") }

// UserSkillsDir holds user-level skill definitions in the user's own
// .mivia home namespace (a sibling, not a child, of the project .agents/
// surface). An unavailable home directory yields an empty path so callers
// can warn and continue without treating optional user customization as a
// startup failure.
func UserSkillsDir() string {
	home, err := UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return NamespacePath(home, "skills")
}

// SessionsDir holds persisted chat sessions.
func SessionsDir(root string) string { return NamespacePath(root, "sessions") }

// WorktreesDir holds git worktree checkouts managed by mivia.
func WorktreesDir(repoRoot string) string { return NamespacePath(repoRoot, "worktrees") }

// ContextStorePath holds the always-on durable context checkpoint database
// for a specific workspace root. Callers that need the default install-wide
// store should use GlobalContextStorePath instead; this stays root-scoped for
// callers (workflow runs) that require per-workspace isolation.
func ContextStorePath(root string) string { return NamespacePath(root, "context.db") }

// GlobalContextStorePath is the default durable chat/session store shared by
// every workspace on the machine, so a fresh install already has one history
// instead of a separate database per project directory. Sessions stay
// isolated inside this shared file by workspace ID (contextWorkspaceID), the
// same mechanism that lets managed worktrees share a store safely. An
// unavailable home directory falls back to root-scoped ContextStorePath so
// the caller still gets a usable path.
func GlobalContextStorePath(root string) string {
	home, err := UserHomeDir()
	if err != nil || home == "" {
		return ContextStorePath(root)
	}
	return NamespacePath(home, "context.db")
}

// MemoryDBPath is the default project-scoped memory database (plan 68). A
// repo owner may point [memory] store_path at a tracked path instead and
// commit memories with the repository.
func MemoryDBPath(root string) string { return NamespacePath(root, "memory.db") }

// OrgMemoryDBPath is the user-level org-scoped memory database. An
// unavailable home directory yields an empty path so callers can disable the
// org store.
func OrgMemoryDBPath() string {
	home, err := UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return NamespacePath(home, "memory", "org.db")
}
