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
// the shape it exists to protect. The test is the project-config SHAPE, not
// equality with this process's resolved workspace root: every shipped-config
// test loads .mivia/mivia.toml with an unrelated (or empty) root.
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

	if _, err := Load(LoadOptions{ConfigPath: path, WorkspaceRoot: t.TempDir()}); err != nil {
		t.Fatalf("Load refused a project-shaped config carrying [mcp]: %v", err)
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
