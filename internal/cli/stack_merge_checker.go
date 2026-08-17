package cli

// The merge oracle: MergeChecker and its production implementation,
// gitMergeChecker. Split out of stack_reconcile.go to keep that file under
// the repo's per-file line ceiling (.mivia/policy/go-structure.json).

import (
	"context"
	"errors"
	"fmt"
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

// errMergeProbeUnavailable marks a merge verdict that no probe could answer.
// It never means "not merged": callers that must fail closed keep waiting,
// and callers that can tolerate an unknown verdict can tell it apart from a
// confident "not merged" (which is reported as merged=false with a nil error).
var errMergeProbeUnavailable = errors.New("merge probe unavailable")

// gitMergeChecker is the MergeChecker over the repository's origin remote.
// It first checks whether the pushed head commit is already an ancestor of the
// remote base branch, then falls back to the remote PR state when the local
// check is inconclusive or unavailable. Only when both probes fail to answer
// does it report errMergeProbeUnavailable, so the stack keeps waiting instead
// of completing on a guess.
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
	ancestor, localErr := g.localAncestor(ctx, baseBranch, headCommit)
	if ancestor {
		return true, nil
	}

	// The local answer is not final: it is either inconclusive (a squash or
	// rebase merge is not an ancestor) or unavailable (localErr). Ask the
	// remote host for the PR state.
	if strings.TrimSpace(repoSlug) == "" || g.pr == nil {
		if localErr != nil {
			// No probe answered. Do not report an unknown as "not merged".
			return false, unavailableProbe(localErr)
		}
		return false, nil
	}
	merged, err := g.pr.IsMerged(ctx, repoSlug, headBranch)
	if err != nil {
		return false, unavailableProbe(err)
	}
	return merged, nil
}

// localAncestor reports whether headCommit is already in the remote base
// branch. A (false, nil) result is git's confident "not an ancestor" (exit
// code 1). A non-nil error means the local probe could not answer at all:
// git exits 128 for a missing ref (the base branch was deleted after a squash
// merge and pruned) and for a missing commit (never fetched, or garbage
// collected). That case must fall through to the remote probe, which is the
// probe designed to answer it.
func (g gitMergeChecker) localAncestor(ctx context.Context, baseBranch, headCommit string) (bool, error) {
	baseRef := "refs/remotes/origin/" + baseBranch
	if _, err := g.git.Run(ctx, g.gc, "merge-base", "--is-ancestor", headCommit, baseRef); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// unavailableProbe wraps a probe failure so callers can match both the
// sentinel and the cause.
func unavailableProbe(err error) error {
	return fmt.Errorf("probe merge state: %w: %w", errMergeProbeUnavailable, err)
}
