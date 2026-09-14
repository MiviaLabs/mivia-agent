package cli

// Wires internal/cli/worktree's OpenRepositoryContextStoreFunc to
// clichat.OpenRepositoryContextStore at process start. cliworktree cannot import
// internal/cli directly (internal/cli imports internal/cli/worktree for
// worktree-marker/route helpers, e.g. chat_command.go, chat_repository_binding.go,
// workflow_resume_lock.go; the reverse import would close a cycle), so the
// router wires this one function in instead. See cliworktree's
// OpenRepositoryContextStoreFunc doc comment for the full rationale and the
// blocker this stands in for.

import (
	clichat "github.com/MiviaLabs/mivia-agent/internal/cli/chat"
	cliworktree "github.com/MiviaLabs/mivia-agent/internal/cli/worktree"
)

func init() {
	cliworktree.OpenRepositoryContextStoreFunc = clichat.OpenRepositoryContextStore
}
