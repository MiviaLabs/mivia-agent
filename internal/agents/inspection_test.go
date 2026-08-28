package agents

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// TestReplaceInspectionRow verifies that a diagnostic row is replaced in place
// when name+path match, and appended otherwise. Inspect uses this to promote a
// resolvable-but-failed file to the malformed class without losing independent
// rows for unrelated files.
func TestReplaceInspectionRow(t *testing.T) {
	loaded := config.AgentFileDiagnostic{
		Name: "a", Source: config.AgentSourceWorkspace, Path: "/ws/a.toml", State: config.AgentFileLoaded,
	}
	malformed := config.AgentFileDiagnostic{
		Name: "a", Source: config.AgentSourceWorkspace, Path: "/ws/a.toml", State: config.AgentFileMalformed,
	}
	other := config.AgentFileDiagnostic{
		Name: "b", Source: config.AgentSourceWorkspace, Path: "/ws/b.toml", State: config.AgentFileLoaded,
	}

	tests := []struct {
		name        string
		rows        []config.AgentFileDiagnostic
		replacement config.AgentFileDiagnostic
		wantLen     int
		wantState   map[string]config.AgentFileState // key: path
	}{
		{
			name:        "replaces existing row with matching name and path",
			rows:        []config.AgentFileDiagnostic{loaded, other},
			replacement: malformed,
			wantLen:     2,
			wantState:   map[string]config.AgentFileState{"/ws/a.toml": config.AgentFileMalformed, "/ws/b.toml": config.AgentFileLoaded},
		},
		{
			name:        "same name different path appends",
			rows:        []config.AgentFileDiagnostic{loaded},
			replacement: config.AgentFileDiagnostic{Name: "a", Source: config.AgentSourceUser, Path: "/user/a.toml", State: config.AgentFileMalformed},
			wantLen:     2,
			wantState:   map[string]config.AgentFileState{"/ws/a.toml": config.AgentFileLoaded, "/user/a.toml": config.AgentFileMalformed},
		},
		{
			name:        "different name appends",
			rows:        []config.AgentFileDiagnostic{loaded},
			replacement: config.AgentFileDiagnostic{Name: "c", Source: config.AgentSourceWorkspace, Path: "/ws/c.toml", State: config.AgentFileMalformed},
			wantLen:     2,
			wantState:   map[string]config.AgentFileState{"/ws/a.toml": config.AgentFileLoaded, "/ws/c.toml": config.AgentFileMalformed},
		},
		{
			name:        "empty rows appends",
			rows:        nil,
			replacement: malformed,
			wantLen:     1,
			wantState:   map[string]config.AgentFileState{"/ws/a.toml": config.AgentFileMalformed},
		},
		{
			name:        "identical row: only first occurrence replaced",
			rows:        []config.AgentFileDiagnostic{loaded, loaded},
			replacement: malformed,
			wantLen:     2,
			wantState:   nil, // can't express "first replaced, second unchanged" in map
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rows := append([]config.AgentFileDiagnostic(nil), tc.rows...)
			replaceInspectionRow(&rows, tc.replacement)
			if len(rows) != tc.wantLen {
				t.Fatalf("len(rows) = %d, want %d: %v", len(rows), tc.wantLen, rows)
			}
			for path, state := range tc.wantState {
				found := false
				for _, row := range rows {
					if row.Path == path {
						found = true
						if row.State != state {
							t.Errorf("row %q state = %q, want %q", path, row.State, state)
						}
					}
				}
				if !found {
					t.Errorf("row %q not present in %v", path, rows)
				}
			}
		})
	}
}

// writeWorkspaceMCPConfig writes <ws>/.mivia/mivia.toml with the given body.
func writeWorkspaceMCPConfig(t *testing.T, ws, body string) {
	t.Helper()
	path := workspace.NamespacePath(ws, "mivia.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestInspectResolvesAgentWithMCPServers locks the fix for
// agents-inspect-mcp-config-omitted: Inspect must load the trusted MCP
// configuration (as LoadAndResolveOpts does) so an agent with an explicit
// mcp_servers list resolves against real server definitions instead of the
// zero-value MCPConfig. Before the fix the zero-value config made every
// mcp_servers reference fail as unknown-or-disabled and Inspect promoted the
// valid agent to malformed, hiding it from CLI/doctor output and automation.
func TestInspectResolvesAgentWithMCPServers(t *testing.T) {
	home, ws := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	writeWorkspaceMCPConfig(t, ws, "[mcp]\nenabled = true\n\n[[mcp.servers]]\nid = \"repo\"\ntransport = \"stdio\"\ncommand = \"/bin/true\"\nglobal = true\n")
	writeAgent(t, config.WorkspaceAgentsDir(ws), "researcher.toml", "name = \"researcher\"\ndescription = \"ok\"\ntools = [\"read_file\"]\nmcp_servers = [\"repo\"]\n")

	report, err := Inspect(ws, LoadResolveOptions{})
	if err != nil {
		t.Fatalf("inspect error = %v", err)
	}
	agent, ok := report.Registry.Get("researcher")
	if !ok {
		t.Fatalf("agent with mcp_servers missing from registry: %#v", report.Registry.Names())
	}
	if !slices.Equal(agent.EffectiveMCPServers, []string{"repo"}) {
		t.Fatalf("EffectiveMCPServers = %v, want [repo]", agent.EffectiveMCPServers)
	}
	if got := report.DiagnosticSummary(); got != "none" {
		t.Fatalf("DiagnosticSummary = %q, want \"none\"", got)
	}
}

// TestInspectStillRejectsUnknownMCPServer keeps MCP validation intact in
// Inspect: an agent referencing a server that is not defined in the trusted
// config must still resolve as malformed.
func TestInspectStillRejectsUnknownMCPServer(t *testing.T) {
	home, ws := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	writeWorkspaceMCPConfig(t, ws, "[mcp]\nenabled = true\n\n[[mcp.servers]]\nid = \"repo\"\ntransport = \"stdio\"\ncommand = \"/bin/true\"\nglobal = true\n")
	writeAgent(t, config.WorkspaceAgentsDir(ws), "broken.toml", "name = \"broken\"\ndescription = \"b\"\ntools = [\"read_file\"]\nmcp_servers = [\"missing\"]\n")

	report, err := Inspect(ws, LoadResolveOptions{})
	if err != nil {
		t.Fatalf("inspect error = %v", err)
	}
	if _, ok := report.Registry.Get("broken"); ok {
		t.Fatal("agent referencing an unknown MCP server must not be selectable")
	}
	// The compiled built-in still loads beside the malformed file.
	if report.Registry.Len() != 1 || report.DiagnosticSummary() != "1 malformed" {
		t.Fatalf("registry=%v diagnostics=%q", report.Registry.Names(), report.DiagnosticSummary())
	}
	if _, ok := report.Registry.Get(BuiltInGeneralPurposeName); !ok {
		t.Fatalf("built-in missing from inspection registry: %v", report.Registry.Names())
	}
}

// TestInspectPropagatesMCPConfigError mirrors LoadAndResolveOpts: an invalid
// [mcp] table in the workspace config must make Inspect return an error
// instead of silently resolving against a zero-value MCPConfig.
func TestInspectPropagatesMCPConfigError(t *testing.T) {
	home, ws := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	writeWorkspaceMCPConfig(t, ws, "[mcp]\nenabled = true\nunknown_key = true\n")
	writeAgent(t, config.WorkspaceAgentsDir(ws), "researcher.toml", "name = \"researcher\"\ndescription = \"ok\"\ntools = [\"read_file\"]\n")

	report, err := Inspect(ws, LoadResolveOptions{})
	if err == nil {
		t.Fatal("inspect must propagate the MCP config error")
	}
	if report.Registry != nil {
		t.Fatal("error path must not publish a registry")
	}
}
