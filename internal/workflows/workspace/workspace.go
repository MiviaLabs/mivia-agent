// Package workspace manages the Git workspace for a workflow run.
package workspace

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	baseworkspace "github.com/MiviaLabs/mivia-agent/internal/workspace"
)

const workflowBranchPrefix = "wf/"

var ensureMu sync.Mutex

var (
	workflowCurrentBranch = vcs.CurrentBranch
	workflowCurrentCommit = vcs.CurrentCommit
	workflowResolve       = vcs.Resolve
	workflowCreate        = vcs.CreateWithPrefix
	workflowRemove        = vcs.RemoveWithPrefix
)

// Isolation selects the workspace policy for a workflow run.
type Isolation uint8

const (
	// IsolationReadOnly uses the caller checkout.
	IsolationReadOnly Isolation = iota
	// IsolationWorktree uses a run-specific Git worktree.
	IsolationWorktree
)

// Identity records the Git identity of a workflow workspace.
type Identity struct {
	Root             string
	MainRoot         string
	BaseRef          string
	BaseCommit       string
	OriginBaseCommit string
	WorktreeName     string
	Branch           string
}

// Ensure returns the workspace for a new or repeated run admission.
func Ensure(ctx context.Context, sourceRoot, runID string, isolation Isolation) (Identity, error) {
	if isolation == IsolationReadOnly {
		return readOnlyIdentity(sourceRoot)
	}
	if isolation != IsolationWorktree {
		return Identity{}, fmt.Errorf("unknown workflow isolation %d", isolation)
	}
	callerRoot, err := vcs.RepoRoot(sourceRoot)
	if err != nil {
		return Identity{}, err
	}
	identity, err := admissionIdentity(ctx, callerRoot, runID)
	if err != nil {
		return Identity{}, err
	}

	ensureMu.Lock()
	defer ensureMu.Unlock()
	return ensureWorktree(ctx, identity)
}

// EnsureRecorded recreates a worktree from its immutable admission identity.
// It never derives a base from the current checkout.
func EnsureRecorded(ctx context.Context, sourceRoot string, recorded Identity) (Identity, error) {
	if recorded.WorktreeName == "" {
		return readOnlyIdentity(sourceRoot)
	}
	mainRoot, err := vcs.MainRepoRoot(sourceRoot)
	if err != nil {
		return Identity{}, err
	}
	if recorded.MainRoot != "" && filepath.Clean(recorded.MainRoot) != filepath.Clean(mainRoot) {
		return Identity{}, fmt.Errorf("workflow main root is %q, want %q", mainRoot, recorded.MainRoot)
	}
	recorded.MainRoot = mainRoot
	if recorded.BaseCommit == "" {
		return Identity{}, fmt.Errorf("workflow base commit must not be empty")
	}
	return ensureWorktree(ctx, recorded)
}

// Resolve validates and returns a recorded workflow workspace.
func Resolve(ctx context.Context, sourceRoot string, recorded Identity) (Identity, error) {
	if recorded.WorktreeName == "" {
		if recorded.MainRoot != "" || recorded.BaseRef != "" || recorded.BaseCommit != "" || recorded.OriginBaseCommit != "" || recorded.Branch != "" {
			return Identity{}, fmt.Errorf("read-only workflow identity contains worktree data")
		}
		return readOnlyIdentity(sourceRoot)
	}
	mainRoot, err := vcs.MainRepoRoot(sourceRoot)
	if err != nil {
		return Identity{}, err
	}
	if recorded.MainRoot != "" && filepath.Clean(recorded.MainRoot) != filepath.Clean(mainRoot) {
		return Identity{}, fmt.Errorf("workflow main root is %q, want %q", mainRoot, recorded.MainRoot)
	}
	recorded.MainRoot = mainRoot
	return validateWorktree(ctx, recorded)
}

func readOnlyIdentity(sourceRoot string) (Identity, error) {
	root, err := baseworkspace.Open(sourceRoot)
	if err != nil {
		return Identity{}, err
	}
	return Identity{Root: root.Abs}, nil
}

func admissionIdentity(ctx context.Context, callerRoot, runID string) (Identity, error) {
	if strings.TrimSpace(runID) == "" {
		return Identity{}, fmt.Errorf("workflow run ID must not be empty")
	}
	name, err := vcs.SanitizeName("workflow-" + runID)
	if err != nil {
		return Identity{}, err
	}
	mainRoot, err := vcs.MainRepoRoot(callerRoot)
	if err != nil {
		return Identity{}, err
	}
	baseRef, err := workflowCurrentBranch(ctx, callerRoot)
	if err != nil {
		return Identity{}, fmt.Errorf("get workflow base ref: %w", err)
	}
	baseCommit, err := workflowCurrentCommit(ctx, callerRoot)
	if err != nil {
		return Identity{}, fmt.Errorf("get workflow base commit: %w", err)
	}
	return Identity{
		MainRoot: mainRoot, BaseRef: baseRef, BaseCommit: baseCommit,
		OriginBaseCommit: originBaseCommit(ctx, callerRoot, baseRef),
		WorktreeName:     name, Branch: workflowBranchPrefix + name,
	}, nil
}

// originBaseCommit resolves the origin tracking ref for the base branch at
// admission. It reads refs/remotes/origin/<base> only when the ref is already
// present locally: the admission path makes no network calls, so a remote ref
// that has never been fetched is recorded as empty and delivery's retryable
// path fetches and verifies the remote base later.
func originBaseCommit(ctx context.Context, root, baseRef string) string {
	// A detached HEAD has no branch, so it has no remote tracking ref.
	if baseRef == "" || baseRef == "HEAD" {
		return ""
	}
	commit, err := vcs.ResolveCommit(ctx, root, "refs/remotes/origin/"+baseRef)
	if err != nil {
		return ""
	}
	return commit
}

func ensureWorktree(ctx context.Context, identity Identity) (Identity, error) {
	existing, err := workflowResolve(ctx, identity.MainRoot, identity.WorktreeName)
	if err != nil {
		return Identity{}, err
	}
	if existing != nil {
		return validateWorktree(ctx, identity)
	}
	if err := validateRetainedBranch(ctx, identity); err != nil {
		return Identity{}, err
	}
	created, err := workflowCreate(
		ctx, identity.MainRoot, identity.WorktreeName, identity.BaseCommit, workflowBranchPrefix,
	)
	if err != nil {
		if recovered, resolveErr := workflowResolve(ctx, identity.MainRoot, identity.WorktreeName); resolveErr == nil && recovered != nil {
			return validateWorktree(ctx, identity)
		}
		return Identity{}, err
	}
	identity.Root = created.Path
	validated, err := validateWorktree(ctx, identity)
	if err == nil {
		return validated, nil
	}
	if removeErr := workflowRemove(ctx, identity.MainRoot, identity.WorktreeName, workflowBranchPrefix); removeErr != nil {
		return Identity{}, fmt.Errorf("%v; cleanup new workflow worktree: %w", err, removeErr)
	}
	return Identity{}, err
}

func validateRetainedBranch(ctx context.Context, identity Identity) error {
	branchCommit, err := vcs.ResolveCommit(ctx, identity.MainRoot, "refs/heads/"+identity.Branch)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return nil
	}
	ok, err := vcs.IsAncestor(ctx, identity.MainRoot, identity.BaseCommit, branchCommit)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("workflow branch %q does not contain base commit %s", identity.Branch, identity.BaseCommit)
	}
	return nil
}

func validateWorktree(ctx context.Context, identity Identity) (Identity, error) {
	wantName, err := vcs.SanitizeName(identity.WorktreeName)
	if err != nil || wantName != identity.WorktreeName {
		return Identity{}, fmt.Errorf("invalid workflow worktree name %q", identity.WorktreeName)
	}
	wantBranch := workflowBranchPrefix + identity.WorktreeName
	if identity.Branch != "" && identity.Branch != wantBranch {
		return Identity{}, fmt.Errorf("workflow branch is %q, want %q", identity.Branch, wantBranch)
	}
	if identity.BaseCommit == "" {
		return Identity{}, fmt.Errorf("workflow base commit must not be empty")
	}
	worktree, err := workflowResolve(ctx, identity.MainRoot, identity.WorktreeName)
	if err != nil {
		return Identity{}, err
	}
	if worktree == nil {
		return Identity{}, fmt.Errorf("workflow worktree %q does not exist", identity.WorktreeName)
	}
	if worktree.Branch != wantBranch {
		return Identity{}, fmt.Errorf("workflow worktree branch is %q, want %q", worktree.Branch, wantBranch)
	}
	commit, err := vcs.CurrentCommit(ctx, worktree.Path)
	if err != nil {
		return Identity{}, err
	}
	ok, err := vcs.IsAncestor(ctx, identity.MainRoot, identity.BaseCommit, commit)
	if err != nil {
		return Identity{}, err
	}
	if !ok {
		return Identity{}, fmt.Errorf("workflow commit %s does not contain base commit %s", commit, identity.BaseCommit)
	}
	identity.Root = worktree.Path
	identity.Branch = wantBranch
	return identity, nil
}
