package delivery

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestBaseNotAncestorIsARefusalNotAGitFailure pins step 5 of eligibility.
//
// `git merge-base --is-ancestor` exits 1 to mean "no" - that is a successful
// verdict read from git, not a git execution failure. Treating exit 1 as a
// plain error contradicted the rule the function's own comment states two
// lines earlier ("only conditions read from SUCCESSFUL git output are
// permanent refusals") and had a real cost: a plain error falls through to
// OnFailure, so a rebased worktree dispatched a repair agent against a
// history problem it cannot see, once per repair budget slot, instead of
// refusing the delivery outright.
func TestBaseNotAncestorIsARefusalNotAGitFailure(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)

	// Make the admitted base stop being an ancestor of HEAD: reset the
	// worktree onto an unrelated root commit.
	writeWorktreeFile(t, worktreeRoot, "divergent.txt", "divergent\n")
	runGit(t, worktreeRoot, "checkout", "--orphan", "divergent-history")
	runGit(t, worktreeRoot, "add", "-A")
	runGit(t, worktreeRoot, "commit", "-m", "unrelated root commit")
	runGit(t, worktreeRoot, "branch", "-M", "wf/wt-test")

	pr := &fakePRClient{}
	_, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "t"}))
	if err == nil {
		t.Fatal("Deliver accepted a base that is not an ancestor of HEAD")
	}
	if !IsRefusal(err) {
		t.Fatalf("Deliver error = %v (%T), want a RefusalError: exit 1 from --is-ancestor is a verdict, not a git failure", err, err)
	}
	if !strings.Contains(err.Error(), baseCommit) {
		t.Fatalf("refusal %q does not name the admitted base commit %s", err, baseCommit)
	}
}

// countingPushGit runs every git command for real except `push`, which it
// fails a fixed number of times with a caller-supplied message. It counts the
// push attempts so a test can prove whether the retry loop engaged.
type countingPushGit struct {
	real     RealGit
	failWith string
	attempts int
}

func (g *countingPushGit) Run(ctx context.Context, gc GitContext, args ...string) (string, error) {
	for _, a := range args {
		if a == "push" {
			g.attempts++
			return "", errors.New(g.failWith)
		}
	}
	return g.real.Run(ctx, gc, args...)
}

// TestPermanentPushFailureIsNotRetriedOrMislabelled covers two defects in one
// path. The retry loop was unconditional, so a non-fast-forward rejection -
// which no amount of retrying fixes - burned the full backoff budget. And
// every failure, whatever its cause, was recorded as "pre-push hook
// rejection", so RepairHint handed the agent hook-oriented guidance for a
// remote-state problem it cannot fix from the worktree.
func TestPermanentPushFailureIsNotRetriedOrMislabelled(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "change.txt", "change\n")

	git := &countingPushGit{failWith: "! [rejected]        main -> main (non-fast-forward)"}
	pr := &fakePRClient{}
	_, err := Deliver(ctx, repo, git, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "t"}))
	if err == nil {
		t.Fatal("Deliver succeeded despite a rejected push")
	}
	if git.attempts != 1 {
		t.Errorf("push attempts = %d, want 1: a non-fast-forward rejection is permanent and must not be retried", git.attempts)
	}
	if strings.Contains(err.Error(), "pre-push hook rejection") {
		t.Errorf("push failure was labelled a pre-push hook rejection, but no hook ran:\n%s", err)
	}
}

// TestTransientPushFailureStillRetries keeps the retry behavior the loop
// exists for: a transport-class kill must still be retried to the budget, so
// tightening the classification above did not disable recovery.
func TestTransientPushFailureStillRetries(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "change.txt", "change\n")

	git := &countingPushGit{failWith: "fatal: unable to access 'https://origin/': Connection reset by peer"}
	pr := &fakePRClient{}
	if _, err := Deliver(ctx, repo, git, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "t"})); err == nil {
		t.Fatal("Deliver succeeded despite a failing push")
	}
	if git.attempts != maxPushAttempts {
		t.Errorf("push attempts = %d, want the full budget %d for a transport fault", git.attempts, maxPushAttempts)
	}
}
