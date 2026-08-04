// Package workspace provides workflow-specific Git worktree lifecycle management.
// It wraps the lower-level vcs package with workflow-specific semantics:
// per-run branch naming, recorded base commit, and cleanup lifecycle.
//
// Phase 2 deliverable — ledger, isolated worktree, and lifecycle.
package workspace
