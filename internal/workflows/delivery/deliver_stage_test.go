package delivery

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// newDeliveryFixtureStatusStoreOrigin is the store-aware fixture with an
// explicit origin base commit recorded at admission (OriginBaseCommit), for
// the remote-base verification tests. An empty originBase leaves the field
// unset, matching the pre-OriginBaseCommit fixtures.
func newDeliveryFixtureStatusStoreOrigin(t *testing.T, status workflowledger.RunStatus, store storage.Store, originBase string) (repoRoot, worktreeRoot string, gc GitContext, baseCommit, originURL string, run workflowledger.RunSnapshot, ledgerRepo workflowledger.Repository, st storage.Store) {
	t.Helper()
	repoRoot = t.TempDir()
	runGit(t, repoRoot, "init", "-b", "main")
	gitConfig(t, repoRoot)
	writeWorktreeFile(t, repoRoot, "a.txt", "base\n")
	runGit(t, repoRoot, "add", "a.txt")
	runGit(t, repoRoot, "commit", "-m", "base")
	baseCommit = runGitOut(t, repoRoot, "rev-parse", "HEAD")

	originDir := filepath.Join(t.TempDir(), "origin.git")
	runGit(t, filepath.Dir(originDir), "init", "--bare", filepath.Base(originDir))
	runGit(t, repoRoot, "remote", "add", "origin", originDir)
	runGit(t, repoRoot, "push", "-u", "origin", "main")
	originURL = originDir

	worktreeRoot = filepath.Join(t.TempDir(), "wt")
	runGit(t, repoRoot, "worktree", "add", "-b", "wf/wt-test", worktreeRoot, baseCommit)
	gitConfig(t, worktreeRoot)
	gc = GitContext{Dir: worktreeRoot, GitDir: filepath.Join(repoRoot, ".git", "worktrees", filepath.Base(worktreeRoot))}

	if store == nil {
		store = storage.NewMemory()
	}
	ledgerRepo = workflowledger.NewStorageRepository(store)
	run = createRunWithStatus(t, ledgerRepo, workflowledger.RunSnapshot{
		RunID:            "wfr-test",
		WorkflowName:     "test-wf",
		WorkflowDigest:   "digest",
		ActiveStepID:     "success",
		OriginBaseCommit: originBase,
		RemoteURL:        originURL,
	}, status)
	return repoRoot, worktreeRoot, gc, baseCommit, originURL, run, ledgerRepo, store
}

// installTreeMutatingHook installs a pre-commit hook (via core.hooksPath)
// that appends a line to b.txt and stages it: a legitimate staged-tree
// mutation like the repo's gofmt -w + git add hook.
func installTreeMutatingHook(t *testing.T, worktreeRoot string) {
	t.Helper()
	hooksDir := filepath.Join(t.TempDir(), "hooks")
	if err := os.Mkdir(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hook := "#!/bin/sh\necho 'hook-line' >> b.txt\ngit add b.txt\n"
	if err := os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, worktreeRoot, "config", "core.hooksPath", hooksDir)
}

// TestDeliverCommitSHAUnconditionalWhenTreeUnchanged pins the bug where
// commitStagedTree fails to persist CommitSHA when the pre-commit hook does
// NOT mutate the staged tree (the common case). The conditional upsert at
// deliver_stage.go line 124 gates the CommitSHA write behind
// adoptedTree != treeSHA, so when the tree is unchanged CommitSHA stays empty
// in the pending record. This test asserts that after a fresh commit WITHOUT
// hook mutation, (a) the result carries the correct CommitSHA, (b) the
// persisted record has CommitSHA set to HEAD, and (c) exactly 2 pending
// wf_delivery_upserted events exist for the key: the pre-commit snapshot (with
// CommitSHA empty) plus the unconditional post-commit upsert (with CommitSHA
// equal to HEAD). The two upserts differ in CommitSHA, so the idempotency
// guard at storage_deliveries.go does NOT absorb the second.
func TestDeliverCommitSHAUnconditionalWhenTreeUnchanged(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo, store := newDeliveryFixtureStatusStore(t, workflowledger.RunStatusDeliveryPending, nil)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")

	pr := &fakePRClient{}
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "add delivery"}))
	if err != nil {
		t.Fatalf("Deliver must succeed on fresh commit without hook mutation: %v", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("Status = %q, want succeeded", res.Status)
	}
	head := runGitOut(t, worktreeRoot, "rev-parse", "HEAD")
	if res.CommitSHA != head {
		t.Fatalf("Result.CommitSHA = %q, want HEAD %q", res.CommitSHA, head)
	}
	rec := deliveryRecordByKey(t, repo, run)
	if rec.CommitSHA == "" {
		t.Fatalf("persisted record CommitSHA is empty; commitStagedTree did not write CommitSHA when the tree was unchanged (the common case)")
	}
	if rec.CommitSHA != head {
		t.Fatalf("persisted record CommitSHA = %q, want HEAD %q", rec.CommitSHA, head)
	}

	// Exactly 2 pending events: pre-commit snapshot (CommitSHA empty) + post-commit upsert (CommitSHA == HEAD).
	key := DeliveryKey(run.RunID, run.WorkflowDigest)
	events, err := store.Events(ctx, run.RunID)
	if err != nil {
		t.Fatalf("store.Events: %v", err)
	}
	var pending int
	for _, ev := range events {
		if ev.Kind != "wf_delivery_upserted" {
			continue
		}
		var payload struct {
			Delivery workflowledger.DeliveryRecord `json:"delivery"`
		}
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			t.Fatalf("unmarshal %s payload: %v", ev.ID, err)
		}
		if payload.Delivery.IdempotencyKey != key {
			continue
		}
		if payload.Delivery.Status == "pending" {
			pending++
		}
	}
	if pending != 2 {
		t.Fatalf("pending wf_delivery_upserted for key = %d, want 2 (pre-commit snapshot + post-commit upsert with CommitSHA)", pending)
	}
}

// TestDeliverCommitSHAPresentInPendingEvents inspects the raw store events to
// assert that at least one pending wf_delivery_upserted event for the key
// carries CommitSHA equal to HEAD after a fresh commit without hook mutation.
// Before the fix this fails because no pending event carries CommitSHA when
// the tree is unchanged; after the fix the unconditional post-commit upsert
// writes CommitSHA.
func TestDeliverCommitSHAPresentInPendingEvents(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo, store := newDeliveryFixtureStatusStore(t, workflowledger.RunStatusDeliveryPending, nil)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")

	pr := &fakePRClient{}
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "add delivery"}))
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("Status = %q, want succeeded", res.Status)
	}
	head := runGitOut(t, worktreeRoot, "rev-parse", "HEAD")

	key := DeliveryKey(run.RunID, run.WorkflowDigest)
	events, err := store.Events(ctx, run.RunID)
	if err != nil {
		t.Fatalf("store.Events: %v", err)
	}
	var foundCommitSHA bool
	for _, ev := range events {
		if ev.Kind != "wf_delivery_upserted" {
			continue
		}
		var payload struct {
			Delivery workflowledger.DeliveryRecord `json:"delivery"`
		}
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			t.Fatalf("unmarshal %s payload: %v", ev.ID, err)
		}
		if payload.Delivery.IdempotencyKey != key || payload.Delivery.Status != "pending" {
			continue
		}
		if payload.Delivery.CommitSHA == head {
			foundCommitSHA = true
		}
	}
	if !foundCommitSHA {
		t.Fatal("no pending event carries CommitSHA == HEAD; commitStagedTree did not unconditionally write CommitSHA post-commit")
	}
}

// TestDeliverNoDiffReVerifyPublishesNewWork pins bug 2: a no_diff verdict is
// point-in-time. A no_diff record seeded by a crashed attempt must NOT be
// replayed at step 3; the current worktree is re-verified, and new work that
// appeared after the no_diff upsert is published instead of settling the run
// succeeded with zero PRs.
func TestDeliverNoDiffReVerifyPublishesNewWork(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	key := DeliveryKey(run.RunID, run.WorkflowDigest)
	if err := repo.UpsertDelivery(ctx, workflowledger.DeliveryRecord{
		RunID: run.RunID, IdempotencyKey: key,
		Mode: "draft", BaseRef: "main", HeadRef: "wf/wt-test",
		Provider: "github", Status: "no_diff",
	}); err != nil {
		t.Fatal(err)
	}
	// New work appeared after the no_diff verdict.
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	pr := &fakePRClient{}
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"}))
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("Status = %q, want succeeded (new work must be published, not settled as no_diff)", res.Status)
	}
	if n := pr.createdCount(); n != 1 {
		t.Fatalf("Create calls = %d, want 1", n)
	}
	rec := deliveryRecordByKey(t, repo, run)
	if rec.Status != "succeeded" || rec.CommitSHA == "" {
		t.Fatalf("record = %+v, want succeeded with a commit", rec)
	}
}

// TestDeliverNoDiffReVerifyStillNoDiffMintsNoEvent pins bug 2's idempotent
// tail: re-verifying a still-empty worktree keeps the no_diff outcome and the
// byte-identical no_diff re-write is absorbed by the storage layer (no second
// no_diff event).
func TestDeliverNoDiffReVerifyStillNoDiffMintsNoEvent(t *testing.T) {
	ctx := context.Background()
	_, _, gc, baseCommit, originURL, run, repo, store := newDeliveryFixtureStatusStore(t, workflowledger.RunStatusDeliveryPending, nil)
	key := DeliveryKey(run.RunID, run.WorkflowDigest)
	if err := repo.UpsertDelivery(ctx, workflowledger.DeliveryRecord{
		RunID: run.RunID, IdempotencyKey: key,
		Mode: "draft", BaseRef: "main", HeadRef: "wf/wt-test",
		Provider: "github", Status: "no_diff",
	}); err != nil {
		t.Fatal(err)
	}
	pr := &fakePRClient{}
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"}))
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if res.Status != "no_diff" {
		t.Fatalf("Status = %q, want no_diff", res.Status)
	}
	assertZeroCreates(t, pr)
	events, err := store.Events(ctx, run.RunID)
	if err != nil {
		t.Fatalf("store.Events: %v", err)
	}
	var noDiffEvents int
	for _, ev := range events {
		if ev.Kind != "wf_delivery_upserted" {
			continue
		}
		var payload struct {
			Delivery workflowledger.DeliveryRecord `json:"delivery"`
		}
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			t.Fatalf("unmarshal %s payload: %v", ev.ID, err)
		}
		if payload.Delivery.IdempotencyKey != key {
			continue
		}
		if payload.Delivery.Status == "no_diff" {
			noDiffEvents++
		}
	}
	if noDiffEvents != 1 {
		t.Fatalf("no_diff events = %d, want 1 (re-verify re-write must be absorbed as idempotent)", noDiffEvents)
	}
}

// TestDeliverRemoteBaseAdvancedAccepted pins the recovery semantics: a REMOTE
// base that advanced forward after admission is a normal condition - delivery
// proceeds and creates the PR against the current base. The local
// refs/heads/<base> ref is deliberately reset to the admitted commit to prove
// the local ref is no longer consulted (linked-worktree shared refs).
func TestDeliverRemoteBaseAdvancedAccepted(t *testing.T) {
	ctx := context.Background()
	repoRoot, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	// Advance the remote base: commit locally, push, then restore the
	// local ref so refs/heads/main still equals BaseCommit while
	// refs/remotes/origin/main points at the new commit.
	writeWorktreeFile(t, repoRoot, "adv.txt", "remote advanced\n")
	runGit(t, repoRoot, "add", "adv.txt")
	runGit(t, repoRoot, "commit", "-m", "advance remote main")
	runGit(t, repoRoot, "push", "origin", "main")
	runGit(t, repoRoot, "reset", "--hard", baseCommit)
	if got := runGitOut(t, repoRoot, "rev-parse", "refs/heads/main"); got != baseCommit {
		t.Fatalf("local refs/heads/main = %s, want admitted base %s", got, baseCommit)
	}
	if got := runGitOut(t, repoRoot, "rev-parse", "refs/remotes/origin/main"); got == baseCommit {
		t.Fatalf("test setup: refs/remotes/origin/main still equals the admitted base")
	}

	pr := &fakePRClient{}
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"}))
	if err != nil {
		t.Fatalf("Deliver with an advanced remote base = %v, want success", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("Result = %+v, want succeeded", res)
	}
	if n := pr.createdCount(); n != 1 {
		t.Fatalf("Create calls = %d, want 1", n)
	}
}

// TestDeliverRemoteBaseAbsentRefFetched pins the fetch-first eligibility: a
// missing remote-tracking ref is healed by the unconditional pinned fetch, so
// delivery proceeds instead of erroring or refusing.
func TestDeliverRemoteBaseAbsentRefFetched(t *testing.T) {
	ctx := context.Background()
	repoRoot, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	runGit(t, repoRoot, "update-ref", "-d", "refs/remotes/origin/main")
	pr := &fakePRClient{}
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"}))
	if err != nil {
		t.Fatalf("Deliver with an absent remote-tracking ref = %v, want the fetch to heal it", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("Result = %+v, want succeeded", res)
	}
}

// TestDeliverRemoteBaseDeletedRefused pins F-1: a remote whose base branch no
// longer exists can never satisfy ancestry, so delivery refuses permanently
// (a deleted base is not a transient condition).
func TestDeliverRemoteBaseDeletedRefused(t *testing.T) {
	ctx := context.Background()
	repoRoot, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	runGit(t, repoRoot, "push", "origin", "--delete", "main")
	pr := &fakePRClient{}
	_, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"}))
	if err == nil || !IsRefusal(err) {
		t.Fatalf("Deliver err = %v, want RefusalError for a deleted remote base branch", err)
	}
	assertNoRecord(t, repo, run)
	assertZeroCreates(t, pr)
}

// TestDeliverRemoteBaseComparedAgainstAdmittedOriginBase pins bug 3's
// admission pin: the remote base is verified against the origin base commit
// recorded at ADMISSION (OriginBaseCommit), not the local BaseCommit. A PR is
// created against the remote base, and the two may legitimately differ when
// the local checkout was not synced at admission: a remote ref still at the
// admitted origin base must pass even though it differs from the local base.
func TestDeliverRemoteBaseComparedAgainstAdmittedOriginBase(t *testing.T) {
	ctx := context.Background()

	repoRoot := t.TempDir()
	runGit(t, repoRoot, "init", "-b", "main")
	gitConfig(t, repoRoot)
	writeWorktreeFile(t, repoRoot, "a.txt", "base\n")
	runGit(t, repoRoot, "add", "a.txt")
	runGit(t, repoRoot, "commit", "-m", "base")
	baseCommit := runGitOut(t, repoRoot, "rev-parse", "HEAD")

	originDir := filepath.Join(t.TempDir(), "origin.git")
	runGit(t, filepath.Dir(originDir), "init", "--bare", filepath.Base(originDir))
	runGit(t, repoRoot, "remote", "add", "origin", originDir)
	runGit(t, repoRoot, "push", "-u", "origin", "main")

	// Advance origin/main to X — the origin base the admission recorded —
	// then restore the local base ref to the admitted local commit.
	writeWorktreeFile(t, repoRoot, "adv.txt", "origin advanced\n")
	runGit(t, repoRoot, "add", "adv.txt")
	runGit(t, repoRoot, "commit", "-m", "advance origin")
	originBase := runGitOut(t, repoRoot, "rev-parse", "HEAD")
	runGit(t, repoRoot, "push", "origin", "main")
	runGit(t, repoRoot, "reset", "--hard", baseCommit)
	if got := runGitOut(t, repoRoot, "rev-parse", "refs/heads/main"); got != baseCommit {
		t.Fatalf("local refs/heads/main = %s, want admitted base %s", got, baseCommit)
	}
	if got := runGitOut(t, repoRoot, "rev-parse", "refs/remotes/origin/main"); got != originBase {
		t.Fatalf("refs/remotes/origin/main = %s, want origin base %s", got, originBase)
	}

	worktreeRoot := filepath.Join(t.TempDir(), "wt")
	runGit(t, repoRoot, "worktree", "add", "-b", "wf/wt-test", worktreeRoot, baseCommit)
	gitConfig(t, worktreeRoot)
	gc := GitContext{Dir: worktreeRoot, GitDir: filepath.Join(repoRoot, ".git", "worktrees", filepath.Base(worktreeRoot))}

	store := storage.NewMemory()
	repo := workflowledger.NewStorageRepository(store)
	run := createRunWithStatus(t, repo, workflowledger.RunSnapshot{
		RunID: "wfr-test", WorkflowName: "test-wf", WorkflowDigest: "digest",
		ActiveStepID: "success", RemoteURL: originDir,
		OriginBaseCommit: originBase,
	}, workflowledger.RunStatusDeliveryPending)

	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	pr := &fakePRClient{}
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originDir, defaultPolicy("draft"), map[string]string{"task": "x"}))
	if err != nil {
		t.Fatalf("Deliver must verify the remote base against the ADMITTED origin base (not the local BaseCommit): %v", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("Status = %q, want succeeded", res.Status)
	}
	if n := pr.createdCount(); n != 1 {
		t.Fatalf("Create calls = %d, want 1", n)
	}
}

// TestDeliverForwardAdvancedLocalBaseAccepted pins the linked-worktree fix: a
// commit that advances the LOCAL base branch (the main repo's shared
// refs/heads/main, which legitimately moves while a run is in flight) must NOT
// refuse delivery. The remote base is unchanged, so ancestry passes and the
// PR is created against the current base.
func TestDeliverForwardAdvancedLocalBaseAccepted(t *testing.T) {
	ctx := context.Background()
	repoRoot, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	// Advance the local base branch only; origin still points at baseCommit.
	writeWorktreeFile(t, repoRoot, "a2.txt", "x\n")
	runGit(t, repoRoot, "add", "a2.txt")
	runGit(t, repoRoot, "commit", "-m", "advance main")
	pr := &fakePRClient{}
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"}))
	if err != nil {
		t.Fatalf("Deliver with an advanced local base = %v, want success (linked-worktree shared refs must not refuse)", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("Result = %+v, want succeeded", res)
	}
	if n := pr.createdCount(); n != 1 {
		t.Fatalf("Create calls = %d, want 1", n)
	}
}

// TestDeliverRemoteBaseForwardAdvanced pins the recovery behavior for the
// original defect: when the REMOTE base advances forward (a normal condition,
// Dependabot/Renovate-style), delivery proceeds and creates the PR against the
// current base instead of refusing.
func TestDeliverRemoteBaseForwardAdvanced(t *testing.T) {
	ctx := context.Background()
	repoRoot, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	// Advance both local and remote base: commit on main and push to origin.
	writeWorktreeFile(t, repoRoot, "a2.txt", "x\n")
	runGit(t, repoRoot, "add", "a2.txt")
	runGit(t, repoRoot, "commit", "-m", "advance base")
	runGit(t, repoRoot, "push", "origin", "main")
	pr := &fakePRClient{}
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"}))
	if err != nil {
		t.Fatalf("Deliver with a forward-advanced remote base = %v, want success", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("Result = %+v, want succeeded", res)
	}
	if n := pr.createdCount(); n != 1 {
		t.Fatalf("Create calls = %d, want 1", n)
	}
}

// TestDeliverRemoteBaseRewrittenRefused pins the permanent-refusal boundary: a
// base REWRITE that drops the admitted commit from origin history must refuse
// (producing a PR would be garbage).
func TestDeliverRemoteBaseRewrittenRefused(t *testing.T) {
	ctx := context.Background()
	repoRoot, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	rewriteRemoteBase(t, repoRoot, originURL)
	pr := &fakePRClient{}
	_, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"}))
	if err == nil || !IsRefusal(err) {
		t.Fatalf("Deliver err = %v, want RefusalError for a rewritten remote base", err)
	}
	assertNoRecord(t, repo, run)
	assertZeroCreates(t, pr)
}

// rewriteRemoteBase force-replaces the origin base branch with an orphan
// commit that has no parent, so the admitted base commit is no longer part of
// the remote history.
func rewriteRemoteBase(t *testing.T, repoRoot, originURL string) {
	t.Helper()
	runGit(t, repoRoot, "checkout", "--orphan", "orphan-main")
	runGit(t, repoRoot, "commit", "--allow-empty", "-m", "rewritten base")
	runGit(t, repoRoot, "push", "--force", "origin", "orphan-main:refs/heads/main")
	runGit(t, repoRoot, "checkout", "main")
}

// TestDeliverDeliveryFailedReentry pins re-eligibility: a run settled
// delivery_failed is promoted back to delivery_pending by the single enforcing
// CAS inside Deliver and then delivers normally.
func TestDeliverDeliveryFailedReentry(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	cur, err := repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, run.RunID, cur.Version, workflowledger.RunStatusDeliveryFailed, nil); err != nil {
		t.Fatalf("CAS to delivery_failed: %v", err)
	}
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	pr := &fakePRClient{}
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"}))
	if err != nil {
		t.Fatalf("Deliver on a delivery_failed run = %v, want re-eligibility success", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("Result = %+v, want succeeded", res)
	}
	if n := pr.createdCount(); n != 1 {
		t.Fatalf("Create calls = %d, want 1", n)
	}
	stored, err := repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("run status = %q, want delivery_pending (re-opened; caller CASes to succeeded)", stored.Status)
	}
}

// TestDeliverPostCreateBaseCheckAR7 pins the TOCTOU close: when the PR's
// actual base (baseRefOid) no longer contains the admitted base commit, the
// attempt must refuse instead of settling succeeded, even though the PR was
// already created. Each subtest uses its own fixture (a succeeded record on
// the shared key would replay, not re-run, the check).
func TestDeliverPostCreateBaseCheckAR7(t *testing.T) {
	ctx := context.Background()

	t.Run("base contains the admitted commit - succeeds", func(t *testing.T) {
		_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
		writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
		pr := &fakePRClient{baseRefOID: baseCommit}
		res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"}))
		if err != nil {
			t.Fatalf("Deliver = %v, want success when the PR base contains the admitted commit", err)
		}
		if res.Status != "succeeded" {
			t.Fatalf("Result = %+v, want succeeded", res)
		}
	})

	t.Run("rewritten PR base - refused", func(t *testing.T) {
		_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
		writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
		orphan := orphanCommit(t, worktreeRoot, "wf/wt-test")
		pr := &fakePRClient{baseRefOID: orphan}
		_, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"}))
		if err == nil || !IsRefusal(err) {
			t.Fatalf("Deliver err = %v, want RefusalError when the PR base was rewritten", err)
		}
		rec := deliveryRecordByKey(t, repo, run)
		if rec.Status != "failed" {
			t.Fatalf("record status = %q, want failed", rec.Status)
		}
	})
}

// orphanCommit creates a parentless commit in the given repo and returns its
// SHA. branch is the branch to return to afterwards (the worktree's own).
func orphanCommit(t *testing.T, dir, branch string) string {
	t.Helper()
	orphan := "orphan-tmp"
	runGit(t, dir, "checkout", "--orphan", orphan)
	runGit(t, dir, "commit", "--allow-empty", "-m", "orphan")
	sha := runGitOut(t, dir, "rev-parse", "HEAD")
	runGit(t, dir, "checkout", branch)
	runGit(t, dir, "branch", "-D", orphan)
	return sha
}
