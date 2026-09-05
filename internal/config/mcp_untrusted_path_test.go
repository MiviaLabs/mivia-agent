package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

const mcpBaseFixture = `
[provider]
name = "deepseek"

[providers.deepseek]
models = [{ name = "deepseek-v4-pro", context_window_tokens = 128000 }]
default_model = "deepseek-v4-pro"
`

const mcpTableFixture = mcpBaseFixture + `
[mcp]
enabled = true

[[mcp.servers]]
id = "local"
transport = "stdio"
command = "/bin/true"
`

// TestLoadRefusesAnMCPTableFromAnUntrustedConfigPath pins the fail-closed
// answer to a silent substitution.
//
// config.File decodes [mcp] from whatever file it loads, but MCP is resolved
// from two trusted paths only (the user config and <root>/.mivia/mivia.toml),
// and resolveLoaded then overwrites the decoded table with that result. So a
// config selected with --config or MIVIA_CONFIG had its [mcp] table parsed
// and discarded without a word: the operator's declared servers never
// started, while stdio servers from the trusted paths - which launch
// arbitrary local commands - started anyway, under a configuration the
// operator never named.
func TestLoadRefusesAnMCPTableFromAnUntrustedConfigPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	path := filepath.Join(t.TempDir(), "prod.toml")
	if err := os.WriteFile(path, []byte(mcpTableFixture), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load(LoadOptions{ConfigPath: path, WorkspaceRoot: t.TempDir()})
	if err == nil {
		t.Fatal("Load accepted an [mcp] table from an untrusted config path and silently discarded it")
	}
	if !strings.Contains(err.Error(), "[mcp] is only read from") {
		t.Fatalf("got %v, want an error naming the two paths that do carry [mcp]", err)
	}
}

// TestLoadAcceptsAnMCPTableFromAProjectConfig keeps the guard from refusing
// the workspace's own project config carrying [mcp].
func TestLoadAcceptsAnMCPTableFromAProjectConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	root := t.TempDir()
	dir := filepath.Join(root, workspace.Namespace)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "mivia.toml")
	if err := os.WriteFile(path, []byte(mcpTableFixture), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(LoadOptions{ConfigPath: path, WorkspaceRoot: root}); err != nil {
		t.Fatalf("Load refused a project config carrying [mcp] with matching WorkspaceRoot: %v", err)
	}
	if _, err := Load(LoadOptions{ConfigPath: path}); err != nil {
		t.Fatalf("Load refused a project config carrying [mcp] with inferred WorkspaceRoot: %v", err)
	}
}

// TestLoadRefusesAnMCPTableFromADifferentProjectConfig asserts that a project
// config from a different checkout than WorkspaceRoot fails closed.
func TestLoadRefusesAnMCPTableFromADifferentProjectConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	otherRoot := t.TempDir()
	otherDir := filepath.Join(otherRoot, workspace.Namespace)
	if err := os.MkdirAll(otherDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(otherDir, "mivia.toml")
	if err := os.WriteFile(path, []byte(mcpTableFixture), 0o600); err != nil {
		t.Fatal(err)
	}

	activeRoot := t.TempDir()
	_, err := Load(LoadOptions{ConfigPath: path, WorkspaceRoot: activeRoot})
	if err == nil {
		t.Fatal("Load accepted an [mcp] table from a different checkout's config and silently discarded it")
	}
	if !strings.Contains(err.Error(), "[mcp] is only read from") {
		t.Fatalf("got %v, want an error naming the two paths that do carry [mcp]", err)
	}
}

// TestRefuseUntrustedMCPTableDefaultsEmptyWorkspaceRootToDot covers the
// message-building fallback directly: every Load-level test passes a real
// WorkspaceRoot, so root's own `if root == "" { root = "." }` default (used
// only to name the trusted project-config path in the error text) was never
// reached.
func TestRefuseUntrustedMCPTableDefaultsEmptyWorkspaceRootToDot(t *testing.T) {
	file := File{MCP: MCPConfig{Enabled: true}}
	err := refuseUntrustedMCPTable(file, "/some/untrusted/prod.toml", "", true)
	if err == nil {
		t.Fatal("refuseUntrustedMCPTable accepted an untrusted path")
	}
	if !strings.Contains(err.Error(), filepath.Join(workspace.Namespace, "mivia.toml")) {
		t.Fatalf("error = %v, want it to name the project config path rooted at \".\"", err)
	}
}

// TestLoadIgnoresAnAbsentMCPTable keeps the guard scoped to configs that
// actually declare one: an ordinary --config file must still load.
func TestLoadIgnoresAnAbsentMCPTable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	path := filepath.Join(t.TempDir(), "prod.toml")
	if err := os.WriteFile(path, []byte(mcpBaseFixture), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(LoadOptions{ConfigPath: path, WorkspaceRoot: t.TempDir()}); err != nil {
		t.Fatalf("Load refused a config with no [mcp] table: %v", err)
	}
}
