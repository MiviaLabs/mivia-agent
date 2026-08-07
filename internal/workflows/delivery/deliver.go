// Package delivery runs delivery commands against pinned git contexts.
package delivery

import (
	"context"
	"errors"
	"fmt"
	"strings"

	ledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// RefusalError marks a permanent, non-retryable delivery refusal. The run may
// CAS to delivery_failed. Transient failures are plain errors and leave the
// run delivery_pending for a retry.
type RefusalError struct{ Reason string }

// Error implements error.
func (e *RefusalError) Error() string { return e.Reason }

// IsRefusal reports whether err is a RefusalError (possibly wrapped).
func IsRefusal(err error) bool {
	var re *RefusalError
	return errors.As(err, &re)
}

// Request is the host-verified delivery invocation for one run.
type Request struct {
	RunID          string
	WorkflowDigest string
	Policy         Policy
	Inputs         map[string]string
	BaseCommit     string
	Branch         string
	GitCtx         GitContext
	OriginURL      string
}

// Result is the durable outcome of a delivery attempt.
type Result struct {
	Mode, BaseRef, HeadRef, CommitSHA, Provider, RemoteID, URL, Status, DiffRef string
}

// maxDiffBytes bounds one stored diff snapshot.
const maxDiffBytes = 64 << 10

// Deliver performs one delivery attempt for a delivery_pending run. Refusals
// (RefusalError) are permanent and are only returned BEFORE this attempt
// writes any delivery record: eligibility failures and retry-path
// verification failures never touch the existing record, so a refusal can
// never destroy the prior attempt's CommitSHA/TreeSHA resume data. Failures
// once the attempt is in flight (after the stage record) are plain errors
// with the record marked failed, so the run stays delivery_pending for a
// retry.
func Deliver(ctx context.Context, repo ledger.Repository, git GitRunner, pr PRClient, req Request) (Result, error) {
	// 1. Only a delivery_pending run may be delivered.
	run, err := repo.GetRun(ctx, req.RunID)
	if err != nil {
		return Result{}, err
	}
	if run.Status != ledger.RunStatusDeliveryPending {
		return Result{}, &RefusalError{Reason: fmt.Sprintf("delivery requires delivery_pending status, run is %q", run.Status)}
	}

	// 2. Policy shape must be supported and active.
	if err := req.Policy.Validate(); err != nil {
		return Result{}, &RefusalError{Reason: err.Error()}
	}
	if req.Policy.Mode == "" || req.Policy.Mode == "none" {
		return Result{}, &RefusalError{Reason: fmt.Sprintf("delivery requires mode draft or ready, got %q", req.Policy.Mode)}
	}

	// 3. Idempotency: a previously succeeded attempt replays its durable
	// result. A no_diff verdict is point-in-time, so it is NOT replayed:
	// the current worktree is re-verified below, and newly appeared work is
	// published instead of letting a stale no_diff settle the run with zero
	// PRs. Any other status is a resumable attempt.
	key := DeliveryKey(req.RunID, req.WorkflowDigest)
	existing, err := repo.GetDeliveryByIdempotencyKey(ctx, key)
	if err != nil && !errors.Is(err, ledger.ErrNotFound) {
		return Result{}, err
	}
	if err == nil && existing.Status == "succeeded" {
		return replayResult(existing), nil
	}

	// 4-9. Verify the pinned worktree and snapshot the intended diff. An
	// empty intended diff settles as no_diff with no PR and no commit.
	// The remote base is verified against the origin base recorded at
	// admission (OriginBaseCommit) when present; otherwise the admitted
	// local BaseCommit is the pin.
	originBase := run.OriginBaseCommit
	if originBase == "" {
		originBase = req.BaseCommit
	}
	head, porcelainEmpty, diffRef, repoSlug, err := verifyEligibility(ctx, repo, git, req, key, originBase)
	if err != nil {
		return Result{}, err
	}
	if diffRef == "" {
		return Result{
			Mode: req.Policy.Mode, BaseRef: req.Policy.Base, HeadRef: req.Branch,
			Provider: "github", Status: "no_diff",
		}, nil
	}

	// 10b. Optional workspace commit-message policy: validate the rendered
	// subject ONLY when a commit will actually be created (a diff exists, so
	// the repo's commit-msg hook would fire). A no_diff run never fires the
	// hook, so a present policy must not refuse it. The check stays BEFORE
	// commitOrResume, so a subject the hook would reject mid-flight becomes
	// a permanent refusal instead of a delivery_pending retry loop; a refusal
	// here writes no delivery record, commits nothing, and pushes nothing.
	if err := req.Policy.ValidateCommitMessage(req.GitCtx.Dir, req.Inputs); err != nil {
		return Result{}, err
	}

	// 11. Commit the intended change (fresh attempt) or resume the recorded
	// delivery commit (retry). All retry-path verification happens BEFORE
	// any record write in this attempt.
	head, treeSHA, err := commitOrResume(ctx, repo, git, req, key, existing, head, porcelainEmpty, diffRef)
	if err != nil {
		return Result{}, err
	}

	// 12-16. Push the branch, then find or create one PR.
	return pushAndPublish(ctx, repo, git, pr, req, key, repoSlug, head, treeSHA, diffRef, existing)
}

// replayResult maps a durable succeeded/no_diff record back to a Result.
func replayResult(existing ledger.DeliveryRecord) Result {
	return Result{
		Mode: existing.Mode, BaseRef: existing.BaseRef, HeadRef: existing.HeadRef,
		CommitSHA: existing.CommitSHA, Provider: existing.Provider,
		RemoteID: existing.RemoteID, URL: existing.URL,
		Status: existing.Status, DiffRef: existing.DiffRef,
	}
}

// verifyEligibility runs the host-enforced checks against the pinned git
// context and snapshots the intended diff. On success it returns the HEAD
// commit, whether the worktree is clean, the content ref of the diff snapshot
// (empty when there is nothing to publish), and the PR client repo slug. The
// no-diff outcome writes the no_diff record here and returns an empty ref.
func verifyEligibility(ctx context.Context, repo ledger.Repository, git GitRunner, req Request, key, originBase string) (head string, porcelainEmpty bool, diffRef, repoSlug string, err error) {
	// 4. The worktree must be on the admitted branch.
	out, err := git.Run(ctx, req.GitCtx, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		// A git execution failure (hang, index lock, transient FS) is
		// recoverable; only conditions read from SUCCESSFUL git output are
		// permanent refusals. A plain error keeps the run delivery_pending.
		return "", false, "", "", fmt.Errorf("cannot resolve worktree HEAD branch: %w", err)
	}
	if branch := strings.TrimSpace(out); branch != req.Branch {
		return "", false, "", "", &RefusalError{Reason: fmt.Sprintf("worktree HEAD is on branch %q, delivery requires %q", branch, req.Branch)}
	}

	// 5. The admitted base commit must be an ancestor of HEAD.
	if _, err := git.Run(ctx, req.GitCtx, "merge-base", "--is-ancestor", req.BaseCommit, "HEAD"); err != nil {
		return "", false, "", "", fmt.Errorf("cannot verify base commit %s ancestry: %w", req.BaseCommit, err)
	}

	// 6. The base target must still point at the admitted commit.
	out, err = git.Run(ctx, req.GitCtx, "rev-parse", "--verify", "--end-of-options", "refs/heads/"+req.Policy.Base+"^{commit}")
	if err != nil {
		return "", false, "", "", fmt.Errorf("cannot resolve delivery base %q: %w", req.Policy.Base, err)
	}
	if strings.TrimSpace(out) != req.BaseCommit {
		return "", false, "", "", &RefusalError{Reason: fmt.Sprintf("delivery base %q moved since admission", req.Policy.Base)}
	}

	// 6b. The PR is created against the REMOTE base, so the origin's base
	// must still point at the commit recorded at admission: a locally pinned
	// base must not mask a remote base that diverged after admission.
	// originBase is the origin base commit recorded at admission
	// (OriginBaseCommit), falling back to the admitted local BaseCommit.
	out, err = git.Run(ctx, req.GitCtx, "rev-parse", "--verify", "--end-of-options", "refs/remotes/origin/"+req.Policy.Base+"^{commit}")
	if err != nil {
		// The remote-tracking ref is absent (never fetched after admission):
		// recoverable — fetching and retrying fixes it, so keep the run
		// delivery_pending instead of refusing.
		return "", false, "", "", fmt.Errorf("cannot resolve remote delivery base %q (fetch and retry): %w", req.Policy.Base, err)
	}
	if strings.TrimSpace(out) != originBase {
		return "", false, "", "", &RefusalError{Reason: fmt.Sprintf("remote delivery base %q diverged since admission", req.Policy.Base)}
	}

	// 7. The origin remote must match the admitted URL.
	out, err = git.Run(ctx, req.GitCtx, "remote", "get-url", "origin")
	if err != nil {
		return "", false, "", "", fmt.Errorf("cannot read origin remote: %w", err)
	}
	remoteURL := strings.TrimSpace(out)
	if normalizeURL(remoteURL) != normalizeURL(req.OriginURL) {
		return "", false, "", "", &RefusalError{Reason: "origin remote changed since admission"}
	}
	repoSlug, perr := ParseOwnerRepo(remoteURL)
	if perr != nil {
		// A local or non-github remote cannot be parsed to owner/repo; use
		// the normalized URL so the PR client still addresses the same remote.
		repoSlug = normalizeURL(remoteURL)
	}

	// 8-9. HEAD commit, intended diff, and the diff snapshot.
	head, porcelainEmpty, diffText, porcelain, noDiff, err := intendedDiff(ctx, repo, git, req, key)
	if err != nil {
		return "", false, "", "", err
	}
	if noDiff {
		return head, true, "", repoSlug, nil
	}
	text := boundText(diffText+"\n"+porcelain, maxDiffBytes, "diff truncated at 64 KiB")
	diffRef = "sha256:" + ledger.DigestHex([]byte(text))
	if err := repo.StoreContent(ctx, diffRef, []byte(text)); err != nil {
		return "", false, "", "", err
	}
	return head, porcelainEmpty, diffRef, repoSlug, nil
}

// intendedDiff resolves HEAD and the intended change against the admitted
// base: committed work (base..HEAD) plus uncommitted work (porcelain). An
// empty intended diff settles as no_diff (record written here) and reports
// noDiff=true. It returns the porcelain text for the diff snapshot too.
func intendedDiff(ctx context.Context, repo ledger.Repository, git GitRunner, req Request, key string) (head string, porcelainEmpty bool, diffText, porcelain string, noDiff bool, err error) {
	out, err := git.Run(ctx, req.GitCtx, "rev-parse", "HEAD")
	if err != nil {
		return "", false, "", "", false, fmt.Errorf("cannot resolve HEAD: %w", err)
	}
	head = strings.TrimSpace(out)
	porcelain, err = git.Run(ctx, req.GitCtx, "-c", "core.fsmonitor=false", "status", "--porcelain")
	if err != nil {
		return "", false, "", "", false, fmt.Errorf("status --porcelain failed: %w", err)
	}
	porcelainEmpty = strings.TrimSpace(porcelain) == ""
	if head != req.BaseCommit {
		committed, derr := git.Run(ctx, req.GitCtx, "diff", "--stat", req.BaseCommit+"..HEAD")
		if derr != nil {
			return "", false, "", "", false, fmt.Errorf("git diff --stat failed: %w", derr)
		}
		if committed == "" && porcelainEmpty {
			// Nothing to publish: no committed change and a clean worktree.
			if err := repo.UpsertDelivery(ctx, deliveryRecord(req, key, "no_diff")); err != nil {
				return "", false, "", "", false, err
			}
			return head, true, "", "", true, nil
		}
		return head, porcelainEmpty, committed, porcelain, false, nil
	}
	stat, derr := git.Run(ctx, req.GitCtx, "diff", "--no-ext-diff", "--no-textconv", "--stat", req.BaseCommit)
	if derr != nil {
		return "", false, "", "", false, fmt.Errorf("git diff --stat failed: %w", derr)
	}
	if porcelainEmpty {
		// Nothing to publish: a clean worktree at the base commit.
		if err := repo.UpsertDelivery(ctx, deliveryRecord(req, key, "no_diff")); err != nil {
			return "", false, "", "", false, err
		}
		return head, true, "", "", true, nil
	}
	return head, porcelainEmpty, stat, porcelain, false, nil
}

// pushAndPublish pushes the delivery commit and finds or creates exactly one
// PR, recording each stage durably.
func pushAndPublish(ctx context.Context, repo ledger.Repository, git GitRunner, pr PRClient, req Request, key, repoSlug, head, treeSHA, diffRef string, existing ledger.DeliveryRecord) (Result, error) {
	// 12. Push the branch to origin.
	if _, err := git.Run(ctx, req.GitCtx, "-c", "core.fsmonitor=false",
		"push", "origin", "HEAD:refs/heads/"+req.Branch); err != nil {
		markFailed(ctx, repo, key, req, err)
		return Result{}, err
	}

	// 13. Record the push. CommitSHA is the adopted head; TreeSHA is the
	// recorded tree; DiffRef is this attempt's deterministic diff snapshot.
	// A known PR identity from a previous attempt is carried over so the
	// durable pushed record never ERASES it (the projection is latest-wins;
	// an identity-less rewrite would make the next retry misjudge the run's
	// own PR as foreign).
	rec := deliveryRecord(req, key, "pushed")
	rec.CommitSHA = head
	rec.DiffRef = diffRef
	rec.TreeSHA = treeSHA
	rec.RemoteID = existing.RemoteID
	rec.URL = existing.URL
	if err := repo.UpsertDelivery(ctx, rec); err != nil {
		return Result{}, err
	}

	// 14-15. Find or create the PR (ownership-aware reuse).
	remoteID, url, err := findOrCreatePR(ctx, repo, pr, req, key, repoSlug, existing)
	if err != nil {
		return Result{}, err
	}

	// 15b. Persist the PR identity in a pushed record when it is newly
	// learned, so a retry after a crash in the PR window can prove ownership
	// of the branch's PR (reuse regardless of draft state) instead of
	// misjudging it as foreign. Skipped when the existing record already
	// carries this PR (the retry absorbs the plain pushed record).
	if existing.RemoteID != remoteID {
		rec = deliveryRecord(req, key, "pushed")
		rec.CommitSHA = head
		rec.DiffRef = diffRef
		rec.TreeSHA = treeSHA
		rec.RemoteID = remoteID
		rec.URL = url
		if err := repo.UpsertDelivery(ctx, rec); err != nil {
			markFailed(ctx, repo, key, req, err)
			return Result{}, err
		}
	}

	// 16. Record success and return the durable outcome.
	rec = deliveryRecord(req, key, "succeeded")
	rec.CommitSHA = head
	rec.DiffRef = diffRef
	rec.TreeSHA = treeSHA
	rec.RemoteID = remoteID
	rec.URL = url
	if err := repo.UpsertDelivery(ctx, rec); err != nil {
		return Result{}, err
	}
	return Result{
		Mode: req.Policy.Mode, BaseRef: req.Policy.Base, HeadRef: req.Branch,
		CommitSHA: head, Provider: "github", RemoteID: remoteID, URL: url,
		Status: "succeeded", DiffRef: diffRef,
	}, nil
}

// findOrCreatePR finds the branch's PR or creates one, then applies the
// ownership-aware reuse guard. Ownership is proven by the RemoteID persisted
// in a previous attempt's pushed record: the run's own PR is reused even if a
// reviewer flipped its draft state, while a genuinely foreign PR with the
// wrong draft state is a permanent condition and settles as a refusal instead
// of deadlocking the run in delivery_pending forever.
func findOrCreatePR(ctx context.Context, repo ledger.Repository, pr PRClient, req Request, key, repoSlug string, existing ledger.DeliveryRecord) (remoteID, url string, err error) {
	title, err := req.Policy.RenderTitle(req.Inputs)
	if err != nil {
		markFailed(ctx, repo, key, req, err)
		return "", "", err
	}
	body := "Automated workflow delivery from Mivia.\n\nRun: " + req.RunID + "\nWorkflow digest: " + req.WorkflowDigest

	found, err := pr.FindByHead(ctx, repoSlug, req.Branch)
	if err != nil {
		markFailed(ctx, repo, key, req, err)
		return "", "", err
	}
	if found == nil {
		created, cerr := pr.Create(ctx, repoSlug, PRInput{
			Base: req.Policy.Base, Head: req.Branch,
			Title: title, Body: body, Draft: req.Policy.Mode == "draft",
		})
		if cerr != nil {
			markFailed(ctx, repo, key, req, cerr)
			return "", "", cerr
		}
		return created.RemoteID, created.URL, nil
	}
	wantDraft := req.Policy.Mode == "draft"
	switch {
	case existing.RemoteID != "" && existing.RemoteID == found.RemoteID:
		// Our own PR from a previous attempt: reuse regardless of draft.
		return found.RemoteID, found.URL, nil
	case found.Draft != wantDraft:
		rerr := &RefusalError{Reason: fmt.Sprintf("existing PR %s draft state does not match delivery mode %q and is not this run's PR", found.RemoteID, req.Policy.Mode)}
		markFailed(ctx, repo, key, req, rerr)
		return "", "", rerr
	default:
		return found.RemoteID, found.URL, nil
	}
}

// deliveryRecord builds the caller-owned fields of the record for one
// delivery attempt. Status is set by the caller; CommitSHA/TreeSHA/DiffRef/
// RemoteID/URL/ErrorRef are added as the attempt progresses.
func deliveryRecord(req Request, key, status string) ledger.DeliveryRecord {
	return ledger.DeliveryRecord{
		RunID:          req.RunID,
		IdempotencyKey: key,
		Mode:           req.Policy.Mode,
		BaseRef:        req.Policy.Base,
		HeadRef:        req.Branch,
		Provider:       "github",
		Status:         status,
	}
}

// normalizeURL trims whitespace and a trailing .git for origin comparison.
func normalizeURL(url string) string {
	return strings.TrimSuffix(strings.TrimSpace(url), ".git")
}
