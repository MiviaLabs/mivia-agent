package cliworkflow

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// newDeliveryFixture builds the write-capable two-step fixture: write_file
// agents, a draft delivery policy, a bare origin, and a recording PR client.
func newDeliveryFixture(t *testing.T) (root, storePath, config string, recorder *recordingPRClient) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"{\"ok\":true}"}}]}`)
	}))
	t.Cleanup(server.Close)
	root = t.TempDir()
	storePath = filepath.Join(root, "workflow.db")
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	writeWorkflowRunFixture(t, root, server.URL, storePath)
	setWorkflowAgentTools(t, root, "write_file")
	appendWorkflowDeliveryPolicy(t, root, "draft")
	initWorkflowGitRepoWithOrigin(t, root)
	recorder = &recordingPRClient{}
	originalNewPR := WorkflowDeliverNewPR
	WorkflowDeliverNewPR = func() delivery.PRClient { return recorder }
	t.Cleanup(func() { WorkflowDeliverNewPR = originalNewPR })
	return root, storePath, filepath.Join(root, "config.toml"), recorder
}

// runFixtureToDeliveryPending runs the two-step fixture without
// --allow-publish and returns the run ID parked at delivery_pending.
func runFixtureToDeliveryPending(t *testing.T, root, config string) string {
	t.Helper()
	var stdout strings.Builder
	if err := RunWorkflowWithIO([]string{"run", "two-step", "--workspace", root, "--config", config, "--input", "task=compile"}, &stdout, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "status=delivery_pending") {
		t.Fatalf("run stdout = %q, want status=delivery_pending", stdout.String())
	}
	return strings.TrimPrefix(strings.Fields(stdout.String())[0], "run_id=")
}

// openDeliveryStore opens the fixture store and returns its repository.
func openDeliveryStore(t *testing.T, storePath string) workflowledger.Repository {
	t.Helper()
	store, err := OpenContextStorePath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return workflowledger.NewStorageRepository(store)
}

// seedWorktreeChange writes change.txt into the run's worktree so delivery
// has a diff to publish.
func seedWorktreeChange(t *testing.T, root, runID string, repo workflowledger.Repository) {
	t.Helper()
	run, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.WorktreeName == "" {
		t.Fatalf("run %q has no worktree name: %+v", runID, run)
	}
	worktree, err := vcs.Resolve(context.Background(), root, run.WorktreeName)
	if err != nil || worktree == nil {
		t.Fatalf("resolve run worktree = %+v, %v", worktree, err)
	}
	if err := os.WriteFile(filepath.Join(worktree.Path, "change.txt"), []byte("seeded change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestWorkflowDeliverCommandHappyPath: a delivery_pending run with a seeded
// change publishes exactly one draft PR, settles to succeeded, and the origin
// receives the wf/<worktree> branch.
func TestWorkflowDeliverCommandHappyPath(t *testing.T) {
	root, storePath, config, prRecorder := newDeliveryFixture(t)
	runID := runFixtureToDeliveryPending(t, root, config)
	repo := openDeliveryStore(t, storePath)
	run, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	seedWorktreeChange(t, root, runID, repo)
	var stdout strings.Builder
	err = RunWorkflowWithIO([]string{"deliver", runID, "--workspace", root, "--config", config, "--allow-publish"}, &stdout, io.Discard)
	if err != nil {
		t.Fatalf("deliver error = %v; stdout = %q", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "status=succeeded") || !strings.Contains(stdout.String(), "PR created") {
		t.Fatalf("deliver stdout = %q, want status=succeeded and PR created", stdout.String())
	}
	if creates, finds := prRecorder.calls(); creates != 1 || finds != 1 {
		t.Fatalf("PR client calls: creates=%d finds=%d, want one of each", creates, finds)
	}
	if drafts := prRecorder.draftCreates(); drafts != 1 {
		t.Fatalf("draft PR creates = %d, want 1 (mode=draft)", drafts)
	}
	rec, err := repo.GetDeliveryByIdempotencyKey(context.Background(), delivery.DeliveryKey(runID, run.WorkflowDigest))
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != "succeeded" {
		t.Fatalf("delivery record status = %q, want succeeded", rec.Status)
	}
	refs, err := exec.Command("git", "ls-remote", filepath.Join(root, "origin.git")).Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(refs), "refs/heads/wf/"+run.WorktreeName) {
		t.Fatalf("origin refs = %q, want refs/heads/wf/%s", refs, run.WorktreeName)
	}
}

// TestWorkflowDeliverRequiresAllowPublish: delivery without --allow-publish is
// refused with zero PR calls and the run stays delivery_pending.
func TestWorkflowDeliverRequiresAllowPublish(t *testing.T) {
	root, storePath, config, prRecorder := newDeliveryFixture(t)
	runID := runFixtureToDeliveryPending(t, root, config)
	err := RunWorkflowWithIO([]string{"deliver", runID, "--workspace", root, "--config", config}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "--allow-publish") {
		t.Fatalf("deliver error = %v, want an --allow-publish refusal", err)
	}
	if creates, finds := prRecorder.calls(); creates != 0 || finds != 0 {
		t.Fatalf("PR client calls: creates=%d finds=%d, want zero", creates, finds)
	}
	repo := openDeliveryStore(t, storePath)
	run, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("run status = %q, want delivery_pending", run.Status)
	}
}

// TestWorkflowDeliverRefusesNonDeliveryPending: delivering a read-only run
// with no policy (settled to succeeded) is refused with zero PR calls.
func TestWorkflowDeliverRefusesNonDeliveryPending(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"{\"ok\":true}"}}]}`)
	}))
	t.Cleanup(server.Close)
	root := t.TempDir()
	storePath := filepath.Join(root, "workflow.db")
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	writeWorkflowRunFixture(t, root, server.URL, storePath)
	prRecorder := &recordingPRClient{}
	originalNewPR := WorkflowDeliverNewPR
	WorkflowDeliverNewPR = func() delivery.PRClient { return prRecorder }
	t.Cleanup(func() { WorkflowDeliverNewPR = originalNewPR })
	config := filepath.Join(root, "config.toml")
	var runOut strings.Builder
	if err := RunWorkflowWithIO([]string{"run", "two-step", "--workspace", root, "--config", config, "--input", "task=compile"}, &runOut, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(runOut.String(), "status=succeeded") {
		t.Fatalf("run stdout = %q, want status=succeeded", runOut.String())
	}
	runID := strings.TrimPrefix(strings.Fields(runOut.String())[0], "run_id=")
	err := RunWorkflowWithIO([]string{"deliver", runID, "--workspace", root, "--config", config, "--allow-publish"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "delivery") {
		t.Fatalf("deliver error = %v, want a delivery refusal for a non-delivery_pending run", err)
	}
	if creates, finds := prRecorder.calls(); creates != 0 || finds != 0 {
		t.Fatalf("PR client calls: creates=%d finds=%d, want zero", creates, finds)
	}
	repo := openDeliveryStore(t, storePath)
	run, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run status = %q, want succeeded (unchanged)", run.Status)
	}
}

// TestWorkflowDeliverClaimFencing pins the AR-1 lease behavior: a held,
// unexpired claim means another host is (or was recently) publishing - deliver
// refuses instead of force-releasing, so two hosts can never publish the same
// run. --force is the explicit bypass and then delivers exactly one PR.
func TestWorkflowDeliverClaimFencing(t *testing.T) {
	root, storePath, config, prRecorder := newDeliveryFixture(t)
	runID := runFixtureToDeliveryPending(t, root, config)
	repo := openDeliveryStore(t, storePath)
	seedWorktreeChange(t, root, runID, repo)
	if err := repo.ClaimRun(context.Background(), runID, "other-holder"); err != nil {
		t.Fatal(err)
	}

	// Without --force: a fresh claim is a live publisher -> refuse, no PR.
	err := RunWorkflowWithIO([]string{"deliver", runID, "--workspace", root, "--config", config, "--allow-publish"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "claim") {
		t.Fatalf("deliver error = %v, want a held-claim refusal without --force", err)
	}
	if creates, finds := prRecorder.calls(); creates != 0 || finds != 0 {
		t.Fatalf("PR client calls after refused claim: creates=%d finds=%d, want zero", creates, finds)
	}

	// With --force: the operator explicitly takes the claim over.
	var stdout strings.Builder
	err = RunWorkflowWithIO([]string{"deliver", runID, "--workspace", root, "--config", config, "--allow-publish", "--force"}, &stdout, io.Discard)
	if err != nil {
		t.Fatalf("deliver --force error = %v; stdout = %q", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "status=succeeded") {
		t.Fatalf("deliver stdout = %q, want status=succeeded", stdout.String())
	}
	if creates, _ := prRecorder.calls(); creates != 1 {
		t.Fatalf("PR creates = %d, want 1", creates)
	}
	run, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run status = %q, want succeeded after --force claim takeover", run.Status)
	}
}

// TestWorkflowDeliverAlreadyDelivered: a second deliver on a succeeded run
// replays the durable record without creating another PR.
func TestWorkflowDeliverAlreadyDelivered(t *testing.T) {
	root, storePath, config, prRecorder := newDeliveryFixture(t)
	runID := runFixtureToDeliveryPending(t, root, config)
	repo := openDeliveryStore(t, storePath)
	seedWorktreeChange(t, root, runID, repo)
	args := []string{"deliver", runID, "--workspace", root, "--config", config, "--allow-publish"}
	if err := RunWorkflowWithIO(args, io.Discard, io.Discard); err != nil {
		t.Fatalf("first deliver error = %v", err)
	}
	createsAfterFirst, _ := prRecorder.calls()
	if createsAfterFirst != 1 {
		t.Fatalf("first deliver PR creates = %d, want 1", createsAfterFirst)
	}
	var second strings.Builder
	if err := RunWorkflowWithIO(args, &second, io.Discard); err != nil {
		t.Fatalf("second deliver error = %v; stdout = %q", err, second.String())
	}
	if createsAfterSecond, _ := prRecorder.calls(); createsAfterSecond != createsAfterFirst {
		t.Fatalf("PR creates = %d after second deliver, want %d (no additional creation)", createsAfterSecond, createsAfterFirst)
	}
	if !strings.Contains(second.String(), "status=succeeded") {
		t.Fatalf("second deliver stdout = %q, want status=succeeded", second.String())
	}
}

// TestWorkflowRunGrantNoDiff: run --allow-publish on an unchanged worktree
// settles through the no_diff path with zero PR creates.
func TestWorkflowRunGrantNoDiff(t *testing.T) {
	root, storePath, config, prRecorder := newDeliveryFixture(t)
	var stdout strings.Builder
	err := RunWorkflowWithIO([]string{"run", "two-step", "--workspace", root, "--config", config, "--input", "task=compile", "--allow-publish"}, &stdout, io.Discard)
	if err != nil {
		t.Fatalf("run --allow-publish error = %v; stdout = %q", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "status=succeeded") || !strings.Contains(stdout.String(), "no diff to publish") {
		t.Fatalf("stdout = %q, want status=succeeded and the no-diff outcome", stdout.String())
	}
	if creates, finds := prRecorder.calls(); creates != 0 || finds != 0 {
		t.Fatalf("PR client calls: creates=%d finds=%d, want zero", creates, finds)
	}
	runID := strings.TrimPrefix(strings.Fields(stdout.String())[0], "run_id=")
	repo := openDeliveryStore(t, storePath)
	run, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run status = %q, want succeeded (no_diff settlement)", run.Status)
	}
	rec, err := repo.GetDeliveryByIdempotencyKey(context.Background(), delivery.DeliveryKey(runID, run.WorkflowDigest))
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != "no_diff" {
		t.Fatalf("delivery record status = %q, want no_diff", rec.Status)
	}
}

// TestWorkflowDeliverTimesOutHungGit: a git command that never returns must
// be cancelled by the delivery timeout instead of blocking the CLI forever;
// a transient execution failure must NOT settle the run permanently - it
// stays delivery_pending (retryable) and no PR is created.
func TestWorkflowDeliverTimesOutHungGit(t *testing.T) {
	root, storePath, config, prRecorder := newDeliveryFixture(t)
	runID := runFixtureToDeliveryPending(t, root, config)
	repo := openDeliveryStore(t, storePath)
	seedWorktreeChange(t, root, runID, repo)

	originalGit := WorkflowDeliverGit
	originalTimeout := WorkflowDeliveryTimeout
	WorkflowDeliverGit = blockingGitRunner{}
	WorkflowDeliveryTimeout = 100 * time.Millisecond
	t.Cleanup(func() {
		WorkflowDeliverGit = originalGit
		WorkflowDeliveryTimeout = originalTimeout
	})

	start := time.Now()
	err := RunWorkflowWithIO([]string{"deliver", runID, "--workspace", root, "--config", config, "--allow-publish"}, io.Discard, io.Discard)
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("deliver blocked for %v, want the delivery timeout to cancel hung git", elapsed)
	}
	if err == nil {
		t.Fatal("deliver error = nil, want a timeout failure")
	}
	if creates, finds := prRecorder.calls(); creates != 0 || finds != 0 {
		t.Fatalf("PR client calls: creates=%d finds=%d, want zero", creates, finds)
	}
	run, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	// A hung git command is a RECOVERABLE execution failure, not a verified
	// refusal: the run must stay delivery_pending so a later attempt can
	// retry, never settle delivery_failed (which is irreversible).
	if run.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("run status = %q, want delivery_pending (retryable) after the eligibility timeout", run.Status)
	}
}

// blockingGitRunner is a GitRunner whose commands never return until the
// context is cancelled.
type blockingGitRunner struct{ delivery.GitRunner }

func (blockingGitRunner) Run(ctx context.Context, _ delivery.GitContext, _ ...string) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

// TestWorkflowDeliverRefusalPrintsDeliveryFailedStatus: a permanent refusal
// (the REMOTE base was rewritten since admission - a base REWRITE, not a
// forward advance) settles the run to delivery_failed, durably records the
// reason, prints the settled status line, and creates no PR.
func TestWorkflowDeliverRefusalPrintsDeliveryFailedStatus(t *testing.T) {
	root, storePath, config, prRecorder := newDeliveryFixture(t)
	runID := runFixtureToDeliveryPending(t, root, config)
	repo := openDeliveryStore(t, storePath)
	seedWorktreeChange(t, root, runID, repo)
	// Rewrite the REMOTE base: force-replace origin main with an orphan
	// commit that drops the admitted base from history, so delivery refuses.
	// (A merely advanced base is now accepted - see
	// TestWorkflowDeliverReopensDeliveryFailed.)
	rewriteFixtureOriginMain(t, root)
	var stdout strings.Builder
	err := RunWorkflowWithIO([]string{"deliver", runID, "--workspace", root, "--config", config, "--allow-publish"}, &stdout, io.Discard)
	if err == nil {
		t.Fatalf("deliver error = nil, want a refusal; stdout = %q", stdout.String())
	}
	if !strings.Contains(err.Error(), "delivery base") {
		t.Fatalf("deliver error = %v, want a base-rewritten refusal", err)
	}
	if !strings.Contains(stdout.String(), "status=delivery_failed") {
		t.Fatalf("deliver stdout = %q, want the settled status=delivery_failed line", stdout.String())
	}
	if creates, finds := prRecorder.calls(); creates != 0 || finds != 0 {
		t.Fatalf("PR client calls: creates=%d finds=%d, want zero", creates, finds)
	}
	run, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusDeliveryFailed {
		t.Fatalf("run status = %q, want delivery_failed after a permanent refusal", run.Status)
	}
	// The refusal reason must be durable so `workflow status` explains the
	// failure and the operator knows the run is recoverable.
	rec, err := repo.GetDeliveryByIdempotencyKey(context.Background(), delivery.DeliveryKey(runID, run.WorkflowDigest))
	if err != nil {
		t.Fatalf("no durable delivery record after the refusal: %v", err)
	}
	if rec.Status != "failed" || rec.ErrorRef == "" {
		t.Fatalf("delivery record = %+v, want failed with a recorded ErrorRef", rec)
	}
	body, err := repo.LoadContent(context.Background(), rec.ErrorRef)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "delivery base") {
		t.Fatalf("recorded refusal reason = %q, want it to name the base rewrite", body)
	}
}

// rewriteFixtureOriginMain force-replaces the fixture origin's main branch with
// an orphan commit that drops the admitted base from remote history.
func rewriteFixtureOriginMain(t *testing.T, root string) {
	t.Helper()
	for _, args := range [][]string{
		{"checkout", "--orphan", "orphan-main"},
		{"commit", "--allow-empty", "-m", "rewritten base"},
		{"push", "--force", "origin", "orphan-main:refs/heads/main"},
		{"checkout", "main"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}

// TestWorkflowDeliverReopensDeliveryFailed pins the CLI recovery path: a run
// settled delivery_failed is re-opened by workflow deliver, eligibility is
// re-run against the CURRENT state, and - once the base advanced forward
// (the original failure mode) - the PR is produced and the run settles
// succeeded.
func TestWorkflowDeliverReopensDeliveryFailed(t *testing.T) {
	root, storePath, config, prRecorder := newDeliveryFixture(t)
	runID := runFixtureToDeliveryPending(t, root, config)
	repo := openDeliveryStore(t, storePath)
	run, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(context.Background(), runID, run.Version, workflowledger.RunStatusDeliveryFailed, nil); err != nil {
		t.Fatalf("CAS to delivery_failed: %v", err)
	}
	seedWorktreeChange(t, root, runID, repo)
	// Advance the remote base forward (the normal mid-run development case).
	for _, args := range [][]string{
		{"add", "base-advance.txt"},
		{"commit", "-m", "advance base"},
		{"push", "origin", "main"},
	} {
		if args[0] == "add" {
			if err := os.WriteFile(filepath.Join(root, "base-advance.txt"), []byte("new\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	var stdout strings.Builder
	err = RunWorkflowWithIO([]string{"deliver", runID, "--workspace", root, "--config", config, "--allow-publish"}, &stdout, io.Discard)
	if err != nil {
		t.Fatalf("deliver on a delivery_failed run = %v; stdout = %q, want re-eligibility success", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "status=succeeded") {
		t.Fatalf("deliver stdout = %q, want status=succeeded", stdout.String())
	}
	if creates, _ := prRecorder.calls(); creates != 1 {
		t.Fatalf("PR creates = %d, want 1", creates)
	}
	run, err = repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run status = %q, want succeeded after re-eligibility delivery", run.Status)
	}
}
