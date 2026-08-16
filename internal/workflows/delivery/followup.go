package delivery

// Follow-up PR publication (spec-auto-split-oversized-prs.md §5.2-5.3): after
// checkChunkDiffSize splits an oversized diff, the delivered commit is
// pushed and its PR created by the normal Deliver() path, but the DEFERRED
// commit is only ever saved to a local branch (freshDeliveryCommitSplit) -
// nothing pushes it or opens its own PR. That used to be the exclusive job
// of the CLI stack driver's admitFollowUpsForChunk, reached only when a run
// is part of a multi-chunk stack being actively driven. A split can happen
// on ANY successful delivery, stacked or not (decompose can choose a single
// chunk even when the resulting diff turns out oversized), so a plain,
// non-stacked delivery left its deferred branch permanently unpublished -
// a silently orphaned commit, discovered via a live run. This file is
// shared by every delivery completion path (CLI's deliverRunWithStore, the
// session engine's Deliver, and the stack driver's own admitFollowUpsForChunk)
// so all of them close the gap identically.

import (
	"context"
	"fmt"
	"strings"

	ledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// HasDeferredFollowUp reports whether runID's most recent succeeded delivery
// record left a pending deferred commit (DeliveryRecord.StackRemainingCommits
// > 0).
func HasDeferredFollowUp(ctx context.Context, repo ledger.Repository, runID string) bool {
	records, err := repo.ListDeliveries(ctx, runID)
	if err != nil {
		return false
	}
	for _, rec := range records {
		if rec.Status == "succeeded" && rec.StackRemainingCommits > 0 {
			return true
		}
	}
	return false
}

// EnsureFollowUpPublished pushes run's deferred branch (left by
// freshDeliveryCommitSplit, when checkChunkDiffSize split an oversized diff)
// and opens a follow-up PR stacked on the delivered branch, if delivery left
// one pending (HasDeferredFollowUp). It has no ledger/stack registration
// dependency - only git and the PR client - so it is safe and cheap to call
// unconditionally after every successful delivery, from every completion
// path. Idempotent: FindByHead reuses an existing PR instead of creating a
// second one, so multiple callers publishing the SAME run's follow-up (the
// generic post-delivery call, and the stack driver's own later pass) never
// double-publish. published=false with a nil error means nothing was
// deferred - the normal case for every non-split delivery.
func EnsureFollowUpPublished(ctx context.Context, git GitRunner, pr PRClient, worktreeRoot string, repo ledger.Repository, run ledger.RunSnapshot, label string, stdout func(string)) (branch, sha string, ref PRRef, published bool, err error) {
	if worktreeRoot == "" || run.WorktreeName == "" {
		return "", "", PRRef{}, false, nil
	}
	if !HasDeferredFollowUp(ctx, repo, run.RunID) {
		return "", "", PRRef{}, false, nil
	}
	parentBranch := "wf/" + run.WorktreeName
	deferredBranch := DeferredBranchName(parentBranch)
	gitCtx := GitContext{Dir: worktreeRoot, GitDir: worktreeRoot + "/.git"}
	shaOut, err := git.Run(ctx, gitCtx, "rev-parse", deferredBranch)
	if err != nil {
		// No local deferred branch: nothing to publish (a plain delivery, or
		// this worktree is not the one that performed the split).
		return "", "", PRRef{}, false, nil
	}
	deferredSHA := strings.TrimSpace(shaOut)
	if _, err := git.Run(ctx, gitCtx, "push", "origin", deferredBranch+":refs/heads/"+deferredBranch); err != nil {
		return "", "", PRRef{}, false, fmt.Errorf("push deferred branch %s: %w", deferredBranch, err)
	}
	slug, err := ParseOwnerRepo(run.RemoteURL)
	if err != nil {
		return "", "", PRRef{}, false, fmt.Errorf("resolve repo for follow-up PR: %w", err)
	}
	if existing, ferr := pr.FindByHead(ctx, slug, deferredBranch); ferr == nil && existing != nil {
		if stdout != nil {
			stdout(fmt.Sprintf("chunk=%s follow-up PR %s %s already open (deferred scope, stacked on %s)\n", label, existing.RemoteID, existing.URL, parentBranch))
		}
		return deferredBranch, deferredSHA, *existing, true, nil
	}
	title := fmt.Sprintf("deferred: %s follow-up (auto-split)", label)
	body := fmt.Sprintf("Automatically split from %s's delivery: this PR carries the scope the diff-size gate deferred to fit the stacking hard limit. Stacked on %s; merge after it.", label, parentBranch)
	created, err := pr.Create(ctx, slug, PRInput{
		Base: parentBranch, Head: deferredBranch, Title: title, Body: body, Draft: true,
	})
	if err != nil {
		// A concurrent driver may have created the same PR between our
		// FindByHead miss and this Create; re-check before reporting failure.
		if existing, ferr := pr.FindByHead(ctx, slug, deferredBranch); ferr == nil && existing != nil {
			if stdout != nil {
				stdout(fmt.Sprintf("chunk=%s follow-up PR %s %s created by concurrent driver (deferred scope, stacked on %s)\n", label, existing.RemoteID, existing.URL, parentBranch))
			}
			return deferredBranch, deferredSHA, *existing, true, nil
		}
		return "", "", PRRef{}, false, fmt.Errorf("create follow-up PR: %w", err)
	}
	if stdout != nil {
		stdout(fmt.Sprintf("chunk=%s follow-up PR %s %s opened (deferred scope, stacked on %s)\n", label, created.RemoteID, created.URL, parentBranch))
	}
	return deferredBranch, deferredSHA, created, true, nil
}
