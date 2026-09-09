package cliworkflow

import "github.com/MiviaLabs/mivia-agent/internal/vcs"

// cleanupLockWorktreeLifecycle is a seam so a test can drive both branches of
// the lock acquisition below. Inline, the acquisition was only ever covered
// incidentally - a mutation that skipped the release leaked a flock that some
// LATER test in the package happened to trip over, so whether the defect was
// caught depended on test order.
var cleanupLockWorktreeLifecycle = vcs.LockWorktreeLifecycle

// lockCleanupWorktree takes the cross-process worktree lifecycle lock for a
// cleanup, reporting held=false when the repository cannot provide one.
//
// A lock failure is not fatal: the lock removes a race between concurrent
// lifecycle operations, and a repository that cannot host one is no worse off
// than before it existed. It returns the release function separately rather
// than a lock value so the caller can never defer a method on a nil lock -
// the shape the inline version had, where a failed acquisition would have
// panicked had the condition ever been inverted.
func lockCleanupWorktree(mainRoot, name string) (release func(), held bool) {
	if mainRoot == "" || name == "" {
		return nil, false
	}
	lock, err := cleanupLockWorktreeLifecycle(mainRoot, name)
	if err != nil || lock == nil {
		return nil, false
	}
	return lock.Close, true
}
