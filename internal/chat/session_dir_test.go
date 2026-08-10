package chat

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

func gitInitDir(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
		{"commit", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// TestCurrentDirContextNonGit verifies a save outside any git repo still
// works: dir is captured and worktree stays empty instead of erroring.
func TestCurrentDirContextNonGit(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	d, wt := currentDirContext()
	if d == "" || filepath.Clean(d) != filepath.Clean(dir) {
		t.Fatalf("dir = %q, want %q", d, dir)
	}
	if wt != "" {
		t.Fatalf("worktree = %q, want empty outside a git repo", wt)
	}
}

// TestCurrentDirContextWhenCwdDeleted covers the os.Getwd error branch of
// currentDirContext: with the process cwd removed, Getwd fails and the
// function reports no directory instead of panicking.
func TestCurrentDirContextWhenCwdDeleted(t *testing.T) {
	base := t.TempDir()
	gone := filepath.Join(base, "gone")
	if err := os.MkdirAll(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(gone)
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	d, wt := currentDirContext()
	if runtime.GOOS == "darwin" {
		if wt != "" || d == "" {
			t.Fatalf("currentDirContext after cwd removal = %q/%q, want a directory and empty worktree", d, wt)
		}
		return
	}
	if d != "" || wt != "" {
		t.Fatalf("currentDirContext after cwd removal = %q/%q, want empty/empty", d, wt)
	}
}

// TestCurrentDirContextInMiviaWorktree verifies the worktree name is captured
// for a session saved inside a mivia-managed worktree.
func TestCurrentDirContextInMiviaWorktree(t *testing.T) {
	root := t.TempDir()
	gitInitDir(t, root)
	wt, err := vcs.Create(context.Background(), root, "feature-x", "HEAD")
	if err != nil {
		t.Fatalf("vcs.Create: %v", err)
	}
	t.Chdir(wt.Path)
	d, name := currentDirContext()
	if filepath.Clean(d) != filepath.Clean(wt.Path) {
		t.Fatalf("dir = %q, want %q", d, wt.Path)
	}
	if name != "feature-x" {
		t.Fatalf("worktree = %q, want feature-x", name)
	}
}
