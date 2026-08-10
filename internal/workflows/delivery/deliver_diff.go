package delivery

import (
	"context"
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

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
	// The worktree against the admitted base, ignoring CR-at-EOL. A Windows
	// autocrlf checkout converts LF blobs into CRLF files, and the delivery
	// git context pins GIT_CONFIG_NOSYSTEM=1 (system config is not read), so
	// git would otherwise report the whole file as changed. Line-ending-only
	// work is never an intended diff: a worktree whose porcelain is limited
	// to modified entries and whose diff (CR ignored) is empty settles as
	// no_diff below.
	worktreeStat, werr := git.Run(ctx, req.GitCtx, "diff", "--no-ext-diff", "--no-textconv", "--ignore-cr-at-eol", "--stat", req.BaseCommit)
	if werr != nil {
		return "", false, "", "", false, fmt.Errorf("git diff --stat failed: %w", werr)
	}
	if head != req.BaseCommit {
		committed, derr := git.Run(ctx, req.GitCtx, "diff", "--stat", req.BaseCommit+"..HEAD")
		if derr != nil {
			return "", false, "", "", false, fmt.Errorf("git diff --stat failed: %w", derr)
		}
		if strings.TrimSpace(committed) == "" && (porcelainEmpty || (strings.TrimSpace(worktreeStat) == "" && porcelainIsLineEndingNoise(porcelain))) {
			// Nothing to publish: no committed change and a clean worktree
			// (or one that differs only by line-ending normalization).
			if err := repo.UpsertDelivery(ctx, deliveryRecord(req, key, "no_diff")); err != nil {
				return "", false, "", "", false, err
			}
			return head, true, "", "", true, nil
		}
		return head, porcelainEmpty, committed, porcelain, false, nil
	}
	if porcelainEmpty || (strings.TrimSpace(worktreeStat) == "" && porcelainIsLineEndingNoise(porcelain)) {
		// Nothing to publish: a clean worktree at the base commit, or one
		// whose only difference is line-ending normalization.
		if err := repo.UpsertDelivery(ctx, deliveryRecord(req, key, "no_diff")); err != nil {
			return "", false, "", "", false, err
		}
		return head, true, "", "", true, nil
	}
	return head, porcelainEmpty, worktreeStat, porcelain, false, nil
}

// porcelainIsLineEndingNoise reports whether porcelain contains modified
// entries only - no untracked, added, deleted, renamed, or copied entries.
// Together with an empty CR-ignored diff, that means the worktree differs
// from the base only by line-ending normalization, which is not intended
// work. Untracked files are invisible to `git diff <commit>`, so their
// presence always means real work.
func porcelainIsLineEndingNoise(porcelain string) bool {
	for _, line := range strings.Split(porcelain, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "??") {
			return false
		}
		if len(line) < 2 {
			return false
		}
		switch line[0] {
		case 'A', 'D', 'R', 'C':
			return false
		}
		switch line[1] {
		case 'A', 'D', 'R', 'C':
			return false
		}
	}
	return true
}
