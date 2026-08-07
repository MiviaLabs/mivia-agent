package definition

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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

func TestDiscoverWorkflows_ReadsCompleteFile(t *testing.T) {
	// Regression: io.ReadAll errors must propagate; a successful read must
	// return the full on-disk bytes in Raw.
	tmp := t.TempDir()
	wfDir := workspace.NamespacePath(tmp, "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	content := []byte("version = 1\nname = \"complete\"\n")
	if err := os.WriteFile(filepath.Join(wfDir, "complete.toml"), content, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	result, err := DiscoverWorkflows(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result))
	}
	if len(result[0].Raw) != len(content) {
		t.Errorf("raw length = %d, want %d", len(result[0].Raw), len(content))
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

// --- openWorkflowsRoot error branches ---

func TestDiscoverWorkflows_MissingWorkflowsDirInsideMivia(t *testing.T) {
	// .mivia exists but workflows does not: parent.Lstat fails and the
	// IsNotExist error is treated as "no workflows".
	tmp := t.TempDir()
	miviaDir := workspace.NamespacePath(tmp)
	if err := os.MkdirAll(miviaDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	result, err := DiscoverWorkflows(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected empty slice, got %d results", len(result))
	}
}

func TestDiscoverWorkflows_WorkflowsPathIsFile(t *testing.T) {
	tmp := t.TempDir()
	wfPath := workspace.NamespacePath(tmp, "workflows")
	if err := os.MkdirAll(filepath.Dir(wfPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(wfPath, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := DiscoverWorkflows(tmp)
	if err == nil {
		t.Fatal("expected error when workflows path is a regular file")
	}
	if !strings.Contains(err.Error(), "not a real directory") {
		t.Errorf("error %q should mention not a real directory", err.Error())
	}
}

func TestDiscoverWorkflows_UnreadableWorkflowsDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission checks are bypassed when running as root")
	}
	tmp := t.TempDir()
	wfDir := workspace.NamespacePath(tmp, "workflows")
	if err := os.MkdirAll(filepath.Dir(wfDir), 0o755); err != nil {
		t.Fatalf("mkdir .mivia: %v", err)
	}
	if err := os.Mkdir(wfDir, 0o000); err != nil {
		t.Fatalf("mkdir workflows: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(wfDir, 0o700) })
	if _, err := DiscoverWorkflows(tmp); err == nil {
		t.Fatal("expected error for unreadable workflows directory")
	}
}

func TestDiscoverWorkflows_NonSearchableWorkflowsDir(t *testing.T) {
	// A read-only (0o400) workflows dir opens as a root but its entries cannot
	// be listed, so discovery surfaces the ReadDir error.
	if os.Geteuid() == 0 {
		t.Skip("permission checks are bypassed when running as root")
	}
	tmp := t.TempDir()
	wfDir := workspace.NamespacePath(tmp, "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wfDir, "a.toml"), []byte("version = 1"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chmod(wfDir, 0o400); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(wfDir, 0o700) })
	if _, err := DiscoverWorkflows(tmp); err == nil {
		t.Fatal("expected an error for a non-listable workflows directory")
	}
}

// --- readRegularWorkflowFile error branches ---

// TestReadRegularWorkflowFileMissingFile covers the Lstat error branch of
// readRegularWorkflowFile directly: a name that vanished between discovery's
// ReadDir and the per-file read.
func TestReadRegularWorkflowFileMissingFile(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, err := readRegularWorkflowFile(root, "missing.toml"); err == nil {
		t.Fatal("expected an error for a missing workflow file")
	}
}

func TestDiscoverWorkflows_NonRegularFile(t *testing.T) {
	tmp := t.TempDir()
	wfDir := workspace.NamespacePath(tmp, "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := syscall.Mkfifo(filepath.Join(wfDir, "a-pipe.toml"), 0o644); err != nil {
		t.Skipf("mkfifo not supported: %v", err)
	}
	_, err := DiscoverWorkflows(tmp)
	if err == nil {
		t.Fatal("expected error for non-regular workflow file")
	}
	if !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("error %q should mention not a regular file", err.Error())
	}
}

func TestDiscoverWorkflows_HardlinkedFile(t *testing.T) {
	tmp := t.TempDir()
	wfDir := workspace.NamespacePath(tmp, "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	a := filepath.Join(wfDir, "a.toml")
	if err := os.WriteFile(a, []byte("version = 1"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Link(a, filepath.Join(wfDir, "b.toml")); err != nil {
		t.Skipf("hard links not supported: %v", err)
	}
	_, err := DiscoverWorkflows(tmp)
	if err == nil {
		t.Fatal("expected error for workflow file with multiple links")
	}
	if !strings.Contains(err.Error(), "multiple links") {
		t.Errorf("error %q should mention multiple links", err.Error())
	}
}

func TestDiscoverWorkflows_UnreadableWorkflowFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission checks are bypassed when running as root")
	}
	tmp := t.TempDir()
	wfDir := workspace.NamespacePath(tmp, "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	file := filepath.Join(wfDir, "a.toml")
	if err := os.WriteFile(file, []byte("version = 1"), 0o000); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(file, 0o600) })
	if _, err := DiscoverWorkflows(tmp); err == nil {
		t.Fatal("expected error for unreadable workflow file")
	}
}

func TestDiscoverWorkflows_OversizedWorkflowFile(t *testing.T) {
	tmp := t.TempDir()
	wfDir := workspace.NamespacePath(tmp, "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	big := make([]byte, MaxWorkflowFileBytes+100)
	for i := range big {
		big[i] = 'a'
	}
	if err := os.WriteFile(filepath.Join(wfDir, "big.toml"), big, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := DiscoverWorkflows(tmp)
	if err == nil {
		t.Fatal("expected error for oversized workflow file")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error %q should mention exceeds", err.Error())
	}
}

// --- fileNlink type-assertion fallback ---

type nonSysFileInfo struct {
	fs.FileInfo
}

func (nonSysFileInfo) Sys() any { return nil }

func TestFileNlink_NonStatType(t *testing.T) {
	// A FileInfo whose Sys() is not *syscall.Stat_t makes fileNlink fall back
	// to 0 (meaning "unknown"), which disables the nlink checks.
	if got := fileNlink(nonSysFileInfo{}); got != 0 {
		t.Errorf("fileNlink = %d, want 0", got)
	}
}
