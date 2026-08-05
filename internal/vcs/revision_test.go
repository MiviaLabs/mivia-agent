package vcs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCurrentCommitAndResolveCommit(t *testing.T) {
	root := initTestRepo(t)
	want := gitOutput(t, root, "rev-parse", "HEAD")

	got, err := CurrentCommit(context.Background(), root)
	if err != nil {
		t.Fatalf("CurrentCommit: %v", err)
	}
	if got != want {
		t.Fatalf("CurrentCommit = %q, want %q", got, want)
	}
	resolved, err := ResolveCommit(context.Background(), root, "HEAD")
	if err != nil {
		t.Fatalf("ResolveCommit: %v", err)
	}
	if resolved != want {
		t.Fatalf("ResolveCommit = %q, want %q", resolved, want)
	}
}

func TestIsAncestorSurfacesMergeBaseFailure(t *testing.T) {
	bin := t.TempDir()
	git := filepath.Join(bin, "git")
	script := "#!/bin/sh\nif [ \"$1\" = \"rev-parse\" ]; then echo 0123456789012345678901234567890123456789; exit 0; fi\nexit 2\n"
	if err := os.WriteFile(git, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	if _, err := IsAncestor(context.Background(), t.TempDir(), "a", "b"); err == nil || !strings.Contains(err.Error(), "merge-base") {
		t.Fatalf("error = %v, want merge-base failure", err)
	}
}

func TestIsAncestor(t *testing.T) {
	root := initTestRepo(t)
	base := gitOutput(t, root, "rev-parse", "HEAD")
	write(t, root, "next.txt", "next")
	run(t, root, "git", "add", "next.txt")
	run(t, root, "git", "commit", "-m", "next")
	head := gitOutput(t, root, "rev-parse", "HEAD")

	got, err := IsAncestor(context.Background(), root, base, head)
	if err != nil {
		t.Fatalf("IsAncestor: %v", err)
	}
	if !got {
		t.Fatal("base is not reported as an ancestor of HEAD")
	}
	got, err = IsAncestor(context.Background(), root, head, base)
	if err != nil {
		t.Fatalf("IsAncestor reverse: %v", err)
	}
	if got {
		t.Fatal("HEAD is reported as an ancestor of base")
	}
}

func TestRevisionHelpersRejectInvalidValues(t *testing.T) {
	root := initTestRepo(t)
	if _, err := ResolveCommit(context.Background(), root, "  "); err == nil {
		t.Fatal("ResolveCommit accepts an empty ref")
	}
	if _, err := ResolveCommit(context.Background(), root, "missing-ref"); err == nil {
		t.Fatal("ResolveCommit accepts a missing ref")
	}
	if _, err := IsAncestor(context.Background(), root, "missing-ref", "HEAD"); err == nil {
		t.Fatal("IsAncestor accepts a missing ref")
	}
	if _, err := IsAncestor(context.Background(), root, "HEAD", "missing-ref"); err == nil {
		t.Fatal("IsAncestor accepts a missing descendant")
	}
	if _, err := IsAncestor(context.Background(), t.TempDir(), "HEAD", "HEAD"); err == nil {
		t.Fatal("IsAncestor accepts a non-repository directory")
	}
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}
