//go:build unix

package vcs

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// TestLockWorktreeLifecycleRejectsNonRegularLockFile reaches the lock-file
// shape guard through LockWorktreeLifecycle itself, with a FIFO.
//
// The existing fifo test calls openWorktreeLifecycleLockFile directly, which
// skips this guard entirely, and a symlink cannot exercise it either: lstat
// reports a symlink as BOTH ModeSymlink and not-regular, so it satisfies each
// arm of
//
//	info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()
//
// and cannot tell a working "||" from a mutated "&&". A FIFO is the shape that
// separates them - not a symlink, not regular - so only the second arm is
// true and the disjunction has to carry the rejection on its own.
func TestLockWorktreeLifecycleRejectsNonRegularLockFile(t *testing.T) {
	repo := initTestRepo(t)
	commonDir, err := WorktreeGitCommonDir(repo)
	if err != nil {
		t.Fatalf("WorktreeGitCommonDir: %v", err)
	}
	lockDir := filepath.Join(commonDir, worktreeLifecycleLockDir)
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		t.Fatal(err)
	}
	name := "wt-fifo"
	sanitized, err := SanitizeName(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(lockDir, sanitized+".lock"), 0o600); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}

	lock, err := LockWorktreeLifecycle(repo, name)
	if lock != nil {
		lock.Close()
	}
	if err == nil {
		t.Fatal("a FIFO standing in for the lifecycle lock file was accepted")
	}
	if !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("error = %v, want it to name the lock file's shape", err)
	}
}
