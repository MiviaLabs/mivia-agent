package delivery

// Split-delivery crash resume (spec-auto-split-oversized-prs.md §5.2-5.3,
// revised per §10): freshDeliveryCommitSplit records the deferred-file list on
// the pending DeliveryRecord BEFORE creating the delivered commit C1, then
// performs C1 (delivered), C2 (deferred, saved under DeferredBranchName), and
// finally resets the worktree back to C1. A crash or transient failure can
// interrupt that sequence in one of four windows:
//
//   - window A: after C1, before C2 (record.CommitSHA == head == C1, deferred
//     files still untracked in the worktree);
//   - window B: after C2, before `git branch -f` (record.CommitSHA == C1,
//     head == C2, deferred branch missing or stale);
//   - window C: after `git branch -f`, before `git reset --hard C1`
//     (record.CommitSHA == C1, head == C2, deferred branch at C2);
//   - window D: after the final reset, before the success record
//     (record.CommitSHA == head == C1, worktree clean, deferred branch at C2).
//
// The generic retry paths mis-handle every one of these: commitWorktreeFollowUp
// would fold the deferred scope into the delivered branch (bypassing the size
// gate), and adoptOwnFollowUpCommit would adopt the deferred commit C2 as the
// delivery commit. resumeDeliveryCommitSplit is the dedicated recovery: it
// re-executes C2 (window A), (re)creates the deferred branch at C2 and resets
// the worktree back to C1 (windows B/C), or verifies the final state (window
// D). A RefusalError is returned only when the state is unrecoverable
// (foreign commits, a missing recorded delivered commit, or deferred files
// that can no longer be reconstructed).

import (
	"context"
	"fmt"
	"strings"

	ledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// resumeDeliveryCommitSplit recovers a deferred-file split (record
// DeferredFiles != "") that a previous attempt left mid-sequence. It never
// commits or adopts anything onto the delivered branch: the delivered commit
// C1 already exists (or is adopted by verification when the CommitSHA record
// write itself crashed), so the only work left is the deferred half. It
// returns the delivered commit and its recorded tree, which is what gets
// pushed next.
func resumeDeliveryCommitSplit(ctx context.Context, repo ledger.Repository, git GitRunner, req Request, existing ledger.DeliveryRecord, head string, porcelainEmpty bool, diffRef, title, body string) (string, string, error) {
	deferred, err := ParseDeferredFiles(existing.DeferredFiles)
	if err != nil {
		return "", "", &RefusalError{Reason: fmt.Sprintf("cannot resume the deferred-file split: the recorded deferred_files value is invalid: %v", err)}
	}
	if len(deferred) == 0 {
		return "", "", &RefusalError{Reason: "cannot resume the deferred-file split: the recorded deferred_files list is empty"}
	}
	branch := DeferredBranchName(req.Branch)
	c1 := existing.CommitSHA
	c1Tree := existing.TreeSHA

	// Crash between the C1 commit and commitStagedTree's CommitSHA re-upsert:
	// adopt HEAD as C1, then re-execute C2 exactly like window A.
	if c1 == "" {
		adoptedC1, adoptedTree, aerr := adoptRecordedC1Commit(ctx, repo, git, req, existing, head)
		if aerr != nil {
			return "", "", aerr
		}
		return reexecuteDeferredCommit(ctx, repo, git, req, deferred, branch, adoptedC1, adoptedTree, title, body)
	}

	// Window A: HEAD is the recorded delivered commit and the deferred files
	// are still untracked in the worktree - re-execute C2 on top of it.
	// Window D (worktree already clean): the split completed; the deferred
	// branch must still exist.
	if c1 == head {
		if porcelainEmpty {
			if !branchExists(ctx, git, req.GitCtx, branch) {
				return "", "", &RefusalError{Reason: "cannot resume the deferred-file split: the deferred follow-up branch is missing"}
			}
			return c1, c1Tree, nil
		}
		return reexecuteDeferredCommit(ctx, repo, git, req, deferred, branch, c1, c1Tree, title, body)
	}

	// Windows B/C: HEAD is the deferred commit C2 (parent C1). Verify it is
	// the run's own follow-up commit, then (re)create the deferred branch at
	// it and reset the worktree back to the delivered commit. Same trust
	// model as adoptOwnFollowUpCommit: no file-set check, because a hook may
	// legitimately have mutated the committed C2 tree.
	if !porcelainEmpty {
		return "", "", &RefusalError{Reason: "worktree has foreign commits or uncommitted changes"}
	}
	if err := verifyMiviaCommitOnTop(ctx, git, req.GitCtx, c1); err != nil {
		return "", "", err
	}
	if _, err := git.Run(ctx, req.GitCtx, "branch", "-f", branch, "HEAD"); err != nil {
		return "", "", fmt.Errorf("cannot save the deferred follow-up branch: %w", err)
	}
	if _, err := git.Run(ctx, req.GitCtx, "reset", "--hard", c1); err != nil {
		return "", "", fmt.Errorf("cannot restore the worktree to the delivered commit: %w", err)
	}
	return c1, c1Tree, nil
}

// adoptRecordedC1Commit adopts HEAD as the delivered commit C1 when a split
// attempt crashed between the C1 commit and commitStagedTree's CommitSHA
// re-upsert (record.CommitSHA == "", TreeSHA holds the pre-commit snapshot).
// Tree identity proves C1 when the pre-commit hook did not mutate the tree;
// otherwise the same author/parent/count checks as adoptOwnDeliveryCommit
// prove it. The record is re-upserted with CommitSHA/TreeSHA (mirroring
// commitStagedTree) so a later crash resumes in windows A/B/C instead of
// re-adopting. It returns the adopted commit and its tree.
func adoptRecordedC1Commit(ctx context.Context, repo ledger.Repository, git GitRunner, req Request, existing ledger.DeliveryRecord, head string) (string, string, error) {
	headTree, terr := git.Run(ctx, req.GitCtx, "rev-parse", "HEAD^{tree}")
	if terr != nil {
		return "", "", fmt.Errorf("cannot verify recorded delivery commit: %w", terr)
	}
	headTree = strings.TrimSpace(headTree)
	if headTree != existing.TreeSHA {
		// A tree-mutating pre-commit hook changed the tree between the
		// snapshot and the commit: prove HEAD is the run's own C1.
		if err := verifyMiviaCommitOnTop(ctx, git, req.GitCtx, req.BaseCommit); err != nil {
			return "", "", err
		}
	}
	rec := existing
	rec.TreeSHA = headTree
	rec.CommitSHA = head
	if err := repo.UpsertDelivery(ctx, rec); err != nil {
		return "", "", err
	}
	return head, headTree, nil
}

// verifyMiviaCommitOnTop verifies HEAD is EXACTLY ONE commit on top of parent
// (rev-list count 1), whose parent IS parent, authored by the mivia delivery
// identity - the same trust model as adoptOwnFollowUpCommit. Git execution
// failures stay recoverable (a plain error keeps the run delivery_pending);
// only a verified mismatch is a refusal.
func verifyMiviaCommitOnTop(ctx context.Context, git GitRunner, gc GitContext, parent string) error {
	count, cerr := git.Run(ctx, gc, "rev-list", "--count", parent+"..HEAD")
	if cerr != nil {
		return fmt.Errorf("cannot count delivery commits: %w", cerr)
	}
	if strings.TrimSpace(count) != "1" {
		return &RefusalError{Reason: "worktree has foreign commits or uncommitted changes"}
	}
	gotParent, perr := git.Run(ctx, gc, "rev-parse", "HEAD~1")
	if perr != nil {
		return fmt.Errorf("cannot verify delivery commit parent: %w", perr)
	}
	if strings.TrimSpace(gotParent) != parent {
		return &RefusalError{Reason: "worktree has foreign commits or uncommitted changes"}
	}
	author, aerr := git.Run(ctx, gc, "log", "-1", "--format=%an/%ae", "HEAD")
	if aerr != nil {
		return fmt.Errorf("cannot verify delivery commit author: %w", aerr)
	}
	if strings.TrimSpace(author) != mviaCommitAuthorName+"/"+mviaCommitAuthorEmail {
		return &RefusalError{Reason: "worktree has foreign commits or uncommitted changes"}
	}
	return nil
}

// reexecuteDeferredCommit re-creates the deferred commit C2 on top of the
// delivered commit c1 after a split attempt crashed in window A (or right
// after the C1 commit, before the CommitSHA record write). The deferred files
// must still be present in the worktree: C2 is committed from EXACTLY those
// paths, saved under the deferred branch, and the worktree is reset back to
// c1. It returns c1 and its tree unchanged.
func reexecuteDeferredCommit(ctx context.Context, repo ledger.Repository, git GitRunner, req Request, deferred []string, branch, c1, c1Tree, title, body string) (string, string, error) {
	addArgs := append([]string{"-c", "core.fsmonitor=false", "add", "--"}, deferred...)
	if _, err := git.Run(ctx, req.GitCtx, addArgs...); err != nil {
		return "", "", fmt.Errorf("cannot stage deferred_files for the follow-up commit: %w", err)
	}
	paths, err := stagedPaths(ctx, git, req.GitCtx)
	if err != nil {
		return "", "", err
	}
	if !samePathSet(paths, deferred) {
		// Name BOTH lists: the recorded deferred files are the resume
		// contract, and the repair agent must see exactly which staged files
		// drifted so it can reconcile the worktree instead of guessing.
		return "", "", &RefusalError{Reason: fmt.Sprintf("cannot resume the deferred-file split: the worktree no longer holds exactly the recorded deferred files (recorded: %s; currently staged: %s)", strings.Join(deferred, ", "), strings.Join(paths, ", "))}
	}
	bodyText, err := req.Policy.RenderCommitMessage(req.Inputs)
	if err != nil {
		return "", "", fmt.Errorf("cannot render commit message for the deferred follow-up: %w", err)
	}
	if strings.TrimSpace(bodyText) == "" {
		bodyText = body
	}
	msg := deferredCommitMessage(title, bodyText, len(deferred))
	if _, err := git.Run(ctx, req.GitCtx, "-c", "core.fsmonitor=false",
		"-c", "user.name="+mviaCommitAuthorName, "-c", "user.email="+mviaCommitAuthorEmail,
		"commit", "--allow-empty-message", "-m", msg); err != nil {
		return "", "", fmt.Errorf("cannot commit deferred_files: %w", err)
	}
	if _, err := git.Run(ctx, req.GitCtx, "branch", "-f", branch, "HEAD"); err != nil {
		return "", "", fmt.Errorf("cannot save the deferred follow-up branch: %w", err)
	}
	if _, err := git.Run(ctx, req.GitCtx, "reset", "--hard", c1); err != nil {
		return "", "", fmt.Errorf("cannot restore the worktree to the delivered commit: %w", err)
	}
	return c1, c1Tree, nil
}

// stagedPaths lists the paths with staged changes vs HEAD, with
// core.quotePath=false so non-ASCII paths come back literal (the default
// C-quoting would turn "café.txt" into "caf\303\251.txt" and break the
// exact-set comparison against the recorded deferred paths).
func stagedPaths(ctx context.Context, git GitRunner, gc GitContext) ([]string, error) {
	out, err := git.Run(ctx, gc, "-c", "core.quotePath=false", "diff", "--cached", "--name-only")
	if err != nil {
		return nil, fmt.Errorf("cannot list staged paths: %w", err)
	}
	var paths []string
	for _, p := range strings.Split(strings.TrimSpace(out), "\n") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// samePathSet reports whether two path lists hold exactly the same set of
// paths (order-insensitive, duplicates-insensitive).
func samePathSet(a, b []string) bool {
	set := make(map[string]struct{}, len(a))
	for _, p := range a {
		set[p] = struct{}{}
	}
	if len(set) != len(b) {
		return false
	}
	for _, p := range b {
		if _, ok := set[p]; !ok {
			return false
		}
	}
	return true
}
