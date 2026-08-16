package delivery

import (
	"context"
	"fmt"
	"strings"

	ledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// deferredCommitMessage builds the host-only follow-up commit message for a
// split delivery: the SAME subject that just passed both the PR-title and
// commit-message policy checks (a host-invented subject is not guaranteed to
// satisfy an arbitrary workspace's commit-msg hook - wrong type/scope shape,
// too long), plus a body naming the deferred follow-up. This commit is never
// pushed on the delivered branch, so it never goes through
// validateDeliveryCommitSubject itself.
func deferredCommitMessage(title string, deferredCount int) string {
	return buildCommitMessage(title, fmt.Sprintf("deferred: %d file(s) split from this chunk's delivery (automatic follow-up)", deferredCount))
}

// freshDeliveryCommitSplit stages and commits everything EXCEPT deferred,
// exactly like freshDeliveryCommitSingle (same pending-record-before-commit
// shape, same return contract: HEAD is the delivered commit). It then stages
// and commits deferred as a SECOND commit, saves that commit under
// deferredBranchName (git branch -f), and resets the worktree back to the
// delivered commit - so deferred's content is preserved but never reachable
// from the branch that gets pushed next.
func freshDeliveryCommitSplit(ctx context.Context, repo ledger.Repository, git GitRunner, req Request, key, diffRef string, deferred []string, title, body string) (string, string, error) {
	if _, err := git.Run(ctx, req.GitCtx, "-c", "core.fsmonitor=false", "add", "-A"); err != nil {
		markFailed(ctx, repo, key, req, err)
		return "", "", err
	}
	// Guard: a deferred-file split must never separate a file from its test
	// companion - the repair agent declared this split, so refuse it before
	// any commit (see guardDeferredSplitConsistency).
	if err := guardDeferredSplitConsistency(ctx, git, req, deferred); err != nil {
		return "", "", err
	}
	resetArgs := append([]string{"reset", "--"}, deferred...)
	if _, err := git.Run(ctx, req.GitCtx, resetArgs...); err != nil {
		markFailed(ctx, repo, key, req, err)
		return "", "", fmt.Errorf("cannot unstage deferred_files for the split commit: %w", err)
	}
	treeOut, err := git.Run(ctx, req.GitCtx, "write-tree")
	if err != nil {
		markFailed(ctx, repo, key, req, err)
		return "", "", err
	}
	treeSHA := strings.TrimSpace(treeOut)
	bodyText, err := req.Policy.RenderCommitMessage(req.Inputs)
	if err != nil {
		markFailed(ctx, repo, key, req, err)
		return "", "", err
	}
	if strings.TrimSpace(bodyText) == "" {
		bodyText = body
	}
	msg := buildCommitMessage(title, bodyText)
	stage := deliveryRecord(req, key, "pending")
	stage.DiffRef = diffRef
	stage.TreeSHA = treeSHA
	// Persist the split decision on the pending record BEFORE C1 exists, so a
	// crash or transient failure after C1 can be resumed (resumeDeliveryCommitSplit)
	// instead of the retry committing or adopting the deferred scope onto the
	// pushed branch. The raw reserved-input value is the canonical JSON form.
	stage.DeferredFiles = req.Inputs[InputDeferredFiles]
	if err := repo.UpsertDelivery(ctx, stage); err != nil {
		return "", "", err
	}
	head, adoptedTree, err := commitStagedTree(ctx, repo, git, req, stage, treeSHA, msg)
	if err != nil {
		markFailed(ctx, repo, key, req, err)
		return "", "", err
	}

	// Second commit: deferred_files only, child of the delivered commit.
	if err := commitDeferredFollowUp(ctx, git, req, deferred, title, head); err != nil {
		markFailed(ctx, repo, key, req, err)
		return "", "", err
	}
	return head, adoptedTree, nil
}

// commitDeferredFollowUp commits the deferred files as a SECOND commit on the
// delivered branch, saves that commit under the deferred branch name, and
// resets the worktree back to the delivered commit - so deferred's content is
// preserved but never reachable from the branch that gets pushed next.
func commitDeferredFollowUp(ctx context.Context, git GitRunner, req Request, deferred []string, title, head string) error {
	addArgs := append([]string{"-c", "core.fsmonitor=false", "add", "--"}, deferred...)
	if _, err := git.Run(ctx, req.GitCtx, addArgs...); err != nil {
		return fmt.Errorf("cannot stage deferred_files for the follow-up commit: %w", err)
	}
	// Reuse the SAME subject that just passed both the PR-title and
	// commit-message policy checks above - a host-invented subject (for
	// example a hardcoded "deferred: N file(s)...") is not guaranteed to
	// satisfy an arbitrary workspace's commit-msg hook (wrong type/scope
	// shape, too long), and this host-only commit never goes through
	// validateDeliveryCommitSubject itself since it is never pushed on the
	// delivered branch. Reusing title sidesteps that entirely: it is
	// proven-valid, and per-workspace commit-msg hooks generally do not
	// reject a repeated subject.
	deferredMsg := deferredCommitMessage(title, len(deferred))
	if _, err := git.Run(ctx, req.GitCtx, "-c", "core.fsmonitor=false",
		"-c", "user.name="+mviaCommitAuthorName, "-c", "user.email="+mviaCommitAuthorEmail,
		"commit", "--allow-empty-message", "-m", deferredMsg); err != nil {
		return fmt.Errorf("cannot commit deferred_files: %w", err)
	}
	// Save the deferred commit under its own branch, then reset the current
	// worktree back to the delivered commit: the branch about to be pushed
	// must never carry deferred_files.
	branch := DeferredBranchName(req.Branch)
	if _, err := git.Run(ctx, req.GitCtx, "branch", "-f", branch, "HEAD"); err != nil {
		return fmt.Errorf("cannot save the deferred follow-up branch: %w", err)
	}
	if _, err := git.Run(ctx, req.GitCtx, "reset", "--hard", head); err != nil {
		return fmt.Errorf("cannot restore the worktree to the delivered commit: %w", err)
	}
	return nil
}
