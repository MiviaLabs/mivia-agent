package delivery

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeliverReadyReusesReadyExistingPRForReadyDelivery(t *testing.T) {
	// A ready-mode delivery that already created a ready PR (earlier attempt
	// failed after publication) must resume it, not refuse its own PR.
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	pr := &fakePRClient{found: &PRRef{RemoteID: "12", URL: "https://example.com/pull/12"}}

	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("ready"), map[string]string{"task": "x"}))
	if err != nil {
		t.Fatalf("ready delivery must resume its own ready PR: %v", err)
	}
	if res.RemoteID != "12" {
		t.Fatalf("Result = %+v, want existing ready PR reuse", res)
	}
	assertZeroCreates(t, pr)
}

func TestDeliverRunsPrePushHook(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	hooksDir := filepath.Join(t.TempDir(), "hooks")
	if err := os.Mkdir(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hook := "#!/bin/sh\necho pre-push blocked >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(hooksDir, "pre-push"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, worktreeRoot, "config", "core.hooksPath", hooksDir)

	pr := &fakePRClient{}
	_, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"}))
	if err == nil || !strings.Contains(err.Error(), "pre-push blocked") {
		t.Fatalf("Deliver error = %v, want failing pre-push hook error", err)
	}
	assertZeroCreates(t, pr)
}

func TestDeliverRunsCommitHooks(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	hooksDir := filepath.Join(t.TempDir(), "hooks")
	if err := os.Mkdir(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hook := "#!/bin/sh\necho commit blocked >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, worktreeRoot, "config", "core.hooksPath", hooksDir)

	pr := &fakePRClient{}
	_, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"}))
	if err == nil || !strings.Contains(err.Error(), "commit blocked") {
		t.Fatalf("Deliver error = %v, want failing pre-commit hook error", err)
	}
	assertZeroCreates(t, pr)
}

func TestDeliverRefusesStagedTreeDrift(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	pr := &fakePRClient{}

	_, err := Deliver(ctx, repo, stageMutationRunner{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"}))
	if err == nil || !strings.Contains(err.Error(), "staged tree changed before commit") {
		t.Fatalf("Deliver error = %v, want staged-tree drift refusal", err)
	}
	if got := runGitOut(t, worktreeRoot, "rev-parse", "HEAD"); got != baseCommit {
		t.Fatalf("HEAD = %s, want uncommitted base %s", got, baseCommit)
	}
	assertZeroCreates(t, pr)
}

// stageMutationRunner changes the index after delivery snapshots it.
type stageMutationRunner struct{}

func (stageMutationRunner) Run(ctx context.Context, gc GitContext, args ...string) (string, error) {
	out, err := (RealGit{}).Run(ctx, gc, args...)
	if err != nil || len(args) == 0 || args[len(args)-1] != "write-tree" {
		return out, err
	}
	if err := os.WriteFile(filepath.Join(gc.Dir, "b.txt"), []byte("drift\n"), 0o644); err != nil {
		return "", err
	}
	_, err = (RealGit{}).Run(ctx, gc, "add", "b.txt")
	return out, err
}
