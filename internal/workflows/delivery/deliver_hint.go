package delivery

import (
	"context"
	"strings"

	ledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// deliveryInventoryHint builds a deterministic inventory of the delivery
// worktree for a failed push: which files the delivered commit carries
// (base..HEAD), which files a split deferred to the follow-up branch, and
// which worktree changes the delivered commit does not carry. It is the
// diagnostic that lets a repair agent tell "the hook rejected the code" from
// "the hook tested a stale or partial tree" - for example after the automatic
// split moved a file's tests to the follow-up PR while the production file
// stayed in the delivered commit (the observed delivery-repair death loop: the
// agent kept reverting the production code to satisfy the delivered commit's
// stale test expectations).
//
// Every section is computed independently and best-effort: a git failure
// drops that section but never masks the caller's original error. Returns ""
// when nothing could be computed. The caller MUST place this text BEFORE the
// rejection output: markFailed stores at most maxErrorBytes and truncates
// from the end, so a long hook rejection would otherwise push the diagnostic
// out of the recorded repair hint.
func deliveryInventoryHint(ctx context.Context, git GitRunner, req Request, existing ledger.DeliveryRecord) string {
	var b strings.Builder
	if committed := runGitLines(ctx, git, req, "diff", "--name-only", req.BaseCommit+"..HEAD"); len(committed) > 0 {
		b.WriteString("delivered commit files (base..HEAD):\n")
		for _, f := range committed {
			b.WriteString("  " + f + "\n")
		}
	}
	if deferred := deferredFilesForHint(req, existing); len(deferred) > 0 {
		b.WriteString("deferred files (excluded from the delivered commit; follow-up PR):\n")
		for _, f := range deferred {
			b.WriteString("  " + f + "\n")
		}
	}
	var pending []string
	pending = append(pending, runGitLines(ctx, git, req, "diff", "--name-only", "HEAD")...)
	pending = append(pending, runGitLines(ctx, git, req, "ls-files", "--others", "--exclude-standard")...)
	if len(pending) > 0 {
		b.WriteString("worktree changes not in the delivered commit:\n")
		for _, f := range pending {
			b.WriteString("  " + f + "\n")
		}
	}
	if b.Len() == 0 {
		return ""
	}
	b.WriteString("\nThe repository's own pre-push hook verified the DELIVERED COMMIT tree (the files listed above), not the worktree. The workflow's evidence gates verified the worktree, which includes uncommitted changes and deferred split files. When the two trees differ, the hook can reject the delivered commit for reasons the gates never saw. Do NOT revert production code to satisfy a stale test in the delivered commit - that undoes the fix. Make the delivered commit internally consistent: ship a file together with its tests (both delivered or both deferred), or shrink the change.\n")
	return b.String()
}

// runGitLines runs one pinned git command and returns its non-empty trimmed
// lines. A git failure returns nil: hint sections are best-effort and must
// never mask the original delivery error.
func runGitLines(ctx context.Context, git GitRunner, req Request, args ...string) []string {
	out, err := git.Run(ctx, req.GitCtx, args...)
	if err != nil {
		return nil
	}
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// deferredFilesForHint returns the deferred-file list for the hint: this
// attempt's split decision when present, else the decision recorded by a
// previous attempt (a resume attempt carries it on the existing record, not
// on the run's inputs). A malformed value is skipped - the raw rejection text
// below still carries the authoritative hook output.
func deferredFilesForHint(req Request, existing ledger.DeliveryRecord) []string {
	if raw := req.Inputs[InputDeferredFiles]; strings.TrimSpace(raw) != "" {
		if files, err := ParseDeferredFiles(raw); err == nil {
			return files
		}
	}
	if files, err := ParseDeferredFiles(existing.DeferredFiles); err == nil {
		return files
	}
	return nil
}
