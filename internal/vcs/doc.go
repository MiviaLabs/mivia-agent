// Package vcs runs the git operations for worktrees and revisions: worktree
// create, list, remove, and prune, plus revision resolution and the
// process lease around worktree mutations.
//
// It may import internal/workspace for path confinement, golang.org/x/sys,
// and the standard library. Runtime and CLI packages may import it.
package vcs
