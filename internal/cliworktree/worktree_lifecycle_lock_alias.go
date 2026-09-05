package cliworktree

import (
	"os"

	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

// The worktree lifecycle lock and its flock primitive now live in
// internal/vcs, so the WORKFLOW engine can take the same lock. It could not
// import this package (a CLI layer), which is why the engine's create/remove
// path ran under nothing but an in-process mutex while `mivia worktree
// remove` held a real cross-process lock on the same name.
//
// These aliases keep this package's call sites and tests on the names they
// already use.

// LockWorktreeLifecycle takes the cross-process lifecycle lock for one
// worktree name.
func LockWorktreeLifecycle(root, name string) (*vcs.WorktreeLifecycleLock, error) {
	return vcs.LockWorktreeLifecycle(root, name)
}

// LockWorktreeMarkerFile takes an exclusive advisory lock on an open marker file.
func LockWorktreeMarkerFile(file *os.File) (func(), error) {
	return vcs.LockWorktreeMarkerFile(file)
}
