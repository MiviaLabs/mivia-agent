package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// TestContextWorkspaceIDIsTheResolvedDirectory pins that a workspace's durable
// identity is the directory itself, not how its path happened to be spelled.
// `mivia chat` passes "." when no --workspace is given, so the id was the hash
// of "." for every project on the machine.
func TestContextWorkspaceIDIsTheResolvedDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	relative := contextWorkspaceID(".")
	absolute := contextWorkspaceID(resolved)
	if relative != absolute {
		t.Fatalf("workspace id depends on path spelling: %q (\".\") vs %q (absolute)", relative, absolute)
	}
	if trailing := contextWorkspaceID(resolved + string(os.PathSeparator)); trailing != absolute {
		t.Fatalf("workspace id depends on a trailing separator: %q vs %q", trailing, absolute)
	}
}

// TestContextWorkspaceIDSeparatesDirectories is the defect itself: two
// different projects, each launched with the default ".", must not share one
// durable workspace identity.
func TestContextWorkspaceIDSeparatesDirectories(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()

	t.Chdir(first)
	firstID := contextWorkspaceID(".")
	t.Chdir(second)
	secondID := contextWorkspaceID(".")

	if firstID == secondID {
		t.Fatalf("two directories share workspace id %q", firstID)
	}
}
