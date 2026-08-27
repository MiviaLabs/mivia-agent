package delivery

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// countingGit fails the first failCount Run calls, then succeeds, and counts
// every call. pushCalls counts only the branch-push calls, so assertions stay
// exact even when the failure path runs additional diagnostic git calls (the
// delivery commit inventory hint).
type countingGit struct {
	failCount int
	calls     int
	pushCalls int
}

func (g *countingGit) Run(ctx context.Context, gc GitContext, args ...string) (string, error) {
	g.calls++
	for _, a := range args {
		if a == "push" {
			g.pushCalls++
			break
		}
	}
	if g.calls <= g.failCount {
		return "", errors.New("signal: killed: pre-push hook")
	}
	return "ok", nil
}

func setPushRetryDelay(t *testing.T) {
	t.Helper()
	prev := pushRetryDelay
	pushRetryDelay = func(int) time.Duration { return time.Millisecond }
	t.Cleanup(func() { pushRetryDelay = prev })
}

// TestPushDeliveryBranchRetriesTransientFailure pins the durability fix: a
// push that dies mid-hook (the pre-push verify gate under memory pressure)
// is retried with backoff instead of stranding the run at delivery_pending
// on the first attempt.
func TestPushDeliveryBranchRetriesTransientFailure(t *testing.T) {
	setPushRetryDelay(t)
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	git := &countingGit{failCount: 2}
	req := newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"})
	if err := pushDeliveryBranch(ctx, repo, git, req, "key", "deadbeef", "tree", "diff", workflowledger.DeliveryRecord{}); err != nil {
		t.Fatalf("pushDeliveryBranch err = %v, want success after retries", err)
	}
	if git.calls != maxPushAttempts {
		t.Fatalf("git.Run calls = %d, want %d", git.calls, maxPushAttempts)
	}
	if git.pushCalls != maxPushAttempts {
		t.Fatalf("push attempts = %d, want %d", git.pushCalls, maxPushAttempts)
	}
	rec, getErr := repo.GetDeliveryByIdempotencyKey(ctx, "key")
	if getErr != nil || rec.Status != "pushed" {
		t.Fatalf("record status = %q (err %v), want pushed", rec.Status, getErr)
	}
}

// TestPushDeliveryBranchFailsAfterMaxAttempts pins the bound: a push that
// never succeeds records a failed delivery with a readable error hint after
// exactly maxPushAttempts calls.
func TestPushDeliveryBranchFailsAfterMaxAttempts(t *testing.T) {
	setPushRetryDelay(t)
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	git := &countingGit{failCount: 99}
	req := newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"})
	if err := pushDeliveryBranch(ctx, repo, git, req, "key", "deadbeef", "tree", "diff", workflowledger.DeliveryRecord{}); err == nil {
		t.Fatal("expected error after max attempts")
	}
	if git.pushCalls != maxPushAttempts {
		t.Fatalf("push attempts = %d, want %d", git.pushCalls, maxPushAttempts)
	}
	rec, getErr := repo.GetDeliveryByIdempotencyKey(ctx, "key")
	if getErr != nil || rec.Status != "failed" {
		t.Fatalf("record status = %q (err %v), want failed", rec.Status, getErr)
	}
	if rec.ErrorRef == "" {
		t.Fatal("failed record missing ErrorRef")
	}
	body, loadErr := repo.LoadContent(ctx, rec.ErrorRef)
	if loadErr != nil || !strings.Contains(string(body), "killed") {
		t.Fatalf("error content = %q (err %v), want the push failure text", string(body), loadErr)
	}
}
