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
	// <workspace>/.agents/agents/.
	AgentSourceWorkspace AgentSource = "workspace"
	// AgentSourceBuiltIn is a compiled definition shipped inside the mivia
	// binary. Built-ins are product content, not workspace input: they load
	// regardless of the load_workspace_config gate and always follow the
	// session provider binding.
	AgentSourceBuiltIn AgentSource = "builtin"
)

const maxAgentFileBytes = 256 << 10

// AgentsGlobal is the trusted [agents] section of ~/.mivia/mivia.toml.
// Only the user file owns these values; workspace [agents] is ignored.
type AgentsGlobal struct {
	// LoadWorkspaceConfig enables workspace agent files and related
	// workspace-controlled prompt/skill surfaces. Default true.
	LoadWorkspaceConfig bool
	// AllowWorkspaceAgentProviders, when true, lets a workspace-sourced agent
	// definition select a (provider, model) binding. This is an operator opt-in
	// that accepts a real credential-routing risk: a checked-out repository
	// could route the operator's prompts, tool results, and file contents to
	// another vendor's endpoint authenticated with the operator's own
	// credentials. When false (the default), a workspace agent's
	// provider/model selection is ignored at resolve time (credential-routing
	// protection) and the agent inherits the session provider. Only the user
	// [agents] section may set this; workspace [agents] is never authoritative.
	AllowWorkspaceAgentProviders bool
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
	Name        *string
	Description *string
	Inherits    *string
	Tools       *[]string
	// AllowEmptyTools permits an explicitly declared empty tools list. It is
	// valid only for a standalone agent with tools = [].
	AllowEmptyTools *bool
	ToolsAdd        *[]string
	ToolsRemove     *[]string
	DisallowedTools *[]string
	// ToolsCore overrides [tools] core for this agent (plan tools/05).
	// nil = inherit (parent's decision, else the global [tools] core).
	ToolsCore *[]string
	// Skills is the skill invocation allowlist for this agent (plan 06).
	// nil = omit (root: all trusted skills; inherited: parent decision);
	// non-nil empty = none; non-nil with names = those skills only.
	Skills *[]string
	// MCPServers is the MCP server allowlist. nil inherits the root default or
	// parent list. An explicit empty list denies every MCP server.
	MCPServers *[]string
	// Provider is the built-in provider name owning Model. It is normalized to
	// lower case at parse time and may only be set together with Model: a
	// provider alone would silently pair a foreign endpoint with the session's
	// model name. Provider selection on a workspace definition is ignored
	// unless the operator enables AllowWorkspaceAgentProviders (credential-
	// routing protection); user definitions always honor it.
	Provider *string
	Model    *string
	MaxTurns *int
	// TimeoutSeconds and MaxTokens are per-agent resource ceilings, deliberately
	// independent of MaxTurns: max_turns = 0 means unlimited iterations, not
	// unlimited wall-clock time or provider spend. nil = inherit the session's.
	TimeoutSeconds *int
	MaxTokens      *int
	SystemPrompt   *string
	// OutputSchema is an optional JSON Schema for the agent's final reply
	// (plan tools/02). Pointer preserves omit vs empty for inheritance.
	OutputSchema *map[string]any
	// InputSchema optionally validates task input at admission.
	InputSchema *map[string]any
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
	home, err := workspace.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return workspace.NamespacePath(home, "agents")
}

// WorkspaceAgentsDir returns <root>/.agents/agents without checking the filesystem.
func WorkspaceAgentsDir(root string) string {
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	return workspace.AgentsDir(root)
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

// RootAgentName is the compiled identity of the main (root) session agent.
// The root surface is never a registry member, so this name must stay
// reserved: file-backed definitions may not use it, and selecting it (flag,
// /agent, picker) restores the root surface. Constraint for future built-ins:
// no compiled built-in may carry this name either - the reservation in
// checkNameCollisions rejects it for every input, by design.
const RootAgentName = "general-orchestrator"

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

// DiscoverAgentFilesTolerant loads user agent files strictly and workspace
// agent files tolerantly: a single malformed, symlinked, hardlinked, or
// oversized file under <ws>/.mivia/agents/ must never abort chat startup
// (INV-AG-34). The trusted user boundary stays fail-closed - any problem in
// ~/.mivia/agents/ is still a hard error.
//
// loadWorkspace is retained for call-site compatibility and is ignored.
// Same-directory home/workspace is treated as user only, exactly like
// DiscoverAgentFiles; it never routes through the tolerant workspace path.
// Workspace files that share a name with a user agent are ignored with the
// same shadow warning as DiscoverAgentFiles. Every other non-loaded workspace
// file becomes a class-only skip warning; raw parser text is never forwarded
// (see tolerantSkipWarnings and the agents_diagnostics.go contract).
func DiscoverAgentFilesTolerant(workspaceRoot string, loadWorkspace bool) ([]LoadedAgentFile, []string, error) {
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

	wsFiles, wsRows, err := loadAgentDirReport(wsDir, AgentSourceWorkspace)
	if err != nil {
		// loadAgentDirReport is tolerant by construction; keep the invariant
		// that the workspace side can never abort startup regardless.
		return mapAgentValues(byName), append(warnings, tolerantSkipWarnings(wsRows)...), nil
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
	warnings = append(warnings, tolerantSkipWarnings(wsRows)...)
	return mapAgentValues(byName), warnings, nil
}

// tolerantSkipWarnings reduces non-loaded workspace diagnostic rows to
// class-only warning strings. Raw parser text is intentionally dropped, per
// the agents_diagnostics.go contract.
func tolerantSkipWarnings(rows []AgentFileDiagnostic) []string {
	var out []string
	for _, row := range rows {
		if row.State == AgentFileLoaded {
			continue
		}
		out = append(out, fmt.Sprintf("skipped workspace agent %q (%s)", row.Name, row.State))
	}
	return out
}
