package delivery

// Split delivery (spec-auto-split-oversized-prs.md §5.2-5.3, revised per
// §10): checkChunkDiffSize's own host-computed split decision (never an
// agent's claim) splits the fresh commit into a delivered commit (pushed)
// and a deferred commit (saved under DeferredBranchName, never pushed on
// the delivered branch). Uses the same real-git fixture as deliver_test.go,
// not a fake GitRunner - the git plumbing (add/reset/commit/branch/reset
// --hard) is exactly what's under test here.
//
// TestDeliverSplitsCommitWhenDeferredFilesPresent and
// TestDeliverNoSplitWhenDeferredFilesAbsent exercise
// freshDeliveryCommit(Split) directly via a pre-set InputDeferredFiles input,
// independent of how that input gets populated - the commit-splitting
// mechanics are the same regardless of source. The size-gate tests below
// exercise the real producer (checkChunkDiffSize's deterministic split) end
// to end: no test ever pre-sets InputDeferredFiles to influence the gate,
// because the gate does not honor a pre-existing value - only its own
// measurement decides.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestDeliverSplitEmptyTemplateFallsBackToBody pins the empty-render fallback
// in commitDeferredFollowUp: when CommitMessageTemplate is empty (or renders
// to whitespace), RenderCommitMessage returns empty and the deferred commit
// message must fall back to the PR body rather than carry no body at all. A
// fix subject whose commit-msg hook demands trailers (Regression/Class/Sweep)
// must still pass on this follow-up commit.
func TestDeliverSplitEmptyTemplateFallsBackToBody(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "essential.txt", "essential change\n")
	writeWorktreeFile(t, worktreeRoot, "deferred.txt", "deferred change\n")

	trailers := "Regression: TestEmptyTemplate\nClass: none\nSweep: 0 sites"
	deferredJSON, err := json.Marshal([]string{"deferred.txt"})
	if err != nil {
		t.Fatal(err)
	}
	policy := Policy{
		Kind:                  "pull_request",
		Mode:                  "draft",
		Provider:              "github",
		Base:                  "main",
		TitleTemplate:         "fix(agent): empty template fallback",
		CommitMessageTemplate: "",
	}
	req := newRequest(run, gc, baseCommit, originURL, policy,
		map[string]string{"task": "x", InputDeferredFiles: string(deferredJSON)})
	key := DeliveryKey(run.RunID, run.WorkflowDigest)
	title := "fix(agent): empty template fallback"
	body := trailers
	deferredFiles, err := ParseDeferredFiles(string(deferredJSON))
	if err != nil {
		t.Fatalf("ParseDeferredFiles: %v", err)
	}

	c1, _, cerr := freshDeliveryCommitSplit(ctx, repo, RealGit{}, req, key, "", deferredFiles, title, body)
	if cerr != nil {
		t.Fatalf("freshDeliveryCommitSplit: %v", cerr)
	}
	if c1 == "" {
		t.Fatal("freshDeliveryCommitSplit returned empty C1")
	}

	msg := runGitOut(t, worktreeRoot, "log", "-1", "--format=%B", DeferredBranchName("wf/wt-test"))
	if !strings.Contains(msg, trailers) {
		t.Fatalf("deferred commit message = %q, must contain the body trailers (empty template fallback): %q", msg, trailers)
	}
	if !strings.Contains(msg, "deferred: 1 file(s) split from this chunk's delivery (automatic follow-up)") {
		t.Fatalf("deferred commit message = %q, must contain the deferred note", msg)
	}
}

func TestDeliverSplitsCommitWhenDeferredFilesPresent(t *testing.T) {
	ctx := context.Background()
	repoRoot, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "essential.txt", "essential change\n")
	writeWorktreeFile(t, worktreeRoot, "deferred.txt", "deferred change\n")

	deferredJSON, err := json.Marshal([]string{"deferred.txt"})
	if err != nil {
		t.Fatal(err)
	}
	pr := &fakePRClient{}
	inputs := map[string]string{"task": "split delivery", InputDeferredFiles: string(deferredJSON)}
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), inputs))
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("Result = %+v, want succeeded", res)
	}

	// The delivered commit (HEAD, and what got pushed) must carry ONLY
	// essential.txt - never deferred.txt.
	delivered := runGitOut(t, worktreeRoot, "diff", "--name-only", baseCommit, "HEAD")
	if delivered != "essential.txt" {
		t.Fatalf("delivered commit files = %q, want exactly essential.txt", delivered)
	}
	if refs := runGitOut(t, repoRoot, "ls-remote", originURL); !strings.Contains(refs, "refs/heads/wf/wt-test") {
		t.Fatalf("ls-remote origin lacks refs/heads/wf/wt-test:\n%s", refs)
	}

	// The deferred branch must exist locally, contain ONLY deferred.txt on
	// top of the delivered commit, and never have reached origin.
	deferredBranch := DeferredBranchName("wf/wt-test")
	deferredDiff := runGitOut(t, worktreeRoot, "diff", "--name-only", "HEAD", deferredBranch)
	if deferredDiff != "deferred.txt" {
		t.Fatalf("deferred branch diff vs HEAD = %q, want exactly deferred.txt", deferredDiff)
	}
	parent := runGitOut(t, worktreeRoot, "rev-parse", deferredBranch+"^")
	head := runGitOut(t, worktreeRoot, "rev-parse", "HEAD")
	if parent != head {
		t.Fatalf("deferred branch parent = %q, want the delivered HEAD %q", parent, head)
	}
	if refs := runGitOut(t, repoRoot, "ls-remote", originURL); strings.Contains(refs, deferredBranch) {
		t.Fatalf("ls-remote origin has the deferred branch, want it never pushed:\n%s", refs)
	}

	// The delivery record must flag a pending follow-up.
	rec := deliveryRecordByKey(t, repo, run)
	if rec.StackRemainingCommits != 1 {
		t.Fatalf("StackRemainingCommits = %d, want 1", rec.StackRemainingCommits)
	}
}

func TestDeliverNoSplitWhenDeferredFilesAbsent(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "a.txt", "change\n")

	pr := &fakePRClient{}
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"}))
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("Result = %+v, want succeeded", res)
	}
	rec := deliveryRecordByKey(t, repo, run)
	if rec.StackRemainingCommits != 0 {
		t.Fatalf("StackRemainingCommits = %d, want 0 (no deferred_files, no split)", rec.StackRemainingCommits)
	}
	if branchExists(ctx, RealGit{}, gc, DeferredBranchName("wf/wt-test")) {
		t.Fatal("deferred branch must not exist when nothing was deferred")
	}
}

func TestDeliverAutoSplitsOversizedDiffWhenEnabled(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	// essential.txt alone is small; the.big.txt alone pushes the total over a
	// tiny hard_lines. No InputDeferredFiles is set anywhere - the host must
	// measure both files itself and decide to defer the larger one.
	writeWorktreeFile(t, worktreeRoot, "essential.txt", "one line\n")
	writeWorktreeFile(t, worktreeRoot, "the.big.txt", strings.Repeat("line\n", 50))

	policy := defaultPolicy("draft")
	policy.StackingHardLines = 5
	policy.SplitDeferred = true

	pr := &fakePRClient{}
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, policy, map[string]string{"task": "gate"}))
	if err != nil {
		t.Fatalf("Deliver: %v (want the host's own split to make the gate pass)", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("Result = %+v, want succeeded", res)
	}
	delivered := runGitOut(t, worktreeRoot, "diff", "--name-only", baseCommit, "HEAD")
	if delivered != "essential.txt" {
		t.Fatalf("delivered commit files = %q, want exactly essential.txt (the host should have deferred the.big.txt)", delivered)
	}
	rec := deliveryRecordByKey(t, repo, run)
	if rec.StackRemainingCommits != 1 {
		t.Fatalf("StackRemainingCommits = %d, want 1", rec.StackRemainingCommits)
	}
}

// TestDeliverAutoSplitDefersNonASCIIFilename pins bug 2: git C-quotes
// numstat paths with non-ASCII characters (core.quotePath defaults true, so a
// file named cafe\303\251.txt appears as "caf\303\251.txt" with quotes and
// octal escapes). The split then fed those quoted strings into exclude
// pathspecs (':!<quoted>'), which git rejects with 'fatal: Unimplemented
// pathspec magic "”, and into git reset -- <quoted>, which silently no-ops.
// The gate must measure numstat with -c core.quotePath=false so the deferred
// path is literal: excludePathspecs, the reset, and the add all agree, the
// file is excluded from C1, and it lands on the deferred branch.
func TestDeliverAutoSplitDefersNonASCIIFilename(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "essential.txt", "one line\n")
	writeWorktreeFile(t, worktreeRoot, "caf\u00e9.txt", strings.Repeat("line\n", 50))

	policy := defaultPolicy("draft")
	policy.StackingHardLines = 5
	policy.SplitDeferred = true

	pr := &fakePRClient{}
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, policy, map[string]string{"task": "gate"}))
	if err != nil {
		t.Fatalf("Deliver with a non-ASCII filename = %v, want the host's split to measure and defer the literal path", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("Result = %+v, want succeeded", res)
	}
	delivered := runGitOut(t, worktreeRoot, "-c", "core.quotePath=false", "diff", "--name-only", baseCommit, "HEAD")
	if delivered != "essential.txt" {
		t.Fatalf("delivered commit files = %q, want exactly essential.txt (caf\u00e9.txt must be deferred, not pushed)", delivered)
	}
	deferredBranch := DeferredBranchName("wf/wt-test")
	deferredDiff := runGitOut(t, worktreeRoot, "-c", "core.quotePath=false", "diff", "--name-only", "HEAD", deferredBranch)
	if deferredDiff != "caf\u00e9.txt" {
		t.Fatalf("deferred branch diff vs HEAD = %q, want exactly caf\u00e9.txt", deferredDiff)
	}
	rec := deliveryRecordByKey(t, repo, run)
	if rec.StackRemainingCommits != 1 {
		t.Fatalf("StackRemainingCommits = %d, want 1", rec.StackRemainingCommits)
	}
}

// TestDeliverSizeGateSkipsSingleModeRun pins the integration-run finding: the
// per-chunk hard diff-size gate must not apply to a stack_mode=single run.
// The integration run re-implements the whole feature after every chunk
// merged, so its diff is the full-feature diff by construction - typically
// over hard_lines (the very reason decompose split the stack). Gating it
// either burned repair rounds on DiffSizeError or, with split_deferred on,
// opened an integration-deferred follow-up PR that the stack driver never
// drives - the drive reports the stack complete with an open, untracked PR.
// The gate is a per-chunk gate: only stack_mode absent (legacy direct runs)
// or chunk-mode deliveries are measured.
func TestDeliverSizeGateSkipsSingleModeRun(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "essential.txt", "one line\n")
	writeWorktreeFile(t, worktreeRoot, "the.big.txt", strings.Repeat("line\n", 50))

	policy := defaultPolicy("draft")
	policy.StackingHardLines = 5
	policy.SplitDeferred = true

	pr := &fakePRClient{}
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, policy, map[string]string{"task": "gate", "stack_mode": "single"}))
	if err != nil {
		t.Fatalf("Deliver: %v (a stack_mode=single run is not a chunk; the per-chunk gate must not measure it)", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("Result = %+v, want succeeded", res)
	}
	// The whole diff delivers - no split, no deferred branch, no follow-up.
	delivered := runGitOut(t, worktreeRoot, "diff", "--name-only", baseCommit, "HEAD")
	if delivered != "essential.txt\nthe.big.txt" {
		t.Fatalf("delivered commit files = %q, want both files (no split for a single-mode run)", delivered)
	}
	rec := deliveryRecordByKey(t, repo, run)
	if rec.StackRemainingCommits != 0 {
		t.Fatalf("StackRemainingCommits = %d, want 0 (no follow-up for a single-mode run)", rec.StackRemainingCommits)
	}
}

func TestDeliverSizeGateOffByDefaultDespiteSplittableDiff(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "essential.txt", "one line\n")
	writeWorktreeFile(t, worktreeRoot, "the.big.txt", strings.Repeat("line\n", 50))

	policy := defaultPolicy("draft")
	policy.StackingHardLines = 5
	// policy.SplitDeferred left false (the opt-in default): a diff that
	// COULD be split must still be rejected outright, proving the gate
	// never splits without the workflow explicitly enabling it.

	pr := &fakePRClient{}
	_, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, policy, map[string]string{"task": "gate"}))
	if !IsDiffSizeError(err) {
		t.Fatalf("Deliver error = %v, want a DiffSizeError (split_deferred is off)", err)
	}
}

func TestDeliverSizeGateStillRejectsWhenNothingSeparable(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	// A single file exceeds hard_lines all on its own: there is nothing else
	// to defer, so a split cannot help and the gate must still refuse.
	writeWorktreeFile(t, worktreeRoot, "essential.txt", strings.Repeat("line\n", 50))

	policy := defaultPolicy("draft")
	policy.StackingHardLines = 5
	policy.SplitDeferred = true

	pr := &fakePRClient{}
	_, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, policy, map[string]string{"task": "gate"}))
	if !IsDiffSizeError(err) {
		t.Fatalf("Deliver error = %v, want a DiffSizeError (a single oversized file has nothing separable to defer)", err)
	}
}

func TestDeliverSizeGateKeepsAtLeastOneFile(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	// Two files, BOTH individually oversized: deferring the larger alone
	// still leaves the kept file over hard_lines, so the gate must refuse
	// rather than defer everything down to an empty delivered commit.
	writeWorktreeFile(t, worktreeRoot, "big.txt", strings.Repeat("line\n", 50))
	writeWorktreeFile(t, worktreeRoot, "also-big.txt", strings.Repeat("line\n", 20))

	policy := defaultPolicy("draft")
	policy.StackingHardLines = 5
	policy.SplitDeferred = true

	pr := &fakePRClient{}
	_, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, policy, map[string]string{"task": "gate"}))
	if !IsDiffSizeError(err) {
		t.Fatalf("Deliver error = %v, want a DiffSizeError (the kept file alone still exceeds hard_lines)", err)
	}
}
