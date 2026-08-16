package cli

// The merge oracle: MergeChecker and its production implementation,
// gitMergeChecker. Split out of stack_reconcile.go to keep that file under
// the repo's per-file line ceiling (.mivia/policy/go-structure.json).

import (
	"context"
	"errors"
	"os/exec"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
)

// MergeChecker reports whether a chunk's PR is merged from durable git state
// and, when necessary, the remote PR state. Tests inject a fake.
//
// wasPushed is the driver's durable pushed evidence for the run (a delivery
// record that reached pushed/succeeded with a commit SHA). Without it a
// missing remote ref only means "never pushed", not "merged".
type MergeChecker interface {
	Merged(ctx context.Context, headBranch, baseBranch, headCommit, repoSlug string, wasPushed bool) (bool, error)
}

// gitMergeChecker is the MergeChecker over the repository's origin remote.
// It first checks whether the pushed head commit is already an ancestor of the
// remote base branch, then falls back to the remote PR state when the local
// check is inconclusive. Any failure degrades to "not merged" so the stack
// keeps waiting instead of completing on a guess.
type gitMergeChecker struct {
	git delivery.GitRunner
	pr  delivery.PRClient
	gc  delivery.GitContext
}

// Merged reports whether the chunk's PR has landed. It requires durable
// pushed evidence (wasPushed) and a known head commit. A branch that was
// closed unmerged and then deleted must NOT read as merged.
func (g gitMergeChecker) Merged(ctx context.Context, headBranch, baseBranch, headCommit, repoSlug string, wasPushed bool) (bool, error) {
	if strings.TrimSpace(headBranch) == "" || strings.TrimSpace(baseBranch) == "" || strings.TrimSpace(headCommit) == "" || !wasPushed {
		return false, nil
	}

	// Fast, network-free check: is the pushed commit already in the base
	// branch? This covers normal and fast-forward merges.
	baseRef := "refs/remotes/origin/" + baseBranch
	_, err := g.git.Run(ctx, g.gc, "merge-base", "--is-ancestor", headCommit, baseRef)
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		// Missing ref or other git failure: fall through to the remote check.
	}

	// Local check inconclusive (squash/rebase merge, stale refs, missing
	// base). Ask the remote host for the PR state.
	if strings.TrimSpace(repoSlug) == "" || g.pr == nil {
		return false, nil
	}
	merged, err := g.pr.IsMerged(ctx, repoSlug, headBranch)
	if err != nil {
		// Remote lookup failed: do not complete the stack on a guess.
		return false, nil
	}
	return merged, nil
}
