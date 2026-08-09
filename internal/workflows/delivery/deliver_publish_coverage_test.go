package delivery

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// failUpsertRepo wraps a ledger.Repository and injects a failure into
// UpsertDelivery for records matched by failWhen. Every other repository
// method delegates to the wrapped repository, so the full delivery flow runs
// against real storage and only the targeted record write fails.
type failUpsertRepo struct {
	workflowledger.Repository
	failWhen func(d workflowledger.DeliveryRecord) bool
	failErr  error
}

func (f *failUpsertRepo) UpsertDelivery(ctx context.Context, d workflowledger.DeliveryRecord) error {
	if f.failWhen != nil && f.failWhen(d) {
		return f.failErr
	}
	return f.Repository.UpsertDelivery(ctx, d)
}

// TestCoveragePushDeliveryBranchPushedUpsertFailure executes the
// record-write failure branch of pushDeliveryBranch (deliver_publish.go:32-34):
// the branch push succeeds but the durable pushed record cannot be written.
// The attempt fails with the injected error, the PR client is never consulted,
// and no failed record is written (the code returns before markFailed) - the
// last durable record stays the pending one and the "failed" stage does not
// fire.
func TestCoveragePushDeliveryBranchPushedUpsertFailure(t *testing.T) {
	ctx := context.Background()
	repoRoot, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")

	failing := &failUpsertRepo{
		Repository: repo,
		failErr:    errors.New("store: pushed record upsert failed"),
		failWhen: func(d workflowledger.DeliveryRecord) bool {
			// The pushed record written inside pushDeliveryBranch carries no
			// PR identity yet (RemoteID is carried from the existing record,
			// empty on a fresh attempt). The later pushed record carrying the
			// PR identity (deliver.go:398) must NOT fail here.
			return d.Status == "pushed" && d.RemoteID == ""
		},
	}

	var stages []string
	req := newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"})
	req.Stage = func(stage, detail string) { stages = append(stages, stage) }
	pr := &fakePRClient{}

	res, err := Deliver(ctx, failing, RealGit{}, pr, req)
	if err == nil || IsRefusal(err) {
		t.Fatalf("Deliver err = %v, want a transient (non-refusal) upsert failure", err)
	}
	if !strings.Contains(err.Error(), "store: pushed record upsert failed") {
		t.Fatalf("Deliver err = %v, want the injected pushed-record failure", err)
	}
	if res != (Result{}) {
		t.Fatalf("Result = %+v, want zero value on failure", res)
	}
	// The push itself ran before the record write failed, so the branch
	// reached origin.
	if refs := runGitOut(t, repoRoot, "ls-remote", originURL); !strings.Contains(refs, "refs/heads/wf/wt-test") {
		t.Fatalf("ls-remote origin lacks refs/heads/wf/wt-test after the push:\n%s", refs)
	}
	// Failure precedes any PR lookup/create.
	assertZeroCreates(t, pr)
	// No failed record: pushDeliveryBranch returns before markFailed, so the
	// last durable record is the pending one.
	rec := deliveryRecordByKey(t, failing, run)
	if rec.Status != "pending" {
		t.Fatalf("record status = %q, want pending (failure happened writing the pushed record)", rec.Status)
	}
	// The failed stage must NOT fire on this path: deliver_publish.go:33
	// returns before markFailed and stage("failed").
	want := []string{"guard", "eligibility", "commit", "push"}
	if !reflect.DeepEqual(stages, want) {
		t.Fatalf("stages = %v, want %v", stages, want)
	}
}

// TestCoveragePushAndPublishPushedPRRecordUpsertFailure executes the
// pushed-record failure branch of pushAndPublish (deliver.go:391-402): the PR
// is created, but the durable pushed record carrying the PR identity cannot be
// written. markFailed persists a failed record (preserving the attempt's
// resume data) and the "failed" stage fires at deliver.go:400 before the error
// returns.
func TestCoveragePushAndPublishPushedPRRecordUpsertFailure(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")

	failing := &failUpsertRepo{
		Repository: repo,
		failErr:    errors.New("store: pushed PR record upsert failed"),
		failWhen: func(d workflowledger.DeliveryRecord) bool {
			// Only the pushed record that carries the freshly created PR
			// identity (deliver.go:398) fails; the plain pushed record from
			// pushDeliveryBranch and the failed record from markFailed pass.
			return d.Status == "pushed" && d.RemoteID != ""
		},
	}

	var stages []string
	req := newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"})
	req.Stage = func(stage, detail string) { stages = append(stages, stage) }
	pr := &fakePRClient{}

	res, err := Deliver(ctx, failing, RealGit{}, pr, req)
	if err == nil || IsRefusal(err) {
		t.Fatalf("Deliver err = %v, want a transient (non-refusal) upsert failure", err)
	}
	if !strings.Contains(err.Error(), "store: pushed PR record upsert failed") {
		t.Fatalf("Deliver err = %v, want the injected pushed-PR-record failure", err)
	}
	if res != (Result{}) {
		t.Fatalf("Result = %+v, want zero value on failure", res)
	}
	// The PR was created before the durable record write failed.
	if n := pr.createdCount(); n != 1 {
		t.Fatalf("Create calls = %d, want 1 (PR created before the record failure)", n)
	}
	// markFailed persisted the failed record with the attempt's resume data.
	rec := deliveryRecordByKey(t, failing, run)
	if rec.Status != "failed" {
		t.Fatalf("record status = %q, want failed", rec.Status)
	}
	if rec.CommitSHA == "" || rec.TreeSHA == "" {
		t.Fatalf("failed record = %+v, want CommitSHA and TreeSHA preserved for the retry", rec)
	}
	// The "failed" stage fired at deliver.go:400 after push and pr.
	want := []string{"guard", "eligibility", "commit", "push", "pr", "failed"}
	if !reflect.DeepEqual(stages, want) {
		t.Fatalf("stages = %v, want %v", stages, want)
	}
}

// TestCoverageFindOrCreatePRTitleRenderFailure executes the title-render
// failure branch of findOrCreatePR (deliver.go:430-435): a non-empty title
// template that renders to whitespace folds to an empty title, which
// RenderTitle rejects. The failure happens before any PR API call; markFailed
// persists a failed record and the "failed" stage fires at deliver.go:433.
func TestCoverageFindOrCreatePRTitleRenderFailure(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")

	pol := defaultPolicy("draft")
	// A non-empty title template that renders to whitespace folds to an empty
	// title, which RenderTitle rejects ("title_template rendered an empty
	// title"). The commit-message template stays valid, so the commit path
	// completes and the failure lands exactly in findOrCreatePR.
	pol.TitleTemplate = "{{ inputs.blank }}"

	var stages []string
	req := newRequest(run, gc, baseCommit, originURL, pol, map[string]string{"task": "x", "blank": "   "})
	req.Stage = func(stage, detail string) { stages = append(stages, stage) }
	pr := &fakePRClient{}

	res, err := Deliver(ctx, repo, RealGit{}, pr, req)
	if err == nil || IsRefusal(err) {
		t.Fatalf("Deliver err = %v, want the title render failure (plain, non-refusal error)", err)
	}
	if !strings.Contains(err.Error(), "rendered an empty title") {
		t.Fatalf("Deliver err = %v, want the empty-title render failure", err)
	}
	if res != (Result{}) {
		t.Fatalf("Result = %+v, want zero value on failure", res)
	}
	// The failure precedes any PR lookup/create.
	assertZeroCreates(t, pr)
	// markFailed persisted the failed record.
	rec := deliveryRecordByKey(t, repo, run)
	if rec.Status != "failed" {
		t.Fatalf("record status = %q, want failed", rec.Status)
	}
	// The "failed" stage fired at deliver.go:433 after push and pr.
	want := []string{"guard", "eligibility", "commit", "push", "pr", "failed"}
	if !reflect.DeepEqual(stages, want) {
		t.Fatalf("stages = %v, want %v", stages, want)
	}
}
