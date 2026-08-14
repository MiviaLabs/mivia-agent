package delivery

import (
	"context"
	"fmt"
	"strings"
)

// guardChunkBaseDrift runs checkChunkBaseDrift only for stacking workflows
// (StackingHardLines > 0); a bare single-PR delivery has no siblings to
// drift against.
func guardChunkBaseDrift(ctx context.Context, git GitRunner, req Request, originBase string) error {
	if req.Policy.StackingHardLines <= 0 {
		return nil
	}
	return checkChunkBaseDrift(ctx, git, req, originBase)
}

// checkChunkBaseDrift detects a stale stacked-chunk diff: originBase is the
// commit this chunk was admitted against; req.Policy.Base's current remote
// tip (already fetched by verifyRemoteBaseAncestry) may have advanced past
// it if a sibling chunk merged while this one was in flight. If any file
// this chunk's diff touches was also changed by that advance, this chunk's
// diff was computed against content the sibling has since replaced, and
// delivering it would silently revert or duplicate the sibling's work. The
// caller only invokes this for stacking workflows (StackingHardLines > 0); a
// bare single-PR delivery never drifts against a sibling.
func checkChunkBaseDrift(ctx context.Context, git GitRunner, req Request, originBase string) error {
	out, err := git.Run(ctx, req.GitCtx, "rev-parse", "--verify", "--end-of-options", "refs/remotes/origin/"+req.Policy.Base+"^{commit}")
	if err != nil {
		return fmt.Errorf("cannot resolve remote delivery base %q: %w", req.Policy.Base, err)
	}
	fetchedBase := strings.TrimSpace(out)
	if fetchedBase == originBase {
		return nil
	}
	landedOut, err := git.Run(ctx, req.GitCtx, "diff", "--name-only", originBase+".."+fetchedBase)
	if err != nil {
		return fmt.Errorf("cannot diff advanced remote base %s..%s: %w", originBase, fetchedBase, err)
	}
	landed := map[string]bool{}
	for _, f := range strings.Split(strings.TrimSpace(landedOut), "\n") {
		if f != "" {
			landed[f] = true
		}
	}
	if len(landed) == 0 {
		return nil
	}
	// The chunk's own change may still be uncommitted working-tree state at
	// this point in Deliver (the fresh commit happens later): stage it first
	// so the diff sees it, exactly as MeasureChunkDiffSize does.
	if _, err := git.Run(ctx, req.GitCtx, "-c", "core.fsmonitor=false", "add", "-A"); err != nil {
		return fmt.Errorf("cannot stage the chunk's diff to check for base drift: %w", err)
	}
	touchedOut, err := git.Run(ctx, req.GitCtx, "-c", "core.quotePath=false", "diff", "--cached", "--name-only", req.BaseCommit)
	if err != nil {
		return fmt.Errorf("cannot diff chunk's touched files: %w", err)
	}
	var overlap []string
	for _, f := range strings.Split(strings.TrimSpace(touchedOut), "\n") {
		if f != "" && landed[f] {
			overlap = append(overlap, f)
		}
	}
	if len(overlap) == 0 {
		return nil
	}
	// A plain error, NOT a RefusalError: a mid-flight sibling merge is a
	// timing accident with a worktree-level fix (reconcile this chunk's
	// change against what the sibling landed), not a permanent host
	// refusal - a RefusalError would settle the run delivery_failed before
	// the repair step ever ran (workflow_deliver.go settleDeliveryError).
	return fmt.Errorf("delivery: chunk touches %d file(s) already changed on %q since this chunk was admitted (%s): reconcile this chunk's change against what already landed before delivery", len(overlap), req.Policy.Base, strings.Join(overlap, ", "))
}
