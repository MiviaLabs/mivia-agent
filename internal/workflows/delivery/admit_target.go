package delivery

import (
	"context"
	"fmt"
	"strings"
)

// AdmitDeliveryTarget verifies a fresh delivery-required run's target
// branch admits, and pins the value delivery-time rewrite detection must
// compare against later.
//
// The repository must have an origin remote, and the delivery target
// (base) must exist on that remote and CONTAIN (as an ancestor) the commit
// the run's worktree started from - the worktree's own source branch does
// NOT need to equal the target by name. This replaces an older check that
// required the target to sit at the EXACT SAME commit as the worktree
// base, which only ever admitted a run started at the target's then-tip.
//
// The containment test fetches the target from the ADMITTED origin URL
// (never a possibly-stale local refs/heads/<base>), so a target that has
// advanced beyond what this checkout last saw still admits. When the
// fetched tip does NOT contain the worktree base, one fallback is tried:
// the LOCAL refs/heads/<base>, for the ordinary case where the operator
// committed to the target locally but not yet pushed it (see
// TestAdmitDeliveryTargetLocalAheadOfOriginAccepted). The fallback accepts
// only a local ref strictly AHEAD of the fetched origin tip; a DIVERGED
// local ref (a local rebase, or a target rewritten on origin while this
// clone was stale) is refused, because the recorded pin would then be
// unrelated to the worktree base and delivery-time rewrite detection would
// compare the pin against the same rewritten history it came from.
//
// The returned targetOriginCommit is always the FETCHED origin tip (never
// the local fallback ref); callers record it as OriginBaseCommit, the pin
// verifyRemoteBaseAncestry compares a later fetch against.
func AdmitDeliveryTarget(ctx context.Context, git GitRunner, gc GitContext, base, worktreeBaseCommit string) (originURL, targetOriginCommit string, err error) {
	out, err := git.Run(ctx, gc, "remote", "get-url", "origin")
	if err != nil {
		return "", "", fmt.Errorf("workflow requires delivery but the repository has no origin remote")
	}
	originURL = strings.TrimSpace(out)

	if _, err := git.Run(ctx, gc, "fetch", "--no-tags", originURL,
		"+refs/heads/"+base+":refs/remotes/origin/"+base); err != nil {
		if strings.Contains(err.Error(), "couldn't find remote ref") {
			return "", "", fmt.Errorf("delivery base %q does not exist on the admitted remote", base)
		}
		return "", "", fmt.Errorf("cannot fetch delivery base %q: %w", base, err)
	}
	tip, err := git.Run(ctx, gc, "rev-parse", "--verify", "--end-of-options", "refs/remotes/origin/"+base+"^{commit}")
	if err != nil {
		return "", "", fmt.Errorf("cannot resolve fetched delivery base %q: %w", base, err)
	}
	targetOriginCommit = strings.TrimSpace(tip)

	// The first containment test is the ADMITTING check: exit 0 admits via
	// the fetched origin tip. A real git failure (exit 128, a missing or
	// corrupt object) fails closed - it must not silently fall into the
	// local-ref fallback, which exists only for the ordinary "committed
	// locally, not yet pushed" state.
	if contains, err := mergeBaseIsAncestor(ctx, git, gc, worktreeBaseCommit, targetOriginCommit); err != nil {
		return "", "", fmt.Errorf("cannot verify delivery base %q ancestry: %w", base, err)
	} else if contains {
		return originURL, targetOriginCommit, nil
	}
	if local, lerr := git.Run(ctx, gc, "rev-parse", "--verify", "--end-of-options", "refs/heads/"+base+"^{commit}"); lerr == nil {
		localTip := strings.TrimSpace(local)
		// The fallback covers exactly one state: the local target is strictly
		// AHEAD of the fetched origin tip. Requiring the origin tip to be an
		// ancestor of the local tip refuses DIVERGENCE (a rebased local
		// branch, or a target rewritten on origin while this clone was
		// stale): admitting a diverged state would record a pin unrelated to
		// the worktree base commit, and delivery-time rewrite detection
		// compares that pin against a later fetch of the same history, so the
		// rewrite would pass undetected and the PR would re-publish commits
		// the remote dropped. A merge-base git failure here also fails
		// closed: the fallback cannot admit what it cannot verify.
		if ahead, aerr := mergeBaseIsAncestor(ctx, git, gc, targetOriginCommit, localTip); aerr != nil {
			return "", "", fmt.Errorf("cannot verify local delivery base %q ancestry: %w", base, aerr)
		} else if ahead {
			if contains, cerr := mergeBaseIsAncestor(ctx, git, gc, worktreeBaseCommit, localTip); cerr != nil {
				return "", "", fmt.Errorf("cannot verify local delivery base %q ancestry: %w", base, cerr)
			} else if contains {
				return originURL, targetOriginCommit, nil
			}
		}
	}
	return "", "", fmt.Errorf(
		"delivery base %q does not contain the commit this run started from (%s); "+
			"start the run from a commit on %q, push %q, or point the workflow's delivery base at a branch that contains it",
		base, worktreeBaseCommit, base, base)
}
