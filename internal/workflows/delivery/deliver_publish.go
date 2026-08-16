package delivery

import (
	"context"
	"fmt"
	"time"

	ledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// maxPushAttempts bounds how many times a failed branch push is retried
// before the attempt is recorded failed. A push that dies mid-hook (for
// example the repo's pre-push verify gate under memory pressure) is a
// transport-class failure: the branch state on origin is unchanged, so a
// bounded retry is idempotent and can succeed once the load drops. A
// permanent refusal never reaches this loop (refusals return before commit).
const maxPushAttempts = 3

// pushRetryDelay returns the backoff for retry attempt n (1-based). It is a
// package variable so tests can shorten the wait.
var pushRetryDelay = func(attempt int) time.Duration {
	if attempt <= 1 {
		return 2 * time.Second
	}
	return 10 * time.Second
}

// waitCtx sleeps for d or returns ctx.Err() when the context ends first.
func waitCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// pushDeliveryBranch pushes the delivery commit to origin and records the
// push durably. CommitSHA is the adopted head; TreeSHA is the recorded tree;
// DiffRef is this attempt's deterministic diff snapshot. A known PR identity
// from a previous attempt is carried over so the durable pushed record never
// ERASES it.
func pushDeliveryBranch(ctx context.Context, repo ledger.Repository, git GitRunner, req Request, key, head, treeSHA, diffRef string, existing ledger.DeliveryRecord) error {
	// 12. Push the branch to origin, retrying transport-class failures with a
	// bounded backoff so a transient kill (OOM, network) does not strand the
	// run at delivery_pending with no automatic recovery.
	req.stage("push", fmt.Sprintf("push branch %s to origin", req.Branch))
	var err error
	for attempt := 1; attempt <= maxPushAttempts; attempt++ {
		if _, err = git.Run(ctx, req.GitCtx, "-c", "core.fsmonitor=false",
			"push", "origin", "HEAD:refs/heads/"+req.Branch); err == nil {
			break
		}
		req.stage("push_retry", fmt.Sprintf("push attempt %d/%d failed: %v", attempt, maxPushAttempts, err))
		if attempt == maxPushAttempts {
			break
		}
		if werr := waitCtx(ctx, pushRetryDelay(attempt)); werr != nil {
			return werr
		}
	}
	if err != nil {
		// The inventory MUST lead the recorded text: markFailed stores at
		// most maxErrorBytes and truncates from the end, so a long hook
		// rejection must not push the diagnostic out of the repair hint.
		// The two-tree divergence (delivered commit vs worktree, e.g. after
		// the automatic split deferred a file's tests) is exactly the class
		// of failure a repair agent cannot see in the hook output alone.
		wrapped := fmt.Errorf("pre-push hook rejection: %w", err)
		if hint := deliveryInventoryHint(ctx, git, req, existing); hint != "" {
			wrapped = fmt.Errorf("delivery commit inventory:\n%s\n\n%s", hint, wrapped)
		}
		markFailed(ctx, repo, key, req, wrapped)
		req.stage("failed", wrapped.Error())
		return wrapped
	}

	// 13. Record the push.
	rec := deliveryRecord(req, key, "pushed")
	rec.CommitSHA = head
	rec.DiffRef = diffRef
	rec.TreeSHA = treeSHA
	rec.RemoteID = existing.RemoteID
	rec.URL = existing.URL
	if err := repo.UpsertDelivery(ctx, rec); err != nil {
		return err
	}
	return nil
}

// findOrCreateAndVerifyPR finds or creates the run's PR and closes the AR-7
// TOCTOU: the PR's actual base must still contain the admitted origin base
// commit. It returns the PR identity for the durable records. title and body
// are the pre-validated PR metadata from the delivery request.
func findOrCreateAndVerifyPR(ctx context.Context, repo ledger.Repository, git GitRunner, pr PRClient, req Request, key, repoSlug, originBase string, existing ledger.DeliveryRecord, title, body string) (string, string, error) {
	// 14-15. Find or create the PR (ownership-aware reuse).
	req.stage("pr", "find or create the pull request")
	ref, err := findOrCreatePR(ctx, repo, pr, req, key, repoSlug, existing, title, body)
	if err != nil {
		return "", "", err
	}
	remoteID, url := ref.RemoteID, ref.URL

	// 15a. AR-7: the PR's base must still contain the admitted origin base; a
	// remote rewrite in the eligibility-to-create window must not settle
	// succeeded. Skipped when the client reports no base OID.
	if ref.BaseRefOID != "" {
		if aerr := baseStillContains(ctx, git, req, originBase, ref.BaseRefOID); aerr != nil {
			rerr := &RefusalError{Reason: fmt.Sprintf("PR %s base %s does not contain the admitted base commit %s (%v)", ref.RemoteID, ref.BaseRefOID, originBase, aerr)}
			markFailed(ctx, repo, key, req, rerr)
			req.stage("failed", rerr.Error())
			return "", "", rerr
		}
	}
	return remoteID, url, nil
}
