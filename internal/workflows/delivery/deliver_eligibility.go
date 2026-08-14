package delivery

import (
	"context"
	"fmt"
	"strings"
)

// verifyWorktreeAndRemote runs steps 4-7b of eligibility: the worktree is on
// the admitted branch with the admitted base as an ancestor, the origin
// remote still matches admission, the remote base still contains the
// admitted origin base commit, and (for stacking workflows) this chunk has
// not drifted against a sibling that merged mid-flight. Split out of
// verifyEligibility (deliver.go) to keep that function under the file-size
// gate's function-length cap.
func verifyWorktreeAndRemote(ctx context.Context, git GitRunner, req Request, originBase string) (repoSlug string, err error) {
	// 4. The worktree must be on the admitted branch.
	out, err := git.Run(ctx, req.GitCtx, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		// A git execution failure (hang, index lock, transient FS) is
		// recoverable; only conditions read from SUCCESSFUL git output are
		// permanent refusals. A plain error keeps the run delivery_pending.
		return "", fmt.Errorf("cannot resolve worktree HEAD branch: %w", err)
	}
	if branch := strings.TrimSpace(out); branch != req.Branch {
		return "", &RefusalError{Reason: fmt.Sprintf("worktree HEAD is on branch %q, delivery requires %q", branch, req.Branch)}
	}

	// 5. The admitted base commit must be an ancestor of HEAD.
	if _, err := git.Run(ctx, req.GitCtx, "merge-base", "--is-ancestor", req.BaseCommit, "HEAD"); err != nil {
		return "", fmt.Errorf("cannot verify base commit %s ancestry: %w", req.BaseCommit, err)
	}

	// 6. (removed) The local refs/heads/<base> equality check was invalid in
	// linked worktrees: workflow runs share refs/heads/* with the main repo,
	// whose live base branch legitimately advances while a run is in flight.
	// The real invariant is the REMOTE base, checked at 6b below.

	// 7. The origin remote must match the admitted URL. This runs BEFORE the
	// fetch so a fetch never contacts a changed origin.
	out, err = git.Run(ctx, req.GitCtx, "remote", "get-url", "origin")
	if err != nil {
		return "", fmt.Errorf("cannot read origin remote: %w", err)
	}
	remoteURL := strings.TrimSpace(out)
	if normalizeURL(remoteURL) != normalizeURL(req.OriginURL) {
		return "", &RefusalError{Reason: "origin remote changed since admission"}
	}
	repoSlug, perr := ParseOwnerRepo(remoteURL)
	if perr != nil {
		// A local or non-github remote cannot be parsed to owner/repo; use
		// the normalized URL so the PR client still addresses the same remote.
		repoSlug = normalizeURL(remoteURL)
	}

	// 7a+6b. Refresh the remote base from the ADMITTED URL and verify it
	// still contains the admitted origin base commit (ancestry, not equality).
	if err := verifyRemoteBaseAncestry(ctx, git, req, originBase); err != nil {
		return "", err
	}
	// 7b. Overlap guard for stacked chunks (§checkChunkBaseDrift): refuses a
	// chunk whose diff has drifted against a sibling that merged mid-flight.
	if err := guardChunkBaseDrift(ctx, git, req, originBase); err != nil {
		return "", err
	}
	// 7c. Scope guard for stacked chunks (§guardChunkScope): refuses a chunk
	// whose diff touches files outside its declared decompose plan slice.
	if err := guardChunkScope(ctx, git, req); err != nil {
		return "", err
	}
	return repoSlug, nil
}
