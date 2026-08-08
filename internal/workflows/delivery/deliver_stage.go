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
			headTree = strings.TrimSpace(headTree)
			if headTree == existing.TreeSHA {
				// Same tree as the recorded snapshot: the common crash-resume
				// case, verified by tree identity.
				if !porcelainEmpty {
					return "", "", &RefusalError{Reason: "worktree has foreign commits or uncommitted changes"}
				}
				count, cerr := git.Run(ctx, req.GitCtx, "rev-list", "--count", req.BaseCommit+"..HEAD")
				if cerr != nil {
					return "", "", fmt.Errorf("cannot count delivery commits: %w", cerr)
				}
				if strings.TrimSpace(count) != "1" {
					return "", "", &RefusalError{Reason: "worktree has foreign commits or uncommitted changes"}
				}
				return head, treeSHA, nil
			}
			// Tree mismatch: a tree-mutating pre-commit hook (e.g. gofmt -w +
			// git add) can legitimately change the tree between the pending
			// record's snapshot and the commit, and a crash between `git
			// commit` and the adoption re-upsert (commitStagedTree's
			// UpsertDelivery) leaves the record holding the PRE-hook tree.
			// Refusing outright would permanently strand the run's OWN commit
			// (delivery_failed with no return edge), so verify HEAD is our
			// delivery commit instead: exactly one commit on top of base, a
			// clean worktree, the parent IS the admitted base, and the author
			// is the mivia delivery identity.
			return adoptOwnDeliveryCommit(ctx, repo, git, req, existing, head, headTree, diffRef, porcelainEmpty)
		default:
			return "", "", &RefusalError{Reason: "worktree has foreign commits or uncommitted changes"}
		}
		return head, treeSHA, nil
	}

	// Fresh: create the delivery commit (idempotent on retry).
	return freshDeliveryCommit(ctx, repo, git, req, key, diffRef)
}

// freshDeliveryCommit stages the intended change (idempotent on retry),
// snapshots the staged tree, records the pending attempt with the tree
// BEFORE creating the commit (so the retry can resume by tree verification),
// and commits through Git (commitStagedTree). It returns the adopted HEAD
// and its ACTUAL tree.
func freshDeliveryCommit(ctx context.Context, repo ledger.Repository, git GitRunner, req Request, key, diffRef string) (string, string, error) {
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
	treeSHA := strings.TrimSpace(treeOut)
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

// adoptOwnDeliveryCommit verifies HEAD is the run's own delivery commit after
// a tree-mutating pre-commit hook (e.g. gofmt -w + git add) changed the tree
// between the pending record's snapshot and the commit: exactly one commit on
// top of base, a clean worktree, the parent IS the admitted base, and the
// author is the mivia delivery identity. A `git commit --amend` with DIFFERENT
// content preserves the original author, parent and commit count, so the FILE
// SET of the committed files (base..HEAD) must also EXACTLY match the recorded
// snapshot's files (base..existing.TreeSHA). Git execution failures stay
// recoverable (plain error keeps the run delivery_pending); only a verified
// mismatch is a refusal. On success the pending record is re-upserted with the
// ACTUAL committed tree, the now-known CommitSHA, and the retry's FRESH diff
// ref (what is actually at HEAD), preserving every other field of the existing
// record (RemoteID, URL, Mode, BaseRef, HeadRef, Provider). It returns the
// adopted HEAD and its actual tree.
func adoptOwnDeliveryCommit(ctx context.Context, repo ledger.Repository, git GitRunner, req Request, existing ledger.DeliveryRecord, head, headTree, diffRef string, porcelainEmpty bool) (string, string, error) {
	if !porcelainEmpty {
		return "", "", &RefusalError{Reason: "worktree has foreign commits or uncommitted changes"}
	}
	count, cerr := git.Run(ctx, req.GitCtx, "rev-list", "--count", req.BaseCommit+"..HEAD")
	if cerr != nil {
		return "", "", fmt.Errorf("cannot count delivery commits: %w", cerr)
	}
	if strings.TrimSpace(count) != "1" {
		return "", "", &RefusalError{Reason: "worktree has foreign commits or uncommitted changes"}
	}
	parent, perr := git.Run(ctx, req.GitCtx, "rev-parse", "HEAD~1")
	if perr != nil {
		return "", "", fmt.Errorf("cannot verify delivery commit parent: %w", perr)
	}
	if strings.TrimSpace(parent) != req.BaseCommit {
		return "", "", &RefusalError{Reason: "worktree has foreign commits or uncommitted changes"}
	}
	authorName, anerr := git.Run(ctx, req.GitCtx, "log", "-1", "--format=%an", "HEAD")
	if anerr != nil {
		return "", "", fmt.Errorf("cannot verify delivery commit author: %w", anerr)
	}
	authorEmail, aeerr := git.Run(ctx, req.GitCtx, "log", "-1", "--format=%ae", "HEAD")
	if aeerr != nil {
		return "", "", fmt.Errorf("cannot verify delivery commit author: %w", aeerr)
	}
	if strings.TrimSpace(authorName) != "mivia" || strings.TrimSpace(authorEmail) != "mivia@localhost" {
		return "", "", &RefusalError{Reason: "worktree has foreign commits or uncommitted changes"}
	}
	// File-set verification: a `git commit --amend` with DIFFERENT
	// content preserves the original author, parent and commit count,
	// so the checks above cannot distinguish it from our own
	// hook-mutated commit. The file set CAN: the retry's committed
	// files (base..HEAD) must be EXACTLY the recorded snapshot's files
	// (base..existing.TreeSHA). A hook mutation like gofmt -w + git add
	// keeps the same file list; an amend that adds or removes a file
	// changes it. Git execution failures stay recoverable (plain
	// error); only a verified file-set mismatch is a refusal.
	headFiles, ferr := git.Run(ctx, req.GitCtx, "diff", "--name-only", req.BaseCommit+"..HEAD")
	if ferr != nil {
		return "", "", fmt.Errorf("cannot list delivery commit files: %w", ferr)
	}
	recFiles, rerr := git.Run(ctx, req.GitCtx, "diff", "--name-only", req.BaseCommit+".."+existing.TreeSHA)
	if rerr != nil {
		return "", "", fmt.Errorf("cannot list recorded delivery files: %w", rerr)
	}
	if headFiles != recFiles {
		return "", "", &RefusalError{Reason: "worktree has foreign commits or uncommitted changes"}
	}
	// Adopt: re-upsert the pending record with the ACTUAL committed
	// tree, the now-known CommitSHA, and the retry's FRESH diff ref
	// (diffRef is this attempt's recomputed snapshot of what is
	// actually at HEAD, so the durable record describes what is
	// published — not the pre-hook/pre-amend diff), PRESERVING every
	// other field of the EXISTING record (RemoteID, URL, Mode,
	// BaseRef, HeadRef, Provider). A fresh deliveryRecord would erase
	// the PR identity; starting from `existing` mirrors
	// commitStagedTree's carry-forward of `stage`.
	rec := existing
	rec.TreeSHA = headTree
	rec.CommitSHA = head
	rec.DiffRef = diffRef
	if err := repo.UpsertDelivery(ctx, rec); err != nil {
		return "", "", err
	}
	return head, headTree, nil
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
	rec := stage
	rec.TreeSHA = adoptedTree
	rec.CommitSHA = head
	if err := repo.UpsertDelivery(ctx, rec); err != nil {
		return "", "", err
	}
	return head, adoptedTree, nil
}
