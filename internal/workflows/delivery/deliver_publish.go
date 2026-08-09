package delivery

import (
	"context"
	"fmt"

	ledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// pushDeliveryBranch pushes the delivery commit to origin and records the
// push durably. CommitSHA is the adopted head; TreeSHA is the recorded tree;
// DiffRef is this attempt's deterministic diff snapshot. A known PR identity
// from a previous attempt is carried over so the durable pushed record never
// ERASES it.
func pushDeliveryBranch(ctx context.Context, repo ledger.Repository, git GitRunner, req Request, key, head, treeSHA, diffRef string, existing ledger.DeliveryRecord) error {
	// 12. Push the branch to origin.
	req.stage("push", fmt.Sprintf("push branch %s to origin", req.Branch))
	if _, err := git.Run(ctx, req.GitCtx, "-c", "core.fsmonitor=false",
		"push", "origin", "HEAD:refs/heads/"+req.Branch); err != nil {
		markFailed(ctx, repo, key, req, err)
		req.stage("failed", err.Error())
		return err
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
