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
	"encoding/json"
	"fmt"
	"strings"

	ledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// latestDeferredDeliveryRecord returns runID's most recent succeeded delivery
// record that left a pending deferred commit (DeliveryRecord.StackRemainingCommits
// > 0), or ok=false when there is none. HasDeferredFollowUp is this function's
// boolean projection; EnsureFollowUpPublished needs the full record for
// DeferredFiles, so both share this one lookup instead of two separate scans.
func latestDeferredDeliveryRecord(ctx context.Context, repo ledger.Repository, runID string) (ledger.DeliveryRecord, bool) {
	records, err := repo.ListDeliveries(ctx, runID)
	if err != nil {
		return ledger.DeliveryRecord{}, false
	}
	for _, rec := range records {
		if rec.Status == "succeeded" && rec.StackRemainingCommits > 0 {
			return rec, true
		}
	}
	return ledger.DeliveryRecord{}, false
}

// HasDeferredFollowUp reports whether runID's most recent succeeded delivery
// record left a pending deferred commit (DeliveryRecord.StackRemainingCommits
// > 0).
func HasDeferredFollowUp(ctx context.Context, repo ledger.Repository, runID string) bool {
	_, ok := latestDeferredDeliveryRecord(ctx, repo, runID)
	return ok
}

// deferredFileList decodes a DeliveryRecord.DeferredFiles JSON array. A parse
// failure or empty value returns nil - the caller falls back to a file-count-
// free body rather than failing PR publication over a cosmetic list.
func deferredFileList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var files []string
	if err := json.Unmarshal([]byte(raw), &files); err != nil {
		return nil
	}
	return files
}

// followUpPRContent builds the title and body for a deferred follow-up PR.
// When parentRef is non-nil and carries a title (the common case - the
// parent PR that EnsureFollowUpPublished is following up on), the title
// reuses the parent's own agent-authored title via deriveTitle, and the
// body links every PR it mentions (never a bare "#142") plus lists the real
// deferred files instead of a generic "the scope the diff-size gate
// deferred" sentence. When parentRef lookup failed (rare: the parent PR is
// somehow not findable by head), both fall back to a label-derived form that
// still identifies the run, so publication is never blocked on this
// enrichment.
func followUpPRContent(label, parentBranch string, parentRef *PRRef, deferredFiles []string) (title, body string) {
	baseTitle := label
	baseLink := parentBranch
	if parentRef != nil && strings.TrimSpace(parentRef.Title) != "" {
		baseTitle = parentRef.Title
	}
	if parentRef != nil {
		if link := prLinkMarkdown(*parentRef); link != "" {
			baseLink = link
		}
	}
	// deriveTitle's truncated result is used unconditionally here (the fits
	// bool is ignored): this path has no repair loop, so the title must
	// never overflow GitHub's limit and fail pr.Create outright - a reused
	// parent title is already at MaxTitleRunes at worst, and the affix
	// would push it over unguarded. Contrast appendStackPartTitle, which
	// rejects an overflow instead (it CAN route to a repair step).
	title, _ = deriveTitle(baseTitle, "[split 2/2, base: "+shortRef(parentRef, parentBranch)+"]", MaxTitleRunes)

	var sb strings.Builder
	sb.WriteString("Mechanical split, not an agent-authored description.\n\n")
	if len(deferredFiles) > 0 {
		fmt.Fprintf(&sb, "%d file(s) deferred from %s to fit the stacking diff-size limit:\n", len(deferredFiles), baseLink)
		for _, f := range deferredFiles {
			sb.WriteString("- " + f + "\n")
		}
	} else {
		fmt.Fprintf(&sb, "File(s) deferred from %s to fit the stacking diff-size limit.\n", baseLink)
	}
	// This PR's base IS parentBranch (pr.Create below: Base: parentBranch,
	// Head: deferredBranch), so it merges INTO the parent branch - meaning
	// this PR merges FIRST, and only then does the parent PR (parentBranch
	// -> main) carry the deferred commit along with it. Getting this
	// backwards would tell a reviewer to merge the parent first, which
	// either fails (this PR's base branch would already be gone) or
	// silently drops the deferred scope from the eventual main history.
	sb.WriteString("\nBase: " + baseLink + ". Merge order: this PR -> " + baseLink + ".")
	return title, sb.String()
}

// shortRef renders the bracket-tag reference for a derived title: the
// parent's own "#142" when known, or the raw branch name as a fallback so
// the tag is never empty.
func shortRef(parentRef *PRRef, parentBranch string) string {
	if parentRef != nil && parentRef.RemoteID != "" {
		return "#" + parentRef.RemoteID
	}
	return parentBranch
}

// EnsureFollowUpPublished pushes run's deferred branch (left by
// freshDeliveryCommitSplit, when checkChunkDiffSize split an oversized diff)
// and opens a follow-up PR stacked on the delivered branch, if delivery left
// one pending (HasDeferredFollowUp). It has no ledger/stack registration
// dependency beyond reading the deferred file list - it is still safe and
// cheap to call unconditionally after every successful delivery, from every
// completion path. Idempotent: FindByHead reuses an existing PR instead of
// creating a second one, so multiple callers publishing the SAME run's
// follow-up (the generic post-delivery call, and the stack driver's own
// later pass) never double-publish. published=false with a nil error means
// nothing was deferred - the normal case for every non-split delivery.
func EnsureFollowUpPublished(ctx context.Context, git GitRunner, pr PRClient, worktreeRoot string, repo ledger.Repository, run ledger.RunSnapshot, label string, stdout func(string)) (branch, sha string, ref PRRef, published bool, err error) {
	if worktreeRoot == "" || run.WorktreeName == "" {
		return "", "", PRRef{}, false, nil
	}
	rec, ok := latestDeferredDeliveryRecord(ctx, repo, run.RunID)
	if !ok {
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
	// Best-effort: the parent PR's own title/number/URL enrich the follow-up's
	// title and body (see followUpPRContent). A lookup failure never blocks
	// publication - it only falls back to the raw label/branch name.
	parentRef, _ := pr.FindByHead(ctx, slug, parentBranch)
	title, body := followUpPRContent(label, parentBranch, parentRef, deferredFileList(rec.DeferredFiles))
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
