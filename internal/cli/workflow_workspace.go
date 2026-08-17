package cli

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	workflowspace "github.com/MiviaLabs/mivia-agent/internal/workflows/workspace"
)

const workflowBranchPrefix = "wf/"

var (
	workflowVCSResolve      = vcs.Resolve
	workflowWorkspaceEnsure = workflowspace.Ensure
)

func newCLIWorkflowRunID() string {
	var value [10]byte
	_, _ = rand.Read(value[:])
	return "wfr-" + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(value[:])
}

func selectWorkflowWorkspace(ctx context.Context, sourceRoot, runID string, writeCapable bool, recorded *workflowledger.RunSnapshot) (workflowspace.Identity, func(), error) {
	if recorded != nil {
		identity := workflowspace.Identity{
			BaseRef: recorded.BaseRef, BaseCommit: recorded.BaseCommit,
			OriginBaseCommit: recorded.OriginBaseCommit,
			WorktreeName:     recorded.WorktreeName,
		}
		if recorded.WorktreeName != "" {
			identity.Branch = workflowBranchPrefix + recorded.WorktreeName
		}
		if writeCapable && recorded.WorktreeName == "" {
			return workflowspace.Identity{}, nil, fmt.Errorf("write-capable workflow run %q has no recorded worktree", runID)
		}
		resolved, err := workflowspace.Resolve(ctx, sourceRoot, identity)
		if err != nil {
			if writeCapable {
				return workflowspace.Identity{}, nil, fmt.Errorf("workflow run %q cannot resume: recorded worktree %q is unavailable; unfinished edits cannot be recovered; recreate the worktree from branch %q if it survives, else start a fresh run: %w", runID, recorded.WorktreeName, workflowBranchPrefix+recorded.WorktreeName, err)
			}
			return workflowspace.Identity{}, nil, err
		}
		return resolved, func() {}, nil
	}
	if !writeCapable {
		identity, err := workflowWorkspaceEnsure(ctx, sourceRoot, runID, workflowspace.IsolationReadOnly)
		return identity, func() {}, err
	}
	name, err := vcs.SanitizeName("workflow-" + runID)
	if err != nil {
		return workflowspace.Identity{}, nil, err
	}
	mainRoot, err := vcs.MainRepoRoot(sourceRoot)
	if err != nil {
		return workflowspace.Identity{}, nil, err
	}
	existing, err := workflowVCSResolve(ctx, mainRoot, name)
	if err != nil {
		return workflowspace.Identity{}, nil, err
	}
	identity, err := workflowWorkspaceEnsure(ctx, sourceRoot, runID, workflowspace.IsolationWorktree)
	if err != nil {
		return workflowspace.Identity{}, nil, err
	}
	if existing != nil {
		return identity, func() {}, nil
	}
	cleanup := func() {
		_ = vcs.RemoveWithPrefix(context.Background(), identity.MainRoot, identity.WorktreeName, workflowBranchPrefix)
	}
	return identity, cleanup, nil
}
