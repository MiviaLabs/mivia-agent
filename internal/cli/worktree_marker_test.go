package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

func TestWorktreeMarkerRoundTripAndRejectsWrongRoot(t *testing.T) {
	root := t.TempDir()
	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	if err := writeWorktreeMarker(root, instance); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	got, err := readWorktreeMarker(root)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if got != instance {
		t.Fatalf("marker = %+v, want %+v", got, instance)
	}
	if err := os.Mkdir(filepath.Join(root, "child"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := readWorktreeMarker(filepath.Join(root, "child")); err == nil {
		t.Fatal("subdirectory marker read succeeded")
	}
}

func TestWorktreeMarkerRejectsSymlinkDirectory(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".mivia")); err != nil {
		t.Fatal(err)
	}
	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	if err := writeWorktreeMarker(root, instance); err == nil {
		t.Fatal("write through symlink directory succeeded")
	}
}
