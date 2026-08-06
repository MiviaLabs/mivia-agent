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

	// 3. Idempotency: a previously succeeded/no_diff attempt replays its
	// durable result. Any other status is a resumable attempt.
	key := DeliveryKey(req.RunID, req.WorkflowDigest)
	existing, err := repo.GetDeliveryByIdempotencyKey(ctx, key)
	if err != nil && !errors.Is(err, ledger.ErrNotFound) {
		return Result{}, err
	}
	if err == nil && (existing.Status == "succeeded" || existing.Status == "no_diff") {
		return replayResult(existing), nil
	}

	// 4-9. Verify the pinned worktree and snapshot the intended diff. An
	// empty intended diff settles as no_diff with no PR and no commit.
	head, porcelainEmpty, diffRef, repoSlug, err := verifyEligibility(ctx, repo, git, req, key)
	if err != nil {
		return Result{}, err
	}
	if diffRef == "" {
		return Result{
			Mode: req.Policy.Mode, BaseRef: req.Policy.Base, HeadRef: req.Branch,
			Provider: "github", Status: "no_diff",
		}, nil
	}

	// 11. Commit the intended change (fresh attempt) or resume the recorded
	// delivery commit (retry). All retry-path verification happens BEFORE
	// any record write in this attempt.
	head, treeSHA, err := commitOrResume(ctx, repo, git, req, key, existing, head, porcelainEmpty, diffRef)
	if err != nil {
		return Result{}, err
	}

	// 12-16. Push the branch, then find or create one PR.
	return pushAndPublish(ctx, repo, git, pr, req, key, repoSlug, head, treeSHA, diffRef)
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
func verifyEligibility(ctx context.Context, repo ledger.Repository, git GitRunner, req Request, key string) (head string, porcelainEmpty bool, diffRef, repoSlug string, err error) {
	// 4. The worktree must be on the admitted branch.
	out, err := git.Run(ctx, req.GitCtx, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", false, "", "", &RefusalError{Reason: "cannot resolve worktree HEAD branch: " + err.Error()}
	}
	if branch := strings.TrimSpace(out); branch != req.Branch {
		return "", false, "", "", &RefusalError{Reason: fmt.Sprintf("worktree HEAD is on branch %q, delivery requires %q", branch, req.Branch)}
	}

	// 5. The admitted base commit must be an ancestor of HEAD.
	if _, err := git.Run(ctx, req.GitCtx, "merge-base", "--is-ancestor", req.BaseCommit, "HEAD"); err != nil {
		return "", false, "", "", &RefusalError{Reason: fmt.Sprintf("base commit %s is not an ancestor of HEAD", req.BaseCommit)}
	}

	// 6. The base target must still point at the admitted commit.
	out, err = git.Run(ctx, req.GitCtx, "rev-parse", "--verify", "--end-of-options", "refs/heads/"+req.Policy.Base+"^{commit}")
	if err != nil || strings.TrimSpace(out) != req.BaseCommit {
		return "", false, "", "", &RefusalError{Reason: fmt.Sprintf("delivery base %q moved since admission", req.Policy.Base)}
	}

	// 7. The origin remote must match the admitted URL.
	out, err = git.Run(ctx, req.GitCtx, "remote", "get-url", "origin")
	if err != nil {
		return "", false, "", "", &RefusalError{Reason: "no origin remote: " + err.Error()}
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
		return "", false, "", "", false, &RefusalError{Reason: "cannot resolve HEAD: " + err.Error()}
	}
	head = strings.TrimSpace(out)
	porcelain, err = git.Run(ctx, req.GitCtx, "-c", "core.fsmonitor=false", "status", "--porcelain")
	if err != nil {
		return "", false, "", "", false, &RefusalError{Reason: "status --porcelain failed: " + err.Error()}
	}
	porcelainEmpty = strings.TrimSpace(porcelain) == ""
	if head != req.BaseCommit {
		committed, derr := git.Run(ctx, req.GitCtx, "diff", "--stat", req.BaseCommit+"..HEAD")
		if derr != nil {
			return "", false, "", "", false, &RefusalError{Reason: "git diff --stat failed: " + derr.Error()}
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
		return "", false, "", "", false, &RefusalError{Reason: "git diff --stat failed: " + derr.Error()}
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

// commitOrResume creates the delivery commit on a fresh attempt, or verifies
// and adopts the recorded delivery commit on a retry. The delivery commit is
// built deterministically: the tree is recorded with the diff ref BEFORE the
// branch ref moves, so a crash between the commit and the pushed record can
// be resumed by verifying HEAD against the recorded tree instead of refusing
// the worktree as foreign. It returns the adopted HEAD and tree SHA.
func commitOrResume(ctx context.Context, repo ledger.Repository, git GitRunner, req Request, key string, existing ledger.DeliveryRecord, head string, porcelainEmpty bool, diffRef string) (string, string, error) {
	treeSHA := existing.TreeSHA
	if head != req.BaseCommit {
		// Retry path: verify the record found at step 3 against HEAD BEFORE
		// any record write in this attempt. A refusal leaves the existing
		// record untouched; on success the existing record IS the stage state
		// and is NOT rewritten with a fresh pending upsert.
		switch {
		case existing.CommitSHA == head:
			// Already committed by a previous attempt; the worktree must be
			// exactly that commit (no edits on top).
			if !porcelainEmpty {
				return "", "", &RefusalError{Reason: "worktree has uncommitted changes on the delivery commit"}
			}
		case existing.CommitSHA == "" && existing.TreeSHA != "":
			// Crash between commit and the CommitSHA record: adopt HEAD only
			// if it is EXACTLY the recorded delivery commit — same tree, clean
			// worktree, and exactly one commit on top of base.
			headTree, terr := git.Run(ctx, req.GitCtx, "rev-parse", "HEAD^{tree}")
			if terr != nil || strings.TrimSpace(headTree) != existing.TreeSHA || !porcelainEmpty {
				return "", "", &RefusalError{Reason: "worktree has foreign commits or uncommitted changes"}
			}
			count, cerr := git.Run(ctx, req.GitCtx, "rev-list", "--count", req.BaseCommit+"..HEAD")
			if cerr != nil || strings.TrimSpace(count) != "1" {
				return "", "", &RefusalError{Reason: "worktree has foreign commits or uncommitted changes"}
			}
		default:
			return "", "", &RefusalError{Reason: "worktree has foreign commits or uncommitted changes"}
		}
		return head, treeSHA, nil
	}

	// Fresh: stage the intended change (idempotent on retry).
	if _, err := git.Run(ctx, req.GitCtx, "-c", "core.hooksPath=", "-c", "core.fsmonitor=false", "add", "-A"); err != nil {
		markFailed(ctx, repo, key, req, err)
		return "", "", err
	}
	// Snapshot the staged tree; the delivery commit will carry it.
	treeOut, err := git.Run(ctx, req.GitCtx, "write-tree")
	if err != nil {
		markFailed(ctx, repo, key, req, err)
		return "", "", err
	}
	treeSHA = strings.TrimSpace(treeOut)
	// Render the commit message.
	msg, err := req.Policy.RenderCommitMessage(req.Inputs)
	if err != nil {
		markFailed(ctx, repo, key, req, err)
		return "", "", err
	}
	// Record the pending attempt with the tree BEFORE creating the commit,
	// so the retry can resume by tree verification. This is the ONLY stage
	// record written on the fresh path.
	stage := deliveryRecord(req, key, "pending")
	stage.DiffRef = diffRef
	stage.TreeSHA = treeSHA
	if err := repo.UpsertDelivery(ctx, stage); err != nil {
		return "", "", err
	}
	// Create the commit object from the recorded tree.
	shaOut, err := git.Run(ctx, req.GitCtx, "-c", "core.hooksPath=", "-c", "core.fsmonitor=false",
		"-c", "user.name=mivia", "-c", "user.email=mivia@localhost",
		"commit-tree", treeSHA, "-p", req.BaseCommit, "-m", msg)
	if err != nil {
		markFailed(ctx, repo, key, req, err)
		return "", "", err
	}
	sha := strings.TrimSpace(shaOut)
	// Point the branch at the new commit (the only HEAD mutation).
	if _, err := git.Run(ctx, req.GitCtx, "update-ref", "refs/heads/"+req.Branch, sha); err != nil {
		markFailed(ctx, repo, key, req, err)
		return "", "", err
	}
	return sha, treeSHA, nil
}

// pushAndPublish pushes the delivery commit and finds or creates exactly one
// PR, recording each stage durably.
func pushAndPublish(ctx context.Context, repo ledger.Repository, git GitRunner, pr PRClient, req Request, key, repoSlug, head, treeSHA, diffRef string) (Result, error) {
	// 12. Push the branch to origin.
	if _, err := git.Run(ctx, req.GitCtx, "-c", "core.hooksPath=", "-c", "core.fsmonitor=false",
		"push", "origin", "HEAD:refs/heads/"+req.Branch); err != nil {
		markFailed(ctx, repo, key, req, err)
		return Result{}, err
	}

	// 13. Record the push. CommitSHA is the adopted head; TreeSHA is the
	// recorded tree; DiffRef is this attempt's deterministic diff snapshot.
	rec := deliveryRecord(req, key, "pushed")
	rec.CommitSHA = head
	rec.DiffRef = diffRef
	rec.TreeSHA = treeSHA
	if err := repo.UpsertDelivery(ctx, rec); err != nil {
		return Result{}, err
	}

	// 14. Render the PR title. The body is fixed host text.
	title, err := req.Policy.RenderTitle(req.Inputs)
	if err != nil {
		markFailed(ctx, repo, key, req, err)
		return Result{}, err
	}
	body := "Automated workflow delivery from Mivia.\n\nRun: " + req.RunID + "\nWorkflow digest: " + req.WorkflowDigest

	// 15. Find an existing PR for the branch, else create one.
	found, err := pr.FindByHead(ctx, repoSlug, req.Branch)
	if err != nil {
		markFailed(ctx, repo, key, req, err)
		return Result{}, err
	}
	var remoteID, url string
	if found != nil {
		remoteID, url = found.RemoteID, found.URL
	} else {
		created, cerr := pr.Create(ctx, repoSlug, PRInput{
			Base: req.Policy.Base, Head: req.Branch,
			Title: title, Body: body, Draft: req.Policy.Mode == "draft",
		})
		if cerr != nil {
			markFailed(ctx, repo, key, req, cerr)
			return Result{}, cerr
		}
		remoteID, url = created.RemoteID, created.URL
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
