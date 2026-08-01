package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// AgentSource identifies the trust origin of a loaded agent definition file.
type AgentSource string

const (
	// AgentSourceUser is a trusted definition under ~/.mivia/agents/.
	AgentSourceUser AgentSource = "user"
	// AgentSourceWorkspace is an untrusted, gated definition under
	// <workspace>/.mivia/agents/.
	AgentSourceWorkspace AgentSource = "workspace"
)

const maxAgentFileBytes = 256 << 10

// AgentsGlobal is the trusted [agents] section of ~/.mivia/mivia.toml.
// Only the user file owns these values; workspace [agents] is ignored.
type AgentsGlobal struct {
	// LoadWorkspaceConfig enables workspace agent files and related
	// workspace-controlled prompt/skill surfaces. Default true.
	LoadWorkspaceConfig bool
	// RequireExplicitTools, when true, forces authored agents that omit tools
	// to resolve an empty allowlist (deny-by-default). Default false.
	RequireExplicitTools bool
	// FailOnEmptyToolset refuses an agent whose effective toolset is empty.
	// Default true so a typo cannot publish a no-tool agent silently.
	FailOnEmptyToolset bool
	// MandatoryToolDenylistAdditions are operator additions on top of the
	// compiled mandatory denylist. Config may only add; never remove baseline.
	MandatoryToolDenylistAdditions []string
	// Warnings are non-fatal diagnostics (e.g. workspace [agents] ignored).
	Warnings []string
	// Path is the user config file that supplied these values (may be empty).
	Path string
}

// AgentFileSpec is one presence-preserving agent TOML definition.
// Pointer fields distinguish omitted keys from empty values.
type AgentFileSpec struct {
	Name            *string
	Description     *string
	Inherits        *string
	Tools           *[]string
	ToolsAdd        *[]string
	ToolsRemove     *[]string
	DisallowedTools *[]string
	// Skills is the skill invocation allowlist for this agent (plan 06).
	// nil = omit (root: all trusted skills; inherited: parent decision);
	// non-nil empty = none; non-nil with names = those skills only.
	Skills *[]string
	// Provider is the built-in provider name owning Model. It is normalized to
	// lower case at parse time and may only be set together with Model: a
	// provider alone would silently pair a foreign endpoint with the session's
	// model name. Never set by a workspace definition (see internal/agents).
	Provider     *string
	Model        *string
	MaxTurns     *int
	SystemPrompt *string
}

// LoadedAgentFile is one safely-read agent definition with provenance.
type LoadedAgentFile struct {
	// Name is the canonical agent name from the filename (without .toml).
	Name   string
	Source AgentSource
	Path   string
	Spec   AgentFileSpec
}

// UserAgentsDir returns ~/.mivia/agents without checking the filesystem.
func UserAgentsDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return workspace.NamespacePath(home, "agents")
}

// WorkspaceAgentsDir returns <root>/.mivia/agents without checking the filesystem.
func WorkspaceAgentsDir(root string) string {
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	return workspace.NamespacePath(root, "agents")
}

// LoadAgentsGlobal reads the trusted user config for [agents] gate and
// guardrails. Workspace [agents] values are never authoritative; when a
// workspace config path is supplied and contains [agents], a warning is added.
//
// The user file is always UserConfigPath(). Missing user config yields defaults
// (gate on, fail_on_empty_toolset true).
func LoadAgentsGlobal(workspaceRoot string) (AgentsGlobal, error) {
	g := AgentsGlobal{
		LoadWorkspaceConfig: true,
		FailOnEmptyToolset:  true,
		Path:                UserConfigPath(),
	}
	if g.Path != "" {
		user, err := readAgentsSection(g.Path)
		if err != nil && !os.IsNotExist(err) {
			return AgentsGlobal{}, err
		}
		if err == nil {
			applyAgentsSection(&g, user)
		}
	}

	wsRoot := workspaceRoot
	if strings.TrimSpace(wsRoot) == "" {
		wsRoot = "."
	}
	wsConfig := workspace.NamespacePath(wsRoot, "mivia.toml")
	same, err := sameResolvedDir(filepath.Dir(g.Path), filepath.Dir(wsConfig))
	if err == nil && same {
		// One directory: keep the user interpretation only.
		return g, nil
	}
	if g.Path != "" && sameFilePath(g.Path, wsConfig) {
		return g, nil
	}

	// Workspace [agents] is never authoritative - warn when present.
	if data, err := os.ReadFile(wsConfig); err == nil {
		if hasAgentsTable(data) {
			g.Warnings = append(g.Warnings,
				"ignoring workspace [agents]; gate and guardrails remain owned by trusted user config")
		}
	}
	return g, nil
}

// DefaultAgentName is the root-session agent selected when --agent is omitted
// and a definition with this name is available.
const DefaultAgentName = "mivia"

// DiscoverAgentFiles loads user agent files and workspace agent files.
//
// Project agent definitions under <ws>/.mivia/agents/ always load when present:
// they replace the former ungated .mivia/agent-prompt.md surface. The user
// load_workspace_config gate still controls workspace mivia.toml system prompts
// and project skill handlers at the CLI layer - not agent file discovery.
//
// loadWorkspace is retained for call-site compatibility and is ignored.
// Same-directory home/workspace is treated as user only. Workspace files that
// share a name with a user agent are ignored with a warning. Fail-closed on
// symlinks, non-regular files, hardlink ambiguity, path escapes, and
// replacement races.
func DiscoverAgentFiles(workspaceRoot string, loadWorkspace bool) ([]LoadedAgentFile, []string, error) {
	_ = loadWorkspace
	var warnings []string
	byName := make(map[string]LoadedAgentFile)

	userDir := UserAgentsDir()
	userFiles, err := loadAgentDir(userDir, AgentSourceUser)
	if err != nil {
		return nil, nil, err
	}
	for _, f := range userFiles {
		byName[f.Name] = f
	}

	wsRoot := workspaceRoot
	if strings.TrimSpace(wsRoot) == "" {
		wsRoot = "."
	}
	wsDir := WorkspaceAgentsDir(wsRoot)

	same, err := sameResolvedDir(userDir, wsDir)
	if err == nil && same {
		// Trusted reading only; never reinterpret user files as workspace.
		return mapAgentValues(byName), warnings, nil
	}

	wsFiles, err := loadAgentDir(wsDir, AgentSourceWorkspace)
	if err != nil {
		return nil, nil, err
	}
	for _, f := range wsFiles {
		if _, ok := byName[f.Name]; ok {
			warnings = append(warnings, fmt.Sprintf(
				"workspace agent %q shadowed by user agent",
				f.Name))
			continue
		}
		byName[f.Name] = f
	}
	return mapAgentValues(byName), warnings, nil
}

func mapAgentValues(m map[string]LoadedAgentFile) []LoadedAgentFile {
	out := make([]LoadedAgentFile, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}
