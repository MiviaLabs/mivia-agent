package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func newDeleteTestTool(t *testing.T) (*deleteFileTool, string) {
	t.Helper()
	root, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatalf("workspace.Open: %v", err)
	}
	return &deleteFileTool{ws: root}, root.Abs
}

func callDelete(t *testing.T, tool *deleteFileTool, path string) (string, error) {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"path": path})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return tool.Execute(context.Background(), raw)
}

func TestDeleteFileRemovesRegularFile(t *testing.T) {
	tool, root := newDeleteTestTool(t)
	target := filepath.Join(root, "gone.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	out, err := callDelete(t, tool, "gone.txt")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !strings.Contains(out, "gone.txt") {
		t.Fatalf("result %q does not name the file", out)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("file still exists after delete (stat err=%v)", err)
	}
}

func TestDeleteFileRefusesDirectory(t *testing.T) {
	tool, root := newDeleteTestTool(t)
	dir := filepath.Join(root, "sub")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	_, err := callDelete(t, tool, "sub")
	if err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("expected directory refusal, got %v", err)
	}
	if _, statErr := os.Stat(dir); statErr != nil {
		t.Fatalf("directory was removed despite refusal: %v", statErr)
	}
}

func TestDeleteFileRefusesMissingPath(t *testing.T) {
	tool, _ := newDeleteTestTool(t)
	_, err := callDelete(t, tool, "nope.txt")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestDeleteFileRefusesEmptyPath(t *testing.T) {
	tool, _ := newDeleteTestTool(t)
	_, err := callDelete(t, tool, "")
	if err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestDeleteFileRefusesWorkspaceEscape(t *testing.T) {
	tool, _ := newDeleteTestTool(t)
	_, err := callDelete(t, tool, "../outside.txt")
	if err == nil {
		t.Fatal("expected escape refusal")
	}
}

func TestDeleteFileRefusesWorkspaceRoot(t *testing.T) {
	tool, _ := newDeleteTestTool(t)
	_, err := callDelete(t, tool, ".")
	if err == nil || !strings.Contains(err.Error(), "workspace root") {
		t.Fatalf("expected root refusal, got %v", err)
	}
}

func TestDeleteFileRefusesSymlink(t *testing.T) {
	tool, root := newDeleteTestTool(t)
	outside := filepath.Join(filepath.Dir(root), "outside-target.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	defer os.Remove(outside)
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	_, err := callDelete(t, tool, "link.txt")
	if err == nil {
		t.Fatal("expected symlink escape refusal")
	}
	if _, statErr := os.Stat(outside); statErr != nil {
		t.Fatalf("symlink target was removed: %v", statErr)
	}
}

// TestDeleteFileFollowsInWorkspaceSymlink pins the Resolve contract: a link
// whose target stays inside the workspace resolves to the target, and delete
// removes the target, never the link itself.
func TestDeleteFileFollowsInWorkspaceSymlink(t *testing.T) {
	tool, root := newDeleteTestTool(t)
	target := filepath.Join(root, "target.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := callDelete(t, tool, "link.txt"); err != nil {
		t.Fatalf("delete_file on in-workspace symlink: %v", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("symlink target still exists (stat err %v)", statErr)
	}
}
