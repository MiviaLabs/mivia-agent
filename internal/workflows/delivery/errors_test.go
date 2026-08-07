package delivery

import (
	"context"
	"errors"
	"sync"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestMarkFailedPreservesPRIdentity is the regression test for markFailed:
// a failed attempt that follows a pushed record carrying the run's PR must
// carry RemoteID and URL forward (mirroring the pushed record in
// pushAndPublish), so the retry's ownership guard (findOrCreatePR) can still
// recognize the run's own PR after a transient failure instead of refusing it
// as foreign.
func TestMarkFailedPreservesPRIdentity(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	req := newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"})
	key := DeliveryKey(run.RunID, run.WorkflowDigest)

	// The crashed attempt left a pushed record carrying the PR identity
	// (deliver.go step 15b) before the transient failure occurred.
	if err := repo.UpsertDelivery(ctx, workflowledger.DeliveryRecord{
		RunID:          run.RunID,
		IdempotencyKey: key,
		Mode:           "draft",
		BaseRef:        "main",
		HeadRef:        "wf/wt-test",
		Provider:       "github",
		Status:         "pushed",
		CommitSHA:      "c0ffee",
		TreeSHA:        "tree",
		DiffRef:        "diff",
		RemoteID:       "42",
		URL:            "https://github.com/x/y/pull/42",
	}); err != nil {
		t.Fatal(err)
	}

	markFailed(ctx, repo, key, req, errors.New("find: transient failure"))

	rec := deliveryRecordByKey(t, repo, run)
	if rec.Status != "failed" {
		t.Fatalf("record status = %q, want failed", rec.Status)
	}
	if rec.RemoteID != "42" || rec.URL != "https://github.com/x/y/pull/42" {
		t.Fatalf("failed record = %+v, want RemoteID 42 and URL preserved from the pushed record", rec)
	}
	if rec.CommitSHA != "c0ffee" || rec.TreeSHA != "tree" || rec.DiffRef != "diff" {
		t.Fatalf("failed record = %+v, want CommitSHA/TreeSHA/DiffRef preserved", rec)
	}
}

// failOnceFindPRClient fails the first FindByHead call (a transient PR lookup
// failure on a retry), then delegates; Create delegates to the fake.
type failOnceFindPRClient struct {
	fake   *fakePRClient
	mu     sync.Mutex
	failed bool
}

func (f *failOnceFindPRClient) FindByHead(ctx context.Context, repo, head string) (*PRRef, error) {
	f.mu.Lock()
	if !f.failed {
		f.failed = true
		f.mu.Unlock()
		return nil, errors.New("find: transient failure")
	}
	f.mu.Unlock()
	return f.fake.FindByHead(ctx, repo, head)
}

func (f *failOnceFindPRClient) Create(ctx context.Context, repo string, in PRInput) (PRRef, error) {
	return f.fake.Create(ctx, repo, in)
}

// TestDeliverRetryReusesOwnPRAfterTransientFindFailure reproduces the
// ownership-guard regression end to end: a run whose earlier attempt created
// the PR and recorded its identity, then hit a transient PR lookup failure on
// retry. The failed record must keep RemoteID/URL so the next retry still
// recognizes the run's own PR (even when its draft state no longer matches)
// instead of refusing it as foreign and permanently CASing the run to
// delivery_failed.
func TestDeliverRetryReusesOwnPRAfterTransientFindFailure(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	// The delivery commit recorded by the crashed attempt.
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	runGit(t, worktreeRoot, "add", "-A")
	runGit(t, worktreeRoot, "commit", "-m", "x")
	head := runGitOut(t, worktreeRoot, "rev-parse", "HEAD")
	tree := runGitOut(t, worktreeRoot, "rev-parse", "HEAD^{tree}")
	req := newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"})
	key := DeliveryKey(run.RunID, run.WorkflowDigest)
	// The crashed attempt created the PR and recorded its identity before dying.
	if err := repo.UpsertDelivery(ctx, workflowledger.DeliveryRecord{
		RunID:          run.RunID,
		IdempotencyKey: key,
		Mode:           "draft",
		BaseRef:        "main",
		HeadRef:        "wf/wt-test",
		Provider:       "github",
		Status:         "pushed",
		CommitSHA:      head,
		TreeSHA:        tree,
		DiffRef:        "diff",
		RemoteID:       "42",
		URL:            "https://github.com/x/y/pull/42",
	}); err != nil {
		t.Fatal(err)
	}
	fake := &fakePRClient{found: &PRRef{RemoteID: "42", URL: "https://github.com/x/y/pull/42", Draft: false}}
	pr := &failOnceFindPRClient{fake: fake}

	// Retry 1: the transient FindByHead failure marks the attempt failed.
	if _, err := Deliver(ctx, repo, RealGit{}, pr, req); err == nil || IsRefusal(err) {
		t.Fatalf("first retry err = %v, want a transient (non-refusal) error", err)
	}
	rec := deliveryRecordByKey(t, repo, run)
	if rec.Status != "failed" || rec.RemoteID != "42" {
		t.Fatalf("record after transient failure = %+v, want failed keeping RemoteID 42", rec)
	}

	// Retry 2: the PR's draft state no longer matches (Draft=false in a draft
	// delivery), but it is the run's own PR, so the ownership guard must
	// reuse it instead of refusing.
	res, err := Deliver(ctx, repo, RealGit{}, pr, req)
	if err != nil {
		t.Fatalf("second retry must reuse the run's own PR, got error: %v", err)
	}
	if res.RemoteID != "42" || res.URL != "https://github.com/x/y/pull/42" {
		t.Fatalf("Result = %+v, want reuse of own PR 42", res)
	}
	if n := fake.createdCount(); n != 0 {
		t.Fatalf("Create calls = %d, want 0 (own PR reused)", n)
	}
}
