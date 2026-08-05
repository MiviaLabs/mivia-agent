package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

func TestMarkerCoverageInjectedPublishFailures(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	originalWrite := writeWorktreeMarkerTemp
	originalClose := closeWorktreeMarkerTemp
	originalRename := renameWorktreeMarker
	writeWorktreeMarkerTemp = func(*os.File, []byte) (int, error) {
		return 0, errors.New("write failure")
	}
	if err := writeWorktreeMarker(repo, instance); err == nil || !strings.Contains(err.Error(), "write worktree marker") {
		t.Fatalf("injected marker write error = %v", err)
	}
	writeWorktreeMarkerTemp = originalWrite
	closeWorktreeMarkerTemp = func(file *os.File) error {
		_ = file.Close()
		return errors.New("close failure")
	}
	if err := writeWorktreeMarker(repo, instance); err == nil || !strings.Contains(err.Error(), "close worktree marker") {
		t.Fatalf("injected marker close error = %v", err)
	}
	closeWorktreeMarkerTemp = originalClose
	renameWorktreeMarker = func(string, string) error { return errors.New("rename failure") }
	t.Cleanup(func() {
		writeWorktreeMarkerTemp = originalWrite
		closeWorktreeMarkerTemp = originalClose
		renameWorktreeMarker = originalRename
	})
	if err := writeWorktreeMarker(repo, instance); err == nil || !strings.Contains(err.Error(), "publish worktree marker") {
		t.Fatalf("injected marker publish error = %v", err)
	}
}

func TestMarkerCoverageInjectedOpenFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".mivia"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(worktreeMarkerPath(root), []byte(`{"version":1,"worktree":"wt-a","id":"wt_1234567890abcdef"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	original := openWorktreeMarkerFile
	openWorktreeMarkerFile = func(*os.Root, string) (*os.File, error) {
		return nil, errors.New("open failure")
	}
	t.Cleanup(func() { openWorktreeMarkerFile = original })
	if _, err := readWorktreeMarker(root); err == nil || !strings.Contains(err.Error(), "read worktree marker") {
		t.Fatalf("injected marker open error = %v", err)
	}
}
