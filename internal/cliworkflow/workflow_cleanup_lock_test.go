package cliworkflow

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

// Cleanup removes a worktree and deletes its branch, so it must hold the same
// cross-process lifecycle lock the interactive `mivia worktree` path holds -
// otherwise it can interleave with a live workflow admission on the same name.
// Both branches are pinned here because inline the acquisition was only
// covered by accident: a skipped release leaked a flock that a later test in
// the package happened to trip over, which made the coverage order-dependent.

func TestLockCleanupWorktreeReleasesOnSuccess(t *testing.T) {
	repo := t.TempDir()
	closed := false
	orig := cleanupLockWorktreeLifecycle
	cleanupLockWorktreeLifecycle = func(root, name string) (*vcs.WorktreeLifecycleLock, error) {
		if root != repo || name != "wt-1" {
			t.Errorf("lock taken for (%q, %q), want (%q, \"wt-1\")", root, name, repo)
		}
		return nil, nil
	}
	t.Cleanup(func() { cleanupLockWorktreeLifecycle = orig })

	// A nil lock with a nil error must NOT be reported as held: releasing it
	// would dereference nil.
	if release, held := lockCleanupWorktree(repo, "wt-1"); held || release != nil {
		t.Fatalf("a nil lock was reported as held (release=%v)", release != nil)
	}
	_ = closed
}

func TestLockCleanupWorktreeReportsNotHeldOnFailure(t *testing.T) {
	orig := cleanupLockWorktreeLifecycle
	cleanupLockWorktreeLifecycle = func(string, string) (*vcs.WorktreeLifecycleLock, error) {
		return nil, errors.New("no lock available")
	}
	t.Cleanup(func() { cleanupLockWorktreeLifecycle = orig })

	release, held := lockCleanupWorktree(t.TempDir(), "wt-1")
	if held {
		t.Fatal("a failed acquisition was reported as held; the caller would release a lock it never took")
	}
	if release != nil {
		t.Fatal("a failed acquisition returned a release function; deferring it would panic")
	}
}

func TestLockCleanupWorktreeSkipsBlankIdentity(t *testing.T) {
	called := false
	orig := cleanupLockWorktreeLifecycle
	cleanupLockWorktreeLifecycle = func(string, string) (*vcs.WorktreeLifecycleLock, error) {
		called = true
		return nil, nil
	}
	t.Cleanup(func() { cleanupLockWorktreeLifecycle = orig })

	for _, tc := range [][2]string{{"", "n"}, {"/repo", ""}, {"", ""}} {
		if _, held := lockCleanupWorktree(tc[0], tc[1]); held {
			t.Errorf("lockCleanupWorktree(%q, %q) reported held", tc[0], tc[1])
		}
	}
	if called {
		t.Error("a blank root or name must not reach the lock at all")
	}
}

// TestLockCleanupWorktreeHoldsARealLock is the end of the chain: against a real
// repository the helper takes an actual lock and hands back a release that
// frees it, so a second acquisition afterwards succeeds.
func TestLockCleanupWorktreeHoldsARealLock(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "seed.txt"), []byte("seed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	initWorkflowGitRepo(t, repo)

	release, held := lockCleanupWorktree(repo, "wt-real")
	if !held {
		t.Fatal("lockCleanupWorktree did not take a lock on a real repository")
	}
	release()

	again, heldAgain := lockCleanupWorktree(repo, "wt-real")
	if !heldAgain {
		t.Fatal("the lock was not released; a second cleanup of the same worktree would be blocked")
	}
	again()
}
