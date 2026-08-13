package localengine

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

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

// resolveOriginURL records the delivery origin for the immutable admission
// record, mirroring the CLI's workflowDeliveryAdmission: the main repository
// must have an origin remote and the delivery base must sit at the admitted
// base commit. A delivery workflow without a matching origin cannot publish.
func resolveOriginURL(ctx context.Context, identity workflowspace.Identity, base string) (string, error) {
	if identity.MainRoot == "" {
		return "", fmt.Errorf("workflow identity has no main root")
	}
	git := delivery.GitContext{Dir: identity.MainRoot, GitDir: filepath.Join(identity.MainRoot, ".git")}
	origin, err := delivery.RealGit{}.Run(ctx, git, "remote", "get-url", "origin")
	if err != nil {
		return "", fmt.Errorf("workflow requires delivery but the repository has no origin remote: %w", err)
	}
	baseCommit, err := delivery.RealGit{}.Run(ctx, git, "rev-parse", "--verify", "--end-of-options", "refs/heads/"+base+"^{commit}")
	if err != nil || strings.TrimSpace(baseCommit) != identity.BaseCommit {
		return "", fmt.Errorf("delivery base %q is not at the admitted base commit", base)
	}
	return strings.TrimSpace(origin), nil
}
