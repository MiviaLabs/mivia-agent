package delivery

// Stack auto-merge (stacking merge_policy=auto): MergePullRequest drives one
// published PR to merged so a multi-chunk stack needs zero human merges. The
// flow is: mark a draft PR ready (drafts cannot merge and usually skip CI),
// attempt the squash-merge, and if the host refuses (pending/red checks,
// review requirements, conflict) wait a bounded check cycle and retry once.
// The caller keeps polling git merge state; the moment the PR merges by any
// means (this merge, the merge queue, or a human) progress continues.

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ghRun executes one gh subprocess with fixed argv. The production value is
// runGH; tests override it to script merge flows without a real host.
var ghRun = runGH

// MergePullRequest merges the PR identified by number in repo (owner/repo).
// draft=true marks the PR ready for review first. It returns nil once the
// merge command succeeded (the PR is merged now, or enqueued on a merge
// queue); a non-nil error means the PR is not mergeable yet and the caller
// should retry later.
func MergePullRequest(ctx context.Context, repo, number string, draft bool) error {
	if draft {
		if _, err := ghRun(ctx, "pr ready", "pr", "ready", number, "--repo", repo); err != nil {
			return fmt.Errorf("mark PR %s ready: %w", number, err)
		}
	}
	if err := mergeAttempt(ctx, repo, number); err == nil {
		return nil
	}
	// The host refused the merge: wait a bounded check cycle so pending CI
	// settles (green or red), then retry once. The caller polls again later.
	if err := waitPRChecks(ctx, repo, number); err != nil {
		return err
	}
	return mergeAttempt(ctx, repo, number)
}

// mergeAttempt squash-merges the PR with branch deletion. On a merge-queue
// repository gh enqueues instead of merging; both are success.
func mergeAttempt(ctx context.Context, repo, number string) error {
	if _, err := ghRun(ctx, "pr merge", "pr", "merge", number, "--repo", repo, "--squash", "--delete-branch"); err != nil {
		return fmt.Errorf("merge PR %s: %w", number, err)
	}
	return nil
}

// waitPRChecks blocks until the PR's checks settle (all green or any red),
// bounded by mergeCheckWatchTimeout. A repository with no CI reports "no
// checks" and is treated as green: there is nothing to wait for.
func waitPRChecks(ctx context.Context, repo, number string) error {
	const mergeCheckWatchTimeout = 45 * time.Minute
	ctx, cancel := context.WithTimeout(ctx, mergeCheckWatchTimeout)
	defer cancel()
	out, err := ghRun(ctx, "pr checks", "pr", "checks", number, "--repo", repo, "--watch")
	if err != nil && strings.Contains(string(out), "no checks reported") {
		return nil
	}
	return err
}
