package delivery

// Split crash-resume (spec-auto-split-oversized-prs.md §5.2-5.3, revised per
// §10): a split delivery (freshDeliveryCommitSplit) is NOT crash-atomic today
// - the pending record is re-upserted with CommitSHA=C1 before C2 exists, so
// a retry after a mid-split crash mis-routes: window A (after C1, before C2)
// goes to commitWorktreeFollowUp, which commits the deferred files onto the
// pushed branch, and windows B/C (after C2) go to adoptOwnFollowUpCommit,
// which adopts the deferred commit C2 as the delivery commit. Both merge the
// deferred scope into the pushed branch, bypassing the size gate. The fix
// persists DeferredFiles on the record BEFORE C1 and routes these states to a
// dedicated split-resume path that restores the delivered commit C1 and
// (re)creates the deferred branch at C2. These tests seed each window exactly
// as the crashed attempt leaves it and assert the delivered branch never
// carries deferred.txt.

import (
	"context"
	"strings"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestDeferredCommitMessage pins the host-only deferred follow-up message
// shape: subject always the passed-through pr_title, the deferred note always
// present, and - when the workflow renders a commit body - that body appended
// so required trailers (for example Regression/Class/Sweep on a fix subject)
// still satisfy a workspace commit-msg hook on the follow-up commit.
func TestDeferredCommitMessage(t *testing.T) {
	noBody := deferredCommitMessage("fix(agent): retain interrupted turns", "", 2)
	if noBody != "fix(agent): retain interrupted turns\n\ndeferred: 2 file(s) split from this chunk's delivery (automatic follow-up)" {
		t.Fatalf("deferredCommitMessage without a body = %q", noBody)
	}
	withBody := deferredCommitMessage("fix(agent): retain interrupted turns", "Regression: TestX\nClass: none (no class fits)\nSweep: searched deliver/, found 0 further sites", 1)
	if !strings.Contains(withBody, "fix(agent): retain interrupted turns\n\ndeferred: 1 file(s) split from this chunk's delivery (automatic follow-up)") {
		t.Fatalf("deferredCommitMessage with a body lost the subject or the deferred note: %q", withBody)
	}
	if !strings.HasSuffix(withBody, "\n\nRegression: TestX\nClass: none (no class fits)\nSweep: searched deliver/, found 0 further sites") {
		t.Fatalf("deferredCommitMessage with a body must end with the rendered body (trailers last): %q", withBody)
	}
}

// seedSplitCrashState builds the state a crashed split attempt leaves behind:
// C1 (the delivered commit, only essential.txt) authored by the mivia
// delivery identity and recorded with CommitSHA/TreeSHA/DeferredFiles BEFORE
// the split, then the window shape on top (window "A": C2 never existed and
// deferred.txt stays in the worktree; window "B": C2 committed, no deferred
// branch; window "C": C2 committed and the deferred branch already saved).
// It returns C1 and C2 (C2 empty for window A).
func seedSplitCrashState(t *testing.T, repo workflowledger.Repository, run workflowledger.RunSnapshot, worktreeRoot, window string) (c1, c2 string) {
	t.Helper()
	ctx := context.Background()
	writeWorktreeFile(t, worktreeRoot, "essential.txt", "essential change\n")
	writeWorktreeFile(t, worktreeRoot, "deferred.txt", "deferred change\n")
	runGit(t, worktreeRoot, "add", "-A")
	runGit(t, worktreeRoot, "reset", "--", "deferred.txt")
	runGit(t, worktreeRoot, "-c", "user.name=Mivia Agent", "-c", "user.email=noreply@mivia.app",
		"commit", "--allow-empty-message", "-m", "feat: task")
	c1 = runGitOut(t, worktreeRoot, "rev-parse", "HEAD")
	tree := runGitOut(t, worktreeRoot, "rev-parse", "HEAD^{tree}")
	key := DeliveryKey(run.RunID, run.WorkflowDigest)
	if err := repo.UpsertDelivery(ctx, workflowledger.DeliveryRecord{
		RunID: run.RunID, IdempotencyKey: key,
		Mode: "draft", BaseRef: "main", HeadRef: "wf/wt-test",
		Provider: "github", Status: "pending",
		CommitSHA: c1, TreeSHA: tree, DeferredFiles: `["deferred.txt"]`,
	}); err != nil {
		t.Fatal(err)
	}
	switch window {
	case "A":
		// C2 never existed; deferred.txt is still in the worktree.
	case "B", "C":
		runGit(t, worktreeRoot, "add", "--", "deferred.txt")
		runGit(t, worktreeRoot, "-c", "user.name=Mivia Agent", "-c", "user.email=noreply@mivia.app",
			"commit", "--allow-empty-message", "-m", "feat: task\n\ndeferred: 1 file(s) split from this chunk's delivery (automatic follow-up)")
		c2 = runGitOut(t, worktreeRoot, "rev-parse", "HEAD")
		if window == "C" {
			runGit(t, worktreeRoot, "branch", "-f", DeferredBranchName("wf/wt-test"), "HEAD")
		}
	default:
		t.Fatalf("unknown split crash window %q", window)
	}
	return c1, c2
}

// assertSplitResumeOutcome asserts the delivered-branch state a split
// crash-resume must produce: HEAD back on the recorded delivered commit c1,
// the delivered diff exactly essential.txt, the deferred branch carrying
// exactly deferred.txt on top of c1, and origin receiving c1 - never the
// deferred commit.
func assertSplitResumeOutcome(t *testing.T, repoRoot, worktreeRoot, baseCommit string, run workflowledger.RunSnapshot, repo workflowledger.Repository, c1 string) {
	t.Helper()
	head := runGitOut(t, worktreeRoot, "rev-parse", "HEAD")
	if head != c1 {
		t.Fatalf("HEAD after resume = %s, want the delivered commit %s", head, c1)
	}
	if got := runGitOut(t, worktreeRoot, "diff", "--name-only", baseCommit, "HEAD"); got != "essential.txt" {
		t.Fatalf("delivered commit files = %q, want exactly essential.txt (deferred.txt must never reach the delivered branch)", got)
	}
	branch := DeferredBranchName("wf/wt-test")
	if got := runGitOut(t, worktreeRoot, "-c", "core.quotePath=false", "diff", "--name-only", "HEAD", branch); got != "deferred.txt" {
		t.Fatalf("deferred branch diff vs HEAD = %q, want exactly deferred.txt", got)
	}
	if parent := runGitOut(t, worktreeRoot, "rev-parse", branch+"^"); parent != c1 {
		t.Fatalf("deferred branch parent = %q, want the delivered commit %q", parent, c1)
	}
	refs := runGitOut(t, repoRoot, "ls-remote", originURLOf(t, repoRoot))
	if !strings.Contains(refs, "refs/heads/wf/wt-test") {
		t.Fatalf("ls-remote origin lacks refs/heads/wf/wt-test after the resume push:\n%s", refs)
	}
	rec := deliveryRecordByKey(t, repo, run)
	if rec.Status != "succeeded" || rec.CommitSHA != c1 {
		t.Fatalf("record = %+v, want succeeded with CommitSHA %s", rec, c1)
	}
	if rec.StackRemainingCommits != 1 {
		t.Fatalf("StackRemainingCommits = %d, want 1 (the deferred branch must signal a follow-up)", rec.StackRemainingCommits)
	}
}

// originURLOf re-reads the fixture's origin URL from the worktree remote, so
// the resume-outcome helper does not need the fixture's URL parameter.
func originURLOf(t *testing.T, repoRoot string) string {
	t.Helper()
	return runGitOut(t, repoRoot, "config", "--get", "remote.origin.url")
}

// TestDeliverSplitCrashResumeWindowA pins the window-A bug: a crash after C1
// (recorded CommitSHA == head) with the deferred files dirty in the worktree
// previously routed to commitWorktreeFollowUp, which committed the ENTIRE
// worktree - deferred.txt included - onto the pushed branch. The retry must
// instead re-execute C2, save the deferred branch, and leave HEAD on C1.
func TestDeliverSplitCrashResumeWindowA(t *testing.T) {
	ctx := context.Background()
	repoRoot, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	c1, _ := seedSplitCrashState(t, repo, run, worktreeRoot, "A")

	pr := &fakePRClient{}
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "resume"}))
	if err != nil {
		t.Fatalf("Deliver must resume the split, not commit deferred.txt onto the delivered branch: %v", err)
	}
	if res.Status != "succeeded" || res.CommitSHA != c1 {
		t.Fatalf("Result = %+v, want succeeded with CommitSHA %s (C1, never a commit carrying deferred.txt)", res, c1)
	}
	if n := pr.createdCount(); n != 1 {
		t.Fatalf("Create calls = %d, want 1", n)
	}
	assertSplitResumeOutcome(t, repoRoot, worktreeRoot, baseCommit, run, repo, c1)

	// The re-executed deferred commit C2 must carry the same policy-rendered
	// body as the delivered commit, not just the deferred note: a fix subject
	// whose commit-msg hook demands trailers must pass on this follow-up too.
	msg := runGitOut(t, worktreeRoot, "log", "-1", "--format=%B", DeferredBranchName("wf/wt-test"))
	for _, want := range []string{"deferred: 1 file(s) split from this chunk's delivery (automatic follow-up)", "Body."} {
		if !strings.Contains(msg, want) {
			t.Fatalf("deferred branch commit message = %q, missing %q (the policy body must ride the follow-up commit)", msg, want)
		}
	}
}

// TestDeliverSplitCrashResumeWindowB pins the window-B bug: a crash after C2
// existed (recorded CommitSHA == C1, head == C2, no deferred branch yet)
// previously routed to adoptOwnFollowUpCommit, which adopted C2 - the deferred
// commit - as the delivery commit. The retry must instead verify C2, save the
// deferred branch at C2, reset the worktree back to C1, and push C1.
func TestDeliverSplitCrashResumeWindowB(t *testing.T) {
	ctx := context.Background()
	repoRoot, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	c1, c2 := seedSplitCrashState(t, repo, run, worktreeRoot, "B")
	if c2 == "" {
		t.Fatal("test setup: window B must produce a deferred commit C2")
	}

	pr := &fakePRClient{}
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "resume"}))
	if err != nil {
		t.Fatalf("Deliver must resume the split, not adopt C2 as the delivery commit: %v", err)
	}
	if res.Status != "succeeded" || res.CommitSHA != c1 {
		t.Fatalf("Result = %+v, want succeeded with CommitSHA %s (C1; adopting C2 would push deferred.txt)", res, c1)
	}
	if n := pr.createdCount(); n != 1 {
		t.Fatalf("Create calls = %d, want 1", n)
	}
	assertSplitResumeOutcome(t, repoRoot, worktreeRoot, baseCommit, run, repo, c1)
}

// TestDeliverSplitCrashResumeWindowC pins the window-C bug: a crash after the
// deferred branch was saved (git branch -f) but before the worktree was reset
// to C1. Same mis-adoption risk as window B; the retry must recognize the
// deferred branch, keep it at C2, and reset the worktree back to C1.
func TestDeliverSplitCrashResumeWindowC(t *testing.T) {
	ctx := context.Background()
	repoRoot, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	c1, c2 := seedSplitCrashState(t, repo, run, worktreeRoot, "C")
	if c2 == "" {
		t.Fatal("test setup: window C must produce a deferred commit C2")
	}

	pr := &fakePRClient{}
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "resume"}))
	if err != nil {
		t.Fatalf("Deliver must resume the split, not adopt C2 as the delivery commit: %v", err)
	}
	if res.Status != "succeeded" || res.CommitSHA != c1 {
		t.Fatalf("Result = %+v, want succeeded with CommitSHA %s (C1; adopting C2 would push deferred.txt)", res, c1)
	}
	if n := pr.createdCount(); n != 1 {
		t.Fatalf("Create calls = %d, want 1", n)
	}
	assertSplitResumeOutcome(t, repoRoot, worktreeRoot, baseCommit, run, repo, c1)
}

// TestDeliverSplitRetryAfterCompletedSplit pins the completed-split retry: a
// fully executed split (C1 + C2 + deferred branch + worktree reset to C1)
// that failed later (for example at push) must NOT re-execute C2 on the
// retry - the deferred files are no longer in the worktree, so re-staging
// them would error and strand the run. The retry must recognize the restored
// state and deliver C1 as-is. This test passes before and after the fix; it
// guards the resume routing against re-execution.
func TestDeliverSplitRetryAfterCompletedSplit(t *testing.T) {
	ctx := context.Background()
	repoRoot, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	c1, c2 := seedSplitCrashState(t, repo, run, worktreeRoot, "C")
	if c2 == "" {
		t.Fatal("test setup: must produce a deferred commit C2")
	}
	// Complete the split: reset the worktree back to the delivered commit,
	// exactly as freshDeliveryCommitSplit's last step does.
	runGit(t, worktreeRoot, "reset", "--hard", c1)
	if got := runGitOut(t, worktreeRoot, "-c", "core.fsmonitor=false", "status", "--porcelain"); got != "" {
		t.Fatalf("test setup: worktree after the completed split must be clean, got %q", got)
	}

	pr := &fakePRClient{}
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "resume"}))
	if err != nil {
		t.Fatalf("Deliver on a completed split = %v, want the delivered commit reused without re-executing C2", err)
	}
	if res.Status != "succeeded" || res.CommitSHA != c1 {
		t.Fatalf("Result = %+v, want succeeded with CommitSHA %s", res, c1)
	}
	if n := pr.createdCount(); n != 1 {
		t.Fatalf("Create calls = %d, want 1", n)
	}
	assertSplitResumeOutcome(t, repoRoot, worktreeRoot, baseCommit, run, repo, c1)
}
