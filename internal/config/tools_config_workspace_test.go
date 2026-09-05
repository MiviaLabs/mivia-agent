package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func TestWorkspaceToolsConfigNoFileReturnsFoundFalse(t *testing.T) {
	tc, found, err := WorkspaceToolsConfig(t.TempDir())
	if err != nil {
		t.Fatalf("WorkspaceToolsConfig with no workspace config file: %v", err)
	}
	if found {
		t.Fatal("found = true for a workspace with no mivia.toml")
	}
	if tc.DiagnosticsCommands != nil || tc.WritePathBlocklist != nil {
		t.Fatalf("tc = %+v, want the zero value when not found", tc)
	}
}

func TestWorkspaceToolsConfigPropagatesNonNotExistReadError(t *testing.T) {
	root := t.TempDir()
	path := workspace.NamespacePath(root, "mivia.toml")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := WorkspaceToolsConfig(root); err == nil {
		t.Fatal("WorkspaceToolsConfig accepted a mivia.toml path that is actually a directory")
	}
}

func TestWorkspaceToolsConfigPropagatesDecodeError(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceToolsConfig(t, root, "not valid toml [[[")
	if _, _, err := WorkspaceToolsConfig(root); err == nil {
		t.Fatal("WorkspaceToolsConfig accepted malformed TOML")
	}
}

func TestWorkspaceToolsConfigPropagatesValidationError(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceToolsConfig(t, root, "[tools]\nwrite_path_blocklist = [\"/etc\"]\n")
	_, _, err := WorkspaceToolsConfig(root)
	if err == nil {
		t.Fatal("WorkspaceToolsConfig accepted an absolute write_path_blocklist entry")
	}
	if !strings.Contains(err.Error(), "write_path_blocklist") {
		t.Fatalf("error = %v, want it to name the failing key", err)
	}
}

func writeWorkspaceToolsConfig(t *testing.T, root, content string) {
	t.Helper()
	path := workspace.NamespacePath(root, "mivia.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
