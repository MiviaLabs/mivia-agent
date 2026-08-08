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

func writeMCPConfig(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}
