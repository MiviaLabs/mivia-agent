package localengine

import (
	"context"
	"fmt"
	"strings"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	workflowspace "github.com/MiviaLabs/mivia-agent/internal/workflows/workspace"
)

// prepareResumeWorktree preserves unfinished edits by requiring a recorded
// Git worktree to resolve before local resume starts execution.
func (e *Engine) prepareResumeWorktree(ctx context.Context, run workflowledger.RunSnapshot) error {
	if run.WorktreeName == "" {
		return nil
	}
	recorded := workflowspace.Identity{
		BaseRef: run.BaseRef, BaseCommit: run.BaseCommit,
		WorktreeName: run.WorktreeName, Branch: "wf/" + run.WorktreeName,
	}
	identity, err := workflowspace.Resolve(ctx, e.WorkspaceRoot, recorded)
	if err == nil {
		e.recordWorktree(run.RunID, identity)
		return nil
	}
	// Legacy non-Git workspaces can record a synthetic name. They do not have
	// a durable worktree to preserve, so keep the read-only resume behavior.
	if strings.Contains(err.Error(), "not a git repository") {
		return nil
	}
	return fmt.Errorf("workflow run %q cannot resume: recorded worktree %q is unavailable; unfinished edits cannot be recovered: %w", run.RunID, run.WorktreeName, err)
}
