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
	// Stage is an optional observability callback. Deliver invokes it once
	// per numbered delivery stage with a stable stage name ("guard",
	// "eligibility", "no_diff", "commit", "push", "pr", "success",
	// "failed") and a free-form detail. It is nil-safe: a nil Stage is a
	// silent no-op. The CLI prints these lines to stderr. The session engine
	// publishes them to the events bus when a bus is wired.
	Stage func(stage, detail string)
}

// Result is the durable outcome of a delivery attempt.
type Result struct {
	Mode, BaseRef, HeadRef, CommitSHA, Provider, RemoteID, URL, Status, DiffRef string
}

// maxDiffBytes bounds one stored diff snapshot.
const maxDiffBytes = 64 << 10

// stage emits one delivery stage observation. It is nil-safe: a host without
// a Stage callback observes nothing.
func (req Request) stage(name, detail string) {
	if req.Stage != nil {
		req.Stage(name, detail)
	}
}

// Deliver performs one delivery attempt for a delivery_pending run. Refusals
// (RefusalError) are permanent and are only returned BEFORE this attempt
// writes any delivery record: eligibility failures and retry-path
// verification failures never touch the existing record, so a refusal can
// never destroy the prior attempt's CommitSHA/TreeSHA resume data. Failures
// once the attempt is in flight (after the stage record) are plain errors
// with the record marked failed, so the run stays delivery_pending for a
// retry.
func Deliver(ctx context.Context, repo ledger.Repository, git GitRunner, pr PRClient, req Request) (Result, error) {
	// 0. Stage observability begins at the entry guard.
	req.stage("guard", fmt.Sprintf("delivering run %s", req.RunID))

	// 1. Only a delivery_pending run may be delivered; a delivery_failed run
	// is re-opened for re-eligibility via the single enforcing CAS.
	run, err := deliveryRunGuard(ctx, repo, req)
	if err != nil {
		return Result{}, err
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
	req.stage("eligibility", "verify the pinned worktree and intended diff")
	originBase := run.OriginBaseCommit
	if originBase == "" {
		originBase = req.BaseCommit
	}
	head, porcelainEmpty, diffRef, repoSlug, err := verifyEligibility(ctx, repo, git, req, key, originBase)
	if err != nil {
		return Result{}, err
	}
	if diffRef == "" {
		req.stage("no_diff", "no diff to publish")
		return Result{
			Mode: req.Policy.Mode, BaseRef: req.Policy.Base, HeadRef: req.Branch,
			Provider: "github", Status: "no_diff",
		}, nil
	}

	// 10b. Optional workspace commit-message policy: validate the rendered
	// subject ONLY when a commit will actually be created (a diff exists, so
	// the repo's commit-msg hook would fire).
	if err := validateDeliveryCommitMessage(req); err != nil {
		return Result{}, err
	}

	// 11. Commit the intended change (fresh attempt) or resume the recorded
	// delivery commit (retry). All retry-path verification happens BEFORE
	// any record write in this attempt.
	req.stage("commit", "commit the intended change or resume the delivery commit")
	head, treeSHA, err := commitOrResume(ctx, repo, git, req, key, existing, head, porcelainEmpty, diffRef)
	if err != nil {
		return Result{}, err
	}

	// 12-16. Push the branch, then find or create one PR. The admitted base
	// pin travels with the publish so the post-create base check (AR-7) can
	// verify the PR's base still contains the admitted commit.
	return pushAndPublish(ctx, repo, git, pr, req, key, repoSlug, head, treeSHA, diffRef, originBase, existing)
}

// deliveryRunGuard enforces the entry status: only a delivery_pending run may
// be delivered, and a delivery_failed run is re-opened for re-eligibility via
// the single enforcing CAS (promoteToDeliveryPending), so every entry path
// (delivery.Deliver, engine.Deliver, CLI deliverRunWithStore) shares one
// recovery transition and cannot diverge. A CAS failure means another
// deliverer raced us; the attempt is a recoverable error.
func deliveryRunGuard(ctx context.Context, repo ledger.Repository, req Request) (ledger.RunSnapshot, error) {
	run, err := repo.GetRun(ctx, req.RunID)
	if err != nil {
		return ledger.RunSnapshot{}, err
	}
	if run.Status == ledger.RunStatusDeliveryFailed {
		run, err = promoteToDeliveryPending(ctx, repo, req.RunID, run)
		if err != nil {
			return ledger.RunSnapshot{}, err
		}
	}
	if run.Status != ledger.RunStatusDeliveryPending {
		return ledger.RunSnapshot{}, &RefusalError{Reason: fmt.Sprintf("delivery requires delivery_pending status, run is %q", run.Status)}
	}
	return run, nil
}

// validateDeliveryCommitMessage checks the optional workspace commit-message
// policy. It runs only when a commit will actually be created (a diff exists,
// so the repo's commit-msg hook would fire): a no_diff run never fires the
// hook, so a present policy must not refuse it. The check stays BEFORE
// commitOrResume, so a subject the hook would reject mid-flight becomes a
// permanent refusal instead of a delivery_pending retry loop; a refusal here
// writes no delivery record, commits nothing, and pushes nothing.
func validateDeliveryCommitMessage(req Request) error {
	return req.Policy.ValidateCommitMessage(req.GitCtx.Dir, req.Inputs)
}

// promoteToDeliveryPending is the single enforcing re-eligibility transition
// for a delivery_failed run (F-4): it CASes the run back to delivery_pending
// so every entry path shares one recovery edge and cannot diverge. A CAS
// failure means another deliverer raced us; the attempt is a recoverable
// error.
func promoteToDeliveryPending(ctx context.Context, repo ledger.Repository, runID string, run ledger.RunSnapshot) (ledger.RunSnapshot, error) {
	if err := repo.CompareAndSetRunStatus(ctx, runID, run.Version, ledger.RunStatusDeliveryPending, nil); err != nil {
		return ledger.RunSnapshot{}, fmt.Errorf("delivery re-eligibility: promote %s to delivery_pending: %w", run.Status, err)
	}
	return repo.GetRun(ctx, runID)
}

// verifyRemoteBaseAncestry refreshes the remote base from the ADMITTED URL
// (never the mutable `origin` name) and verifies it still CONTAINS the
// admitted origin base commit (originBase: OriginBaseCommit at admission,
// falling back to the admitted local BaseCommit). Forward advancement is a
// normal condition (Dependabot/Renovate behavior): the PR is created against
// the current base and GitHub computes the diff. Only a REWRITE that drops
// the admitted commit (or a deleted base branch) is a permanent refusal;
// transport failures are recoverable and keep the run delivery_pending.
func verifyRemoteBaseAncestry(ctx context.Context, git GitRunner, req Request, originBase string) error {
	// Unconditional single-ref fetch: staleness is undetectable without
	// fetching, and a rewritten base would otherwise leave the tracking ref
	// at the old tip and pass the ancestry check. The refspec is
	// force-updating on purpose; the rewrite is detected by ancestry.
	if _, err := git.Run(ctx, req.GitCtx, "fetch", "--no-tags", req.OriginURL,
		"+refs/heads/"+req.Policy.Base+":refs/remotes/origin/"+req.Policy.Base); err != nil {
		if strings.Contains(err.Error(), "couldn't find remote ref") {
			return &RefusalError{Reason: fmt.Sprintf("remote delivery base %q does not exist on the admitted remote", req.Policy.Base)}
		}
		return fmt.Errorf("cannot fetch remote delivery base %q (fetch and retry): %w", req.Policy.Base, err)
	}
	out, err := git.Run(ctx, req.GitCtx, "rev-parse", "--verify", "--end-of-options", "refs/remotes/origin/"+req.Policy.Base+"^{commit}")
	if err != nil {
		return fmt.Errorf("cannot resolve fetched remote delivery base %q: %w", req.Policy.Base, err)
	}
	fetchedBase := strings.TrimSpace(out)
	if _, err := git.Run(ctx, req.GitCtx, "merge-base", "--is-ancestor", originBase, fetchedBase); err != nil {
		return &RefusalError{Reason: fmt.Sprintf("remote delivery base %q was rewritten since admission: it no longer contains the admitted commit %s", req.Policy.Base, originBase)}
	}
	return nil
}
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

	// 6. (removed) The local refs/heads/<base> equality check was invalid in
	// linked worktrees: workflow runs share refs/heads/* with the main repo,
	// whose live base branch legitimately advances while a run is in flight.
	// The real invariant is the REMOTE base, checked at 6b below.

	// 7. The origin remote must match the admitted URL. This runs BEFORE the
	// fetch so a fetch never contacts a changed origin.
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

	// 7a+6b. Refresh the remote base from the ADMITTED URL and verify it
	// still contains the admitted origin base commit (ancestry, not equality).
	if err := verifyRemoteBaseAncestry(ctx, git, req, originBase); err != nil {
		return "", false, "", "", err
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

// baseStillContains reports whether the pull request's current base commit
// still contains the base commit this run was admitted against. It returns nil
// when the base only moved forward, and an error that names the cause when the
// base was rewritten.
//
// The check needs the pull request's base commit in the local object store.
// The base moves forward whenever another change merges while this run works,
// which is normal and must not stop delivery. The worktree does not hold that
// new commit, so `git merge-base` fails with "not a valid object" and the old
// code read that failure as a rewrite. A run then refused to publish because
// somebody else merged first.
//
// So: try the ancestry test, fetch the base when the object is missing, then
// try once more. Only a second failure is a true rewrite.
func baseStillContains(ctx context.Context, git GitRunner, req Request, admittedBase, prBase string) error {
	if _, err := git.Run(ctx, req.GitCtx, "merge-base", "--is-ancestor", admittedBase, prBase); err == nil {
		return nil
	}
	// The object may simply be absent. Fetch the base branch, then retest.
	if _, ferr := git.Run(ctx, req.GitCtx, "-c", "core.fsmonitor=false",
		"fetch", "--no-tags", "origin", req.Policy.Base); ferr != nil {
		return fmt.Errorf("fetch base %q to test ancestry: %w", req.Policy.Base, ferr)
	}
	if _, err := git.Run(ctx, req.GitCtx, "merge-base", "--is-ancestor", admittedBase, prBase); err != nil {
		return fmt.Errorf("remote base rewritten during delivery: %w", err)
	}
	return nil
}

// pushAndPublish pushes the delivery commit and finds or creates exactly one
// PR, recording each stage durably. originBase is the admitted remote base
// pin used by the post-create base verification (AR-7).
func pushAndPublish(ctx context.Context, repo ledger.Repository, git GitRunner, pr PRClient, req Request, key, repoSlug, head, treeSHA, diffRef, originBase string, existing ledger.DeliveryRecord) (Result, error) {
	// 12-13. Push the branch to origin and record the push.
	if err := pushDeliveryBranch(ctx, repo, git, req, key, head, treeSHA, diffRef, existing); err != nil {
		return Result{}, err
	}

	// 14-15a. Find or create the PR and verify the admitted base.
	remoteID, url, err := findOrCreateAndVerifyPR(ctx, repo, git, pr, req, key, repoSlug, originBase, existing)
	if err != nil {
		return Result{}, err
	}

	// 15b. Persist the PR identity in a pushed record when it is newly
	// learned, so a retry after a crash in the PR window can prove ownership
	// of the branch's PR (reuse regardless of draft state) instead of
	// misjudging it as foreign. Skipped when the existing record already
	// carries this PR (the retry absorbs the plain pushed record).
	if existing.RemoteID != remoteID {
		rec := deliveryRecord(req, key, "pushed")
		rec.CommitSHA = head
		rec.DiffRef = diffRef
		rec.TreeSHA = treeSHA
		rec.RemoteID = remoteID
		rec.URL = url
		if err := repo.UpsertDelivery(ctx, rec); err != nil {
			markFailed(ctx, repo, key, req, err)
			req.stage("failed", err.Error())
			return Result{}, err
		}
	}

	// 16. Record success and return the durable outcome.
	rec := deliveryRecord(req, key, "succeeded")
	rec.CommitSHA = head
	rec.DiffRef = diffRef
	rec.TreeSHA = treeSHA
	rec.RemoteID = remoteID
	rec.URL = url
	if err := repo.UpsertDelivery(ctx, rec); err != nil {
		return Result{}, err
	}
	req.stage("success", fmt.Sprintf("PR %s %s", remoteID, url))
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
func findOrCreatePR(ctx context.Context, repo ledger.Repository, pr PRClient, req Request, key, repoSlug string, existing ledger.DeliveryRecord) (*PRRef, error) {
	title, err := req.Policy.RenderTitle(req.Inputs)
	if err != nil {
		markFailed(ctx, repo, key, req, err)
		req.stage("failed", err.Error())
		return nil, err
	}
	body := "Automated workflow delivery from Mivia.\n\nRun: " + req.RunID + "\nWorkflow digest: " + req.WorkflowDigest

	found, err := pr.FindByHead(ctx, repoSlug, req.Branch)
	if err != nil {
		markFailed(ctx, repo, key, req, err)
		req.stage("failed", err.Error())
		return nil, err
	}
	if found == nil {
		created, cerr := pr.Create(ctx, repoSlug, PRInput{
			Base: req.Policy.Base, Head: req.Branch,
			Title: title, Body: body, Draft: req.Policy.Mode == "draft",
		})
		if cerr != nil {
			markFailed(ctx, repo, key, req, cerr)
			req.stage("failed", cerr.Error())
			return nil, cerr
		}
		return &created, nil
	}
	wantDraft := req.Policy.Mode == "draft"
	switch {
	case existing.RemoteID != "" && existing.RemoteID == found.RemoteID:
		// Our own PR from a previous attempt: reuse regardless of draft.
		return found, nil
	case found.Draft != wantDraft:
		rerr := &RefusalError{Reason: fmt.Sprintf("existing PR %s draft state does not match delivery mode %q and is not this run's PR", found.RemoteID, req.Policy.Mode)}
		markFailed(ctx, repo, key, req, rerr)
		req.stage("failed", rerr.Error())
		return nil, rerr
	default:
		return found, nil
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
