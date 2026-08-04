package definition

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func TestDiscoverWorkflows_NonExistentDirectory(t *testing.T) {
	// Use a temp dir that has no .mivia/workflows/ subdirectory.
	tmp := t.TempDir()
	result, err := DiscoverWorkflows(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected empty slice, got %d results", len(result))
	}
}

func TestDiscoverWorkflows_FindsTOMLFiles(t *testing.T) {
	tmp := t.TempDir()
	wfDir := workspace.NamespacePath(tmp, "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Write a valid minimal workflow.
	content := []byte(`
version = 1
name = "my-workflow"
description = "Test"
initial_step = "plan"

[[steps]]
id = "plan"
kind = "human_gate"

[[transitions]]
from = "plan"
to = "success"
match = { status = "approved" }
`)
	if err := os.WriteFile(filepath.Join(wfDir, "my-workflow.toml"), content, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	result, err := DiscoverWorkflows(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result))
	}
	if result[0].Name != "my-workflow" {
		t.Errorf("name = %q, want %q", result[0].Name, "my-workflow")
	}
	if filepath.Base(result[0].Path) != "my-workflow.toml" {
		t.Errorf("path basename = %q, want my-workflow.toml", filepath.Base(result[0].Path))
	}
	if string(result[0].Raw) != string(content) {
		t.Error("raw content mismatch")
	}
}

func TestDiscoverWorkflows_SkipsNonTOML(t *testing.T) {
	tmp := t.TempDir()
	wfDir := workspace.NamespacePath(tmp, "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Write a non-.toml file and a subdirectory.
	if err := os.WriteFile(filepath.Join(wfDir, "README.md"), []byte("# ignore me"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(wfDir, "subdir"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	result, err := DiscoverWorkflows(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected 0 results, got %d", len(result))
	}
}

func TestDiscoverWorkflows_SymlinkDirectoryRejected(t *testing.T) {
	tmp := t.TempDir()
	miviaDir := workspace.NamespacePath(tmp)
	if err := os.MkdirAll(miviaDir, 0o755); err != nil {
		t.Fatalf("mkdir mivia: %v", err)
	}

	// Create a real directory to point to.
	targetDir := filepath.Join(tmp, "outside-workflows")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}

	// Symlink workflows -> outside directory.
	link := filepath.Join(miviaDir, "workflows")
	if err := os.Symlink(targetDir, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	_, err := DiscoverWorkflows(tmp)
	if err == nil {
		t.Fatal("expected error for symlinked workflows directory, got nil")
	}
}

func TestDiscoverWorkflows_SymlinkFileRejected(t *testing.T) {
	tmp := t.TempDir()
	wfDir := workspace.NamespacePath(tmp, "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Create a regular file outside and symlink it in.
	target := filepath.Join(tmp, "outside.toml")
	if err := os.WriteFile(target, []byte("version = 1"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	link := filepath.Join(wfDir, "linked.toml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	_, err := DiscoverWorkflows(tmp)
	if err == nil {
		t.Fatal("expected error for symlinked workflow file, got nil")
	}
}
