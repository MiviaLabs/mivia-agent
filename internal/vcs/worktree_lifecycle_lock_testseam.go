package vcs

import "os"

// SetLifecycleGitRootOpenerForTest replaces the seam LockWorktreeLifecycle
// uses to open the repository's Git common directory, and returns a restore
// function. It exists so callers in OTHER packages can drive the lock's
// fail-closed paths: the lock moved here from internal/cliworktree so the
// workflow engine could take it too, and cliworktree's fault-injection tests
// came with it.
func SetLifecycleGitRootOpenerForTest(fn func(string) (*os.Root, error)) func() {
	prev := openLifecycleGitRoot
	openLifecycleGitRoot = fn
	return func() { openLifecycleGitRoot = prev }
}
