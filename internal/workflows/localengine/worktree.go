package localengine

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	workflowspace "github.com/MiviaLabs/mivia-agent/internal/workflows/workspace"
)

// ensureRunWorktree creates or validates the git worktree for a run, mirroring
// the CLI's workspace selection: a fresh run gets a new worktree via
// workflowspace.Ensure; a resumed run re-validates the recorded worktree and
// recreates it when missing. ok=false means the workspace is not a usable git
// repository or the worktree could not be ensured; callers fall back to the
// previous no-worktree behavior.
func (e *Engine) ensureRunWorktree(ctx context.Context, runID string, recorded *workflowledger.RunSnapshot) (workflowspace.Identity, bool) {
	if e.WorkspaceRoot == "" {
		return workflowspace.Identity{}, false
	}
	if recorded != nil && recorded.WorktreeName != "" {
		recordedIdentity := workflowspace.Identity{
			BaseRef: recorded.BaseRef, BaseCommit: recorded.BaseCommit,
			WorktreeName: recorded.WorktreeName, Branch: "wf/" + recorded.WorktreeName,
		}
		if identity, err := workflowspace.Resolve(ctx, e.WorkspaceRoot, recordedIdentity); err == nil {
			return identity, true
		}
		identity, err := workflowspace.EnsureRecorded(ctx, e.WorkspaceRoot, recordedIdentity)
		if err != nil {
			return workflowspace.Identity{}, false
		}
		return identity, true
	}
	identity, err := workflowspace.Ensure(ctx, e.WorkspaceRoot, runID, workflowspace.IsolationWorktree)
	if err != nil {
		return workflowspace.Identity{}, false
	}
	return identity, identity.WorktreeName != ""
}

// recordWorktree stores the resolved worktree identity for a run so delivery
// can pin the run's git context without re-running vcs discovery.
func (e *Engine) recordWorktree(runID string, identity workflowspace.Identity) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.worktrees == nil {
		e.worktrees = make(map[string]workflowspace.Identity)
	}
	e.worktrees[runID] = identity
}

// worktreeIdentity returns the recorded worktree identity for a run.
func (e *Engine) worktreeIdentity(runID string) (workflowspace.Identity, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	identity, ok := e.worktrees[runID]
	return identity, ok
}

// forgetWorktree removes runID's recorded worktree identity. Callers use it
// once a run leaves the engine for good - deleted, or settled to a terminal
// status - so e.worktrees does not grow forever with entries no run will
// ever look up again.
func (e *Engine) forgetWorktree(runID string) {
	e.mu.Lock()
	delete(e.worktrees, runID)
	e.mu.Unlock()
}

// resolveOriginURL records the delivery origin and target-derived origin
// base commit for the immutable admission record: the main repository must
// have an origin remote, and the delivery base must CONTAIN the admitted
// worktree base commit (delivery.AdmitDeliveryTarget - dedups the CLI's
// workflowDeliveryAdmission). A delivery workflow without a matching origin
// cannot publish. The returned originBaseCommit is the target's fetched
// origin tip, recorded so delivery-time verification detects a rewrite of
// the TARGET, not the worktree's source branch.
func resolveOriginURL(ctx context.Context, identity workflowspace.Identity, base string) (originURL, originBaseCommit string, err error) {
	if identity.MainRoot == "" {
		return "", "", fmt.Errorf("workflow identity has no main root")
	}
	git := delivery.GitContext{Dir: identity.MainRoot, GitDir: filepath.Join(identity.MainRoot, ".git")}
	return delivery.AdmitDeliveryTarget(ctx, delivery.RealGit{}, git, base, identity.BaseCommit)
}
