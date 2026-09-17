package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadRuntimeMCPConfig_EmptyWorkspaceRootFallsBackToGetwd pins
// loadRuntimeMCPConfig's own `workspaceRoot == ""` branch: when the caller
// supplies no workspace root, it must resolve one via os.Getwd() rather than
// resolving MCP servers against an empty/root path. We chdir into a
// temporary directory carrying its own trusted project MCP config and assert
// that config - not some other tree's - is the one that comes back, proving
// os.Getwd() actually drove the resolution.
func TestLoadRuntimeMCPConfig_EmptyWorkspaceRootFallsBackToGetwd(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MIVIA_CONFIG", "")
	t.Chdir(cwd)

	writeMCPConfig(t, filepath.Join(cwd, ".mivia", "mivia.toml"), `
[mcp]
enabled = true
[[mcp.servers]]
id = "repository"
transport = "stdio"
command = "/usr/bin/cwd-server"
`)

	cfg, warnings, err := loadRuntimeMCPConfig("")
	if err != nil {
		t.Fatalf("loadRuntimeMCPConfig(\"\") error = %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if len(cfg.Servers) != 1 || cfg.Servers[0].Command != "/usr/bin/cwd-server" {
		t.Fatalf("cfg.Servers = %#v, want the cwd's own project server (os.Getwd() fallback)", cfg.Servers)
	}
}

// TestLoadRuntimeMCPConfig_EmptyWorkspaceRootMatchesExplicitCwd asserts the
// os.Getwd() fallback yields exactly the same result as passing the current
// directory explicitly, pinning that the two are equivalent rather than
// merely "both succeed".
func TestLoadRuntimeMCPConfig_EmptyWorkspaceRootMatchesExplicitCwd(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MIVIA_CONFIG", "")
	t.Chdir(cwd)

	writeMCPConfig(t, filepath.Join(cwd, ".mivia", "mivia.toml"), `
[mcp]
enabled = true
[[mcp.servers]]
id = "repository"
transport = "stdio"
command = "/usr/bin/explicit-server"
`)

	viaEmpty, _, err := loadRuntimeMCPConfig("")
	if err != nil {
		t.Fatalf("loadRuntimeMCPConfig(\"\") error = %v", err)
	}
	explicitCwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	viaExplicit, _, err := loadRuntimeMCPConfig(explicitCwd)
	if err != nil {
		t.Fatalf("loadRuntimeMCPConfig(explicit cwd) error = %v", err)
	}
	if len(viaEmpty.Servers) != 1 || len(viaExplicit.Servers) != 1 || viaEmpty.Servers[0].Command != viaExplicit.Servers[0].Command {
		t.Fatalf("empty-workspaceRoot result %#v != explicit-cwd result %#v", viaEmpty, viaExplicit)
	}
}
