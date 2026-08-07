package delivery

import (
	"context"
	"fmt"
	"strings"

	ledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

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
			// worktree, and exactly one commit on top of base. Git execution
			// failures are recoverable (plain error keeps the run
			// delivery_pending); only a verified mismatch is a refusal.
			headTree, terr := git.Run(ctx, req.GitCtx, "rev-parse", "HEAD^{tree}")
			if terr != nil {
				return "", "", fmt.Errorf("cannot verify recorded delivery commit: %w", terr)
			}
			if strings.TrimSpace(headTree) != existing.TreeSHA || !porcelainEmpty {
				return "", "", &RefusalError{Reason: "worktree has foreign commits or uncommitted changes"}
			}
			count, cerr := git.Run(ctx, req.GitCtx, "rev-list", "--count", req.BaseCommit+"..HEAD")
			if cerr != nil {
				return "", "", fmt.Errorf("cannot count delivery commits: %w", cerr)
			}
			if strings.TrimSpace(count) != "1" {
				return "", "", &RefusalError{Reason: "worktree has foreign commits or uncommitted changes"}
			}
		default:
			return "", "", &RefusalError{Reason: "worktree has foreign commits or uncommitted changes"}
		}
		return head, treeSHA, nil
	}

	// Fresh: stage the intended change (idempotent on retry).
	if _, err := git.Run(ctx, req.GitCtx, "-c", "core.fsmonitor=false", "add", "-A"); err != nil {
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
	if _, err := git.Run(ctx, req.GitCtx, "diff", "--quiet", "--cached", treeSHA); err != nil {
		return "", "", &RefusalError{Reason: "staged tree changed before commit"}
	}
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
	sha, adoptedTree, err := commitStagedTree(ctx, repo, git, req, stage, treeSHA, msg)
	if err != nil {
		markFailed(ctx, repo, key, req, err)
		return "", "", err
	}
	return sha, adoptedTree, nil
}

// commitStagedTree creates the delivery commit and returns the adopted HEAD
// commit and its ACTUAL tree. The repo's pre-commit hook can legitimately
// mutate the staged tree (e.g. gofmt -w + git add) AFTER the pending record
// snapshotted it: the commit then SUCCEEDS with a different tree. Instead of
// erroring (which would leave the recorded TreeSHA forever mismatched against
// HEAD^{tree} and make the retry's crash-resume refuse the worktree as
// foreign), the ACTUAL committed tree is adopted: the pending record is
// re-upserted with the adopted TreeSHA and the now-known CommitSHA so a crash
// after the commit can resume by tree or commit verification. The re-upsert
// is the SAME pending record with ONLY TreeSHA/CommitSHA changed
// (byte-identical caller fields) and fires only here, on the fresh-commit
// path — never on a retry.
func commitStagedTree(ctx context.Context, repo ledger.Repository, git GitRunner, req Request, stage ledger.DeliveryRecord, treeSHA, msg string) (string, string, error) {
	// Commit through Git so pre-commit and commit-msg hooks can reject it.
	if _, err := git.Run(ctx, req.GitCtx, "-c", "core.fsmonitor=false",
		"-c", "user.name=mivia", "-c", "user.email=mivia@localhost",
		"commit", "--allow-empty-message", "-m", msg); err != nil {
		return "", "", err
	}
	committedTree, err := git.Run(ctx, req.GitCtx, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return "", "", err
	}
	shaOut, err := git.Run(ctx, req.GitCtx, "rev-parse", "HEAD")
	if err != nil {
		return "", "", err
	}
	head := strings.TrimSpace(shaOut)
	adoptedTree := strings.TrimSpace(committedTree)
	if adoptedTree != treeSHA {
		rec := stage
		rec.TreeSHA = adoptedTree
		rec.CommitSHA = head
		if err := repo.UpsertDelivery(ctx, rec); err != nil {
			return "", "", err
		}
	}
	return head, adoptedTree, nil
}
