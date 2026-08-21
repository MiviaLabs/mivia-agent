package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestWorktreeMarkerRejectsOversizedRegularFile(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".mivia"), 0o700); err != nil {
		t.Fatal(err)
	}
	marker := []byte(`{"version":1,"worktree":"wt-a","id":"wt_1234567890abcdef"}`)
	marker = append(marker, bytes.Repeat([]byte(" "), 1<<20)...)
	if err := os.WriteFile(worktreeMarkerPath(root), marker, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadWorktreeMarker(root); err == nil {
		t.Fatal("oversized marker was accepted")
	}
}
