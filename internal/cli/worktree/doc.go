// Package worktree implements the worktree command surface and the
// worktree context support: lifecycle, markers, locks, and recovery. It
// binds chat sessions to managed git worktrees.
//
// It may import internal/vcs, internal/worktreeroute, and the other runtime
// packages. The internal/cli and internal/cli/chat packages may import it.
package worktree
