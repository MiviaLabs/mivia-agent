package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadTrustedMCPConfigProjectServerReplacesUserServer(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MIVIA_CONFIG", "")
	writeMCPConfig(t, filepath.Join(home, ".mivia", "mivia.toml"), `
[mcp]
enabled = true
max_servers = 4
[[mcp.servers]]
id = "repository"
transport = "stdio"
command = "/usr/bin/user-server"
global = true
`)
	writeMCPConfig(t, filepath.Join(workspace, ".mivia", "mivia.toml"), `
[mcp]
enabled = true
[[mcp.servers]]
id = "repository"
transport = "stdio"
command = "/usr/bin/project-server"
global = false
`)

	got, warnings, err := LoadTrustedMCPConfig(workspace)
	if err != nil {
		t.Fatalf("LoadTrustedMCPConfig() error = %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if len(got.Servers) != 1 {
		t.Fatalf("servers = %#v, want one project replacement", got.Servers)
	}
	if got.Servers[0].Command != "/usr/bin/project-server" || got.Servers[0].Global {
		t.Fatalf("server = %#v, want complete project replacement", got.Servers[0])
	}
}

func TestMCPConfigDigestExcludesEnvironmentValues(t *testing.T) {
	base := MCPConfig{Enabled: true, Servers: []MCPServerConfig{{
		ID: "issues", Transport: "streamable_http", URL: "https://example.invalid/mcp",
		Headers: []MCPHeaderConfig{{Name: "Authorization", ValueEnv: "ISSUES_MCP_TOKEN"}},
	}}}
	digest, err := MCPConfigDigest(base)
	if err != nil {
		t.Fatalf("MCPConfigDigest() error = %v", err)
	}
	if !strings.HasPrefix(digest, "sha256:") {
		t.Fatalf("digest = %q, want sha256 digest", digest)
	}
}

func TestLoadTrustedMCPConfigRejectsDuplicateServerIDs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MIVIA_CONFIG", "")
	writeMCPConfig(t, filepath.Join(home, ".mivia", "mivia.toml"), `
[mcp]
enabled = true
[[mcp.servers]]
id = "duplicate"
transport = "stdio"
command = "/usr/bin/one"
[[mcp.servers]]
id = "duplicate"
transport = "stdio"
command = "/usr/bin/two"
`)
	if _, _, err := LoadTrustedMCPConfig(t.TempDir()); err == nil {
		t.Fatal("LoadTrustedMCPConfig() accepted duplicate server IDs")
	}
}

func TestLoadTrustedMCPConfigDefaultsAndBounds(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MIVIA_CONFIG", "")
	writeMCPConfig(t, filepath.Join(home, ".mivia", "mivia.toml"), `
[mcp]
enabled = true
`)
	got, _, err := LoadTrustedMCPConfig(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got.MaxServers == 0 || got.MaxToolsPerServer == 0 || got.MaxToolResultBytes == 0 {
		t.Fatalf("MCP defaults = %#v, want positive limits", got)
	}
	writeMCPConfig(t, filepath.Join(home, ".mivia", "mivia.toml"), `
[mcp]
enabled = true
max_servers = -1
`)
	if _, _, err := LoadTrustedMCPConfig(t.TempDir()); err == nil {
		t.Fatal("LoadTrustedMCPConfig() accepted a negative limit")
	}
}

func TestLoadTrustedMCPConfigRejectsUnknownMCPKeys(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MIVIA_CONFIG", "")
	writeMCPConfig(t, filepath.Join(home, ".mivia", "mivia.toml"), `
[mcp]
enabled = true
unsafe_secret = "value"
`)
	if _, _, err := LoadTrustedMCPConfig(t.TempDir()); err == nil {
		t.Fatal("LoadTrustedMCPConfig() accepted an unknown MCP key")
	}
}

func TestLoadTrustedMCPConfigRejectsInvalidSecretReferences(t *testing.T) {
	tests := []struct {
		name   string
		server string
	}{
		{
			name: "literal stdio environment value",
			server: `
[[mcp.servers]]
id = "repository"
transport = "stdio"
command = "/usr/bin/repository-server"
env = ["REPOSITORY_TOKEN=literal-secret"]
	`,
		},
		{
			name: "literal HTTP header value",
			server: `
[[mcp.servers]]
id = "issues"
transport = "streamable_http"
url = "https://example.invalid/mcp"
headers = [{ name = "Authorization", value_env = "literal-secret" }]
`,
		},
		{
			name: "transport owned header",
			server: `
[[mcp.servers]]
id = "issues"
transport = "streamable_http"
url = "https://example.invalid/mcp"
headers = [{ name = "Mcp-Session-Id", value_env = "SESSION_ID" }]
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("MIVIA_CONFIG", "")
			writeMCPConfig(t, filepath.Join(home, ".mivia", "mivia.toml"), "[mcp]\nenabled = true\n"+tt.server)
			if _, _, err := LoadTrustedMCPConfig(t.TempDir()); err == nil {
				t.Fatal("LoadTrustedMCPConfig() accepted invalid server secret reference")
			}
		})
	}
}

func TestLoadExposesEffectiveMCPConfig(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MIVIA_CONFIG", "")
	t.Chdir(workspace)
	writeMCPConfig(t, filepath.Join(home, ".mivia", "mivia.toml"), `
[mcp]
enabled = true
[[mcp.servers]]
id = "repository"
transport = "stdio"
command = "/usr/bin/user-server"
`)
	writeMCPConfig(t, filepath.Join(workspace, ".mivia", "mivia.toml"), `
[provider]
name = "deepseek"
[providers.deepseek]
default_model = "deepseek-v4-pro"
models = [{ name = "deepseek-v4-pro", context_window_tokens = 10000 }]
[mcp]
enabled = true
[[mcp.servers]]
id = "repository"
transport = "stdio"
command = "/usr/bin/project-server"
`)

	got, err := Load(LoadOptions{})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(got.MCP.Servers) != 1 || got.MCP.Servers[0].Command != "/usr/bin/project-server" {
		t.Fatalf("MCP = %#v, want effective project server", got.MCP)
	}
}

func TestLoadTrustedMCPConfigWarnsForPlaintextHTTP(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MIVIA_CONFIG", "")
	writeMCPConfig(t, filepath.Join(home, ".mivia", "mivia.toml"), `
[mcp]
enabled = true
[[mcp.servers]]
id = "plain"
transport = "streamable_http"
url = "http://127.0.0.1:8080/mcp"
`)
	_, warnings, err := LoadTrustedMCPConfig(t.TempDir())
	if err != nil || len(warnings) != 1 || !strings.Contains(warnings[0], "plain") {
		t.Fatalf("LoadTrustedMCPConfig() warnings = %v, error = %v", warnings, err)
	}
}

func TestMergeMCPConfigDoesNotWidenUserLimits(t *testing.T) {
	userLimit, projectLimit := 10, 100
	user := mcpConfigInput{StartupTimeoutSeconds: &userLimit, MaxServers: &userLimit, MaxToolsPerServer: &userLimit, MaxToolSchemaBytes: &userLimit, MaxToolDescriptionBytes: &userLimit, MaxToolResultBytes: &userLimit}
	project := mcpConfigInput{StartupTimeoutSeconds: &projectLimit, MaxServers: &projectLimit, MaxToolsPerServer: &projectLimit, MaxToolSchemaBytes: &projectLimit, MaxToolDescriptionBytes: &projectLimit, MaxToolResultBytes: &projectLimit}
	merged := mergeMCPConfig(user, project)
	for _, value := range []*int{merged.StartupTimeoutSeconds, merged.MaxServers, merged.MaxToolsPerServer, merged.MaxToolSchemaBytes, merged.MaxToolDescriptionBytes, merged.MaxToolResultBytes} {
		if value == nil || *value != 10 {
			t.Fatalf("merged limit = %v, want 10", value)
		}
	}
}

func TestLoadUsesExplicitWorkspaceRootForMCPConfig(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	otherDirectory := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MIVIA_CONFIG", "")
	t.Chdir(otherDirectory)
	writeMCPConfig(t, filepath.Join(home, ".mivia", "mivia.toml"), `
[provider]
name = "deepseek"
[providers.deepseek]
default_model = "deepseek-v4-pro"
models = [{ name = "deepseek-v4-pro", context_window_tokens = 10000 }]
[mcp]
enabled = true
[[mcp.servers]]
id = "repository"
transport = "stdio"
command = "/usr/bin/user-server"
`)
	writeMCPConfig(t, filepath.Join(workspace, ".mivia", "mivia.toml"), `
[mcp]
enabled = true
[[mcp.servers]]
id = "repository"
transport = "stdio"
command = "/usr/bin/project-server"
`)

	got, err := Load(LoadOptions{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(got.MCP.Servers) != 1 || got.MCP.Servers[0].Command != "/usr/bin/project-server" {
		t.Fatalf("MCP = %#v, want server from explicit workspace", got.MCP)
	}
}

func writeMCPConfig(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}
