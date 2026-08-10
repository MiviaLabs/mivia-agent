package vcs

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// installFailingWorktreeListGit installs a fake git shim in PATH that
// succeeds for rev-parse (so ensureGitRepo passes) and exits 3 for every
// other command, in particular 'worktree list --porcelain'. The pattern
// comes from TestIsAncestorSurfacesMergeBaseFailure. t.Setenv restores PATH.
func installFailingWorktreeListGit(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	name := "git"
	script := "#!/bin/sh\nif [ \"$1\" = \"rev-parse\" ]; then exit 0; fi\nexit 3\n"
	if runtime.GOOS == "windows" {
		// Windows cannot execute POSIX shell scripts; cmd.exe runs the same
		// fixture as a batch file, resolved through PATHEXT as git.cmd.
		name = "git.cmd"
		script = "@if \"%1\"==\"rev-parse\" exit /b 0\r\nexit /b 3\r\n"
	}
	git := filepath.Join(bin, name)
	if err := os.WriteFile(git, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
}

// TestListSurfacesWorktreeListFailure is the regression test for the DC-9
// defect: List swallowed the 'git worktree list --porcelain' failure and
// returned an empty list with a nil error. This test fails on the old code
// (out, _ := cmd.Output()) and passes after the fix.
func TestListSurfacesWorktreeListFailure(t *testing.T) {
	installFailingWorktreeListGit(t)
	_, err := List(context.Background(), t.TempDir())
	if err == nil {
		t.Fatal("List returned no error for a failing worktree list")
	}
	if !strings.Contains(err.Error(), "worktree list") {
		t.Fatalf("error = %v, want a 'worktree list' failure", err)
	}
}

// TestResolvePropagatesListError proves Resolve propagates the worktree list
// failure instead of returning (nil, nil), so the CLI and workflow layers
// see the git failure instead of reporting 'worktree not found'.
func TestResolvePropagatesListError(t *testing.T) {
	installFailingWorktreeListGit(t)
	_, err := Resolve(context.Background(), t.TempDir(), "wt-a")
	if err == nil {
		t.Fatal("Resolve returned no error for a failing worktree list")
	}
	if !strings.Contains(err.Error(), "worktree list") {
		t.Fatalf("error = %v, want a 'worktree list' failure", err)
	}
}
