package delivery

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// seedCrashResumeTreeMismatch builds the exact state a crashed hook-mutated
// attempt leaves behind: the pending record holds the PRE-hook tree (the
// snapshot taken before the tree-mutating pre-commit hook ran), while HEAD
// carries the POST-hook tree committed by the mivia delivery identity. It
// returns the HEAD commit and the committed (post-hook) tree. diffRef seeds
// the record's DiffRef (empty or a stale pre-amend snapshot ref).
func seedCrashResumeTreeMismatch(t *testing.T, repo workflowledger.Repository, run workflowledger.RunSnapshot, worktreeRoot string, remoteID, url, diffRef string) (preHookTree, head, committedTree string) {
	t.Helper()
	ctx := context.Background()
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	runGit(t, worktreeRoot, "add", "-A")
	preHookTree = runGitOut(t, worktreeRoot, "write-tree") // snapshot BEFORE the hook
	// The tree-mutating pre-commit hook appends a line and stages it.
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\nhook-line\n")
	runGit(t, worktreeRoot, "add", "b.txt")
	runGit(t, worktreeRoot, "-c", "user.name=mivia", "-c", "user.email=mivia@localhost",
		"commit", "--allow-empty-message", "-m", "feat: resume")
	head = runGitOut(t, worktreeRoot, "rev-parse", "HEAD")
	committedTree = runGitOut(t, worktreeRoot, "rev-parse", "HEAD^{tree}")
	if committedTree == preHookTree {
		t.Fatalf("test setup: the hook mutation must change the committed tree")
	}
	key := DeliveryKey(run.RunID, run.WorkflowDigest)
	rec := workflowledger.DeliveryRecord{
		RunID: run.RunID, IdempotencyKey: key,
		Mode: "draft", BaseRef: "main", HeadRef: "wf/wt-test",
		Provider: "github", Status: "pending", TreeSHA: preHookTree,
		RemoteID: remoteID, URL: url, DiffRef: diffRef,
	}
	if err := repo.UpsertDelivery(ctx, rec); err != nil {
		t.Fatal(err)
	}
	return preHookTree, head, committedTree
}

// TestDeliverAdoptsHookMutatedTree pins bug 1: a pre-commit hook that
// legitimately mutates the staged tree (append + stage) makes the committed
// tree differ from the pre-hook tree snapshot. The commit SUCCEEDED, so the
// FIRST attempt must adopt the actual committed tree and deliver, instead of
// erroring "commit hooks changed the staged tree" (and refusing forever on
// the retry because HEAD^{tree} can never equal the pre-hook treeSHA).
func TestDeliverAdoptsHookMutatedTree(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	installTreeMutatingHook(t, worktreeRoot)

	pr := &fakePRClient{}
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "add delivery"}))
	if err != nil {
		t.Fatalf("Deliver must succeed on the FIRST attempt when the pre-commit hook mutates the staged tree: %v", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("Status = %q, want succeeded", res.Status)
	}
	head := runGitOut(t, worktreeRoot, "rev-parse", "HEAD")
	committedTree := runGitOut(t, worktreeRoot, "rev-parse", "HEAD^{tree}")
	if res.CommitSHA != head {
		t.Fatalf("CommitSHA = %s, want HEAD %s", res.CommitSHA, head)
	}
	if body := runGitOut(t, worktreeRoot, "show", "HEAD:b.txt"); !strings.Contains(body, "hook-line") {
		t.Fatalf("committed b.txt = %q, want the hook-appended line", body)
	}
	if n := pr.createdCount(); n != 1 {
		t.Fatalf("Create calls = %d, want 1", n)
	}
	rec := deliveryRecordByKey(t, repo, run)
	if rec.Status != "succeeded" || rec.CommitSHA != head {
		t.Fatalf("record = %+v, want succeeded with CommitSHA %s", rec, head)
	}
	// The recorded tree is the ADOPTED committed tree: a crash between the
	// commit and the pushed record can still resume by tree verification.
	if rec.TreeSHA != committedTree {
		t.Fatalf("record TreeSHA = %q, want adopted committed tree %q", rec.TreeSHA, committedTree)
	}
}

// TestDeliverHookMutationRetryWritesNoStageRecord pins the critical
// invariant for bug 1's adoption: after a hook-mutated commit was adopted
// (pending record re-upserted with the ACTUAL tree + CommitSHA), a retry
// after a transient failure must NOT mint another pending event — the retry
// path reuses the stage state byte-for-byte and only adds the push /
// PR-identity / succeeded records.
func TestDeliverHookMutationRetryWritesNoStageRecord(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo, store := newDeliveryFixtureStatusStore(t, workflowledger.RunStatusDeliveryPending, nil)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	installTreeMutatingHook(t, worktreeRoot)

	req := newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"})
	fake := &fakePRClient{}
	pr := &failOncePRClient{fake: fake}
	if _, err := Deliver(ctx, repo, RealGit{}, pr, req); err == nil {
		t.Fatal("first Deliver = nil error, want transient Create failure")
	}
	rec1 := deliveryRecordByKey(t, repo, run)
	if rec1.Status != "failed" || rec1.CommitSHA == "" {
		t.Fatalf("record after first attempt = %+v, want failed with the adopted CommitSHA", rec1)
	}
	if rec1.TreeSHA != runGitOut(t, worktreeRoot, "rev-parse", "HEAD^{tree}") {
		t.Fatalf("record TreeSHA = %q, want the adopted committed tree", rec1.TreeSHA)
	}
	res, err := Deliver(ctx, repo, RealGit{}, pr, req)
	if err != nil {
		t.Fatalf("second Deliver: %v", err)
	}
	if res.Status != "succeeded" || res.CommitSHA != rec1.CommitSHA {
		t.Fatalf("Result = %+v, want succeeded with the same adopted commit %s", res, rec1.CommitSHA)
	}
	rec2 := deliveryRecordByKey(t, repo, run)
	if rec2.TreeSHA != rec1.TreeSHA {
		t.Fatalf("TreeSHA changed across attempts: %s -> %s", rec1.TreeSHA, rec2.TreeSHA)
	}

	events, err := store.Events(ctx, run.RunID)
	if err != nil {
		t.Fatalf("store.Events: %v", err)
	}
	key := DeliveryKey(run.RunID, run.WorkflowDigest)
	var pending, pushed, succeeded, failed int
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
		switch payload.Delivery.Status {
		case "pending":
			pending++
		case "pushed":
			pushed++
		case "succeeded":
			succeeded++
		case "failed":
			failed++
		default:
			t.Fatalf("event %s carries unexpected status %q", ev.ID, payload.Delivery.Status)
		}
	}
	// Fresh attempt: TWO pending events (pre-hook tree snapshot + adopted
	// tree re-upsert with CommitSHA), one pushed, one failed. Retry: NO
	// pending event — only the push record, the PR-identity record (the
	// identity is learned only on the retry) and succeeded. Any additional
	// pending event on the retry would break the byte-identical stage-state
	// invariant.
	if pending != 2 || pushed != 3 || succeeded != 1 || failed != 1 {
		t.Fatalf("wf_delivery_upserted for key: pending=%d pushed=%d succeeded=%d failed=%d, want 2/3/1/1", pending, pushed, succeeded, failed)
	}
}

func TestDeliverPostCommitCrashResume(t *testing.T) {
	ctx := context.Background()

	// Crash between commit and the CommitSHA record: the record carries only
	// TreeSHA; Deliver must verify HEAD's tree and adopt the commit.
	t.Run("tree sha only", func(t *testing.T) {
		_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
		writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
		runGit(t, worktreeRoot, "add", "-A")
		runGit(t, worktreeRoot, "commit", "-m", "x")
		head := runGitOut(t, worktreeRoot, "rev-parse", "HEAD")
		tree := runGitOut(t, worktreeRoot, "rev-parse", "HEAD^{tree}")
		key := DeliveryKey(run.RunID, run.WorkflowDigest)
		if err := repo.UpsertDelivery(ctx, workflowledger.DeliveryRecord{
			RunID: run.RunID, IdempotencyKey: key,
			Mode: "draft", BaseRef: "main", HeadRef: "wf/wt-test",
			Provider: "github", Status: "pending", TreeSHA: tree,
		}); err != nil {
			t.Fatal(err)
		}
		pr := &fakePRClient{}
		res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "resume"}))
		if err != nil {
			t.Fatalf("Deliver: %v", err)
		}
		if res.Status != "succeeded" {
			t.Fatalf("Status = %q, want succeeded", res.Status)
		}
		if n := pr.createdCount(); n != 1 {
			t.Fatalf("Create calls = %d, want 1", n)
		}
		rec := deliveryRecordByKey(t, repo, run)
		if rec.Status != "succeeded" || rec.CommitSHA != head {
			t.Fatalf("record = %+v, want succeeded with CommitSHA %s", rec, head)
		}
	})

	// Crash after the CommitSHA record: resume by CommitSHA.
	t.Run("commit sha", func(t *testing.T) {
		_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
		writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
		runGit(t, worktreeRoot, "add", "-A")
		runGit(t, worktreeRoot, "commit", "-m", "x")
		head := runGitOut(t, worktreeRoot, "rev-parse", "HEAD")
		key := DeliveryKey(run.RunID, run.WorkflowDigest)
		if err := repo.UpsertDelivery(ctx, workflowledger.DeliveryRecord{
			RunID: run.RunID, IdempotencyKey: key,
			Mode: "draft", BaseRef: "main", HeadRef: "wf/wt-test",
			Provider: "github", Status: "pending", CommitSHA: head,
		}); err != nil {
			t.Fatal(err)
		}
		pr := &fakePRClient{}
		res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "resume"}))
		if err != nil {
			t.Fatalf("Deliver: %v", err)
		}
		if res.Status != "succeeded" {
			t.Fatalf("Status = %q, want succeeded", res.Status)
		}
		if n := pr.createdCount(); n != 1 {
			t.Fatalf("Create calls = %d, want 1", n)
		}
		rec := deliveryRecordByKey(t, repo, run)
		if rec.Status != "succeeded" || rec.CommitSHA != head {
			t.Fatalf("record = %+v, want succeeded with CommitSHA %s", rec, head)
		}
	})
}

// TestDeliverRetryPathWritesNoStageRecord seeds a post-commit-crash record
// (CommitSHA == head) and delivers. The retry path must reuse the existing
// events with no extra pending stage upsert (the PR-identity record is the only extra push).
func TestDeliverRetryPathWritesNoStageRecord(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo, store := newDeliveryFixtureStatusStore(t, workflowledger.RunStatusDeliveryPending, nil)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	runGit(t, worktreeRoot, "add", "-A")
	runGit(t, worktreeRoot, "commit", "-m", "x")
	head := runGitOut(t, worktreeRoot, "rev-parse", "HEAD")
	key := DeliveryKey(run.RunID, run.WorkflowDigest)
	if err := repo.UpsertDelivery(ctx, workflowledger.DeliveryRecord{
		RunID: run.RunID, IdempotencyKey: key,
		Mode: "draft", BaseRef: "main", HeadRef: "wf/wt-test",
		Provider: "github", Status: "pending", CommitSHA: head,
	}); err != nil {
		t.Fatal(err)
	}
	pr := &fakePRClient{}
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "resume"}))
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if res.Status != "succeeded" || res.CommitSHA != head {
		t.Fatalf("Result = %+v, want succeeded with CommitSHA %s", res, head)
	}
	if n := pr.createdCount(); n != 1 {
		t.Fatalf("Create calls = %d, want 1", n)
	}
	rec := deliveryRecordByKey(t, repo, run)
	if rec.Status != "succeeded" || rec.CommitSHA != head {
		t.Fatalf("record = %+v, want succeeded with CommitSHA %s", rec, head)
	}

	// Count the key's wf_delivery_upserted events by status: exactly the
	// seed's pending, the attempt's push record, the PR-identity record
	// (pushed again with RemoteID/URL, only when newly learned), and
	// succeeded. Any extra pending event would mean the retry rewrote the
	// stage record.
	events, err := store.Events(ctx, run.RunID)
	if err != nil {
		t.Fatalf("store.Events: %v", err)
	}
	var pending, pushed, succeeded int
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
		switch payload.Delivery.Status {
		case "pending":
			pending++
		case "pushed":
			pushed++
		case "succeeded":
			succeeded++
		default:
			t.Fatalf("event %s carries unexpected status %q", ev.ID, payload.Delivery.Status)
		}
	}
	if pending != 1 || pushed != 2 || succeeded != 1 {
		t.Fatalf("wf_delivery_upserted for key: pending=%d pushed=%d succeeded=%d, want 1/2/1 (seed + push record + PR-identity record + succeeded)", pending, pushed, succeeded)
	}
}

// TestDeliverRetryDetectsForeignCommit covers a crash-resume attempt whose
// worktree was edited after the crash: the recorded tree no longer matches
// HEAD, so the retry must refuse instead of adopting a foreign commit.
func TestDeliverRetryDetectsForeignCommit(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	// The delivery commit recorded by the crashed attempt (tree of b.txt).
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	runGit(t, worktreeRoot, "add", "-A")
	runGit(t, worktreeRoot, "commit", "-m", "x")
	tree := runGitOut(t, worktreeRoot, "rev-parse", "HEAD^{tree}")
	// A DIFFERENT change lands on top: the worktree is now foreign.
	writeWorktreeFile(t, worktreeRoot, "c.txt", "other\n")
	runGit(t, worktreeRoot, "add", "-A")
	runGit(t, worktreeRoot, "commit", "-m", "foreign")
	key := DeliveryKey(run.RunID, run.WorkflowDigest)
	if err := repo.UpsertDelivery(ctx, workflowledger.DeliveryRecord{
		RunID: run.RunID, IdempotencyKey: key,
		Mode: "draft", BaseRef: "main", HeadRef: "wf/wt-test",
		Provider: "github", Status: "pending", TreeSHA: tree,
	}); err != nil {
		t.Fatal(err)
	}
	// The seed as the crashed attempt left it: the refusal must leave it
	// byte-identical (Status still pending, TreeSHA intact).
	seed := deliveryRecordByKey(t, repo, run)
	pr := &fakePRClient{}
	_, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"}))
	if err == nil || !IsRefusal(err) {
		t.Fatalf("Deliver err = %v, want RefusalError for foreign commit", err)
	}
	assertZeroCreates(t, pr)
	// Refusal is pre-stage: the seeded record is untouched — not marked
	// failed, and the CommitSHA/TreeSHA resume data is intact.
	rec := deliveryRecordByKey(t, repo, run)
	if !reflect.DeepEqual(rec, seed) {
		t.Fatalf("record = %+v, want byte-identical seed %+v (refusal pre-stage)", rec, seed)
	}
}

// TestDeliverCrashResumeAdoptsHookMutatedTree pins THE regression: a
// tree-mutating pre-commit hook (gofmt -w + git add) legitimately changes the
// tree between the pending record's snapshot and the commit, and a crash
// between `git commit` and the adoption re-upsert (commitStagedTree's
// UpsertDelivery) leaves the durable record holding the PRE-hook tree. The
// retry must NOT refuse the run's OWN commit as foreign: when HEAD^{tree}
// differs from the recorded TreeSHA, the crash-resume path verifies HEAD is
// our delivery commit (count==1, clean worktree, parent==base, author mivia)
// and ADOPTS it, so delivery succeeds instead of terminating delivery_failed
// with no return edge.
func TestDeliverCrashResumeAdoptsHookMutatedTree(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	_, head, committedTree := seedCrashResumeTreeMismatch(t, repo, run, worktreeRoot, "", "", "")

	pr := &fakePRClient{}
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "resume"}))
	if err != nil {
		t.Fatalf("Deliver must ADOPT the run's own hook-mutated commit instead of refusing it as foreign: %v", err)
	}
	if res.Status != "succeeded" || res.CommitSHA != head {
		t.Fatalf("Result = %+v, want succeeded with CommitSHA %s", res, head)
	}
	if n := pr.createdCount(); n != 1 {
		t.Fatalf("Create calls = %d, want 1", n)
	}
	rec := deliveryRecordByKey(t, repo, run)
	if rec.Status != "succeeded" || rec.CommitSHA != head {
		t.Fatalf("record = %+v, want succeeded with CommitSHA %s", rec, head)
	}
	// The record's tree is the ADOPTED committed tree: the pre-hook mismatch
	// is healed, so a further retry can resume by tree verification.
	if rec.TreeSHA != committedTree {
		t.Fatalf("record TreeSHA = %q, want adopted committed tree %q", rec.TreeSHA, committedTree)
	}
}

// TestDeliverCrashResumeRefusesForeignMismatchedTree pins the refusal boundary
// of the adoption: when HEAD^{tree} differs from the recorded tree AND the
// HEAD commit is NOT provably our delivery commit (a different author, or a
// parent that is not the admitted base), the retry must refuse as foreign and
// leave the seeded record byte-identical (pre-stage, nothing written).
func TestDeliverCrashResumeRefusesForeignMismatchedTree(t *testing.T) {
	ctx := context.Background()

	t.Run("different author", func(t *testing.T) {
		_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
		writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
		runGit(t, worktreeRoot, "add", "-A")
		preHookTree := runGitOut(t, worktreeRoot, "write-tree")
		// Tree mutates, but the commit is authored by the TEST identity, not
		// the mivia delivery identity.
		writeWorktreeFile(t, worktreeRoot, "b.txt", "change\nhook-line\n")
		runGit(t, worktreeRoot, "add", "b.txt")
		runGit(t, worktreeRoot, "commit", "-m", "foreign") // user.name=Test
		key := DeliveryKey(run.RunID, run.WorkflowDigest)
		if err := repo.UpsertDelivery(ctx, workflowledger.DeliveryRecord{
			RunID: run.RunID, IdempotencyKey: key,
			Mode: "draft", BaseRef: "main", HeadRef: "wf/wt-test",
			Provider: "github", Status: "pending", TreeSHA: preHookTree,
		}); err != nil {
			t.Fatal(err)
		}
		seed := deliveryRecordByKey(t, repo, run)
		pr := &fakePRClient{}
		_, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"}))
		if err == nil || !IsRefusal(err) {
			t.Fatalf("Deliver err = %v, want RefusalError for a foreign-authored mismatched commit", err)
		}
		assertZeroCreates(t, pr)
		rec := deliveryRecordByKey(t, repo, run)
		if !reflect.DeepEqual(rec, seed) {
			t.Fatalf("record = %+v, want byte-identical seed %+v (refusal pre-stage)", rec, seed)
		}
	})

	t.Run("parent not base", func(t *testing.T) {
		_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
		writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
		runGit(t, worktreeRoot, "add", "-A")
		preHookTree := runGitOut(t, worktreeRoot, "write-tree")
		writeWorktreeFile(t, worktreeRoot, "b.txt", "change\nhook-line\n")
		runGit(t, worktreeRoot, "add", "b.txt")
		runGit(t, worktreeRoot, "-c", "user.name=mivia", "-c", "user.email=mivia@localhost",
			"commit", "--allow-empty-message", "-m", "first")
		// A SECOND mivia commit on top: HEAD's parent is not the admitted base.
		writeWorktreeFile(t, worktreeRoot, "c.txt", "extra\n")
		runGit(t, worktreeRoot, "add", "-A")
		runGit(t, worktreeRoot, "-c", "user.name=mivia", "-c", "user.email=mivia@localhost",
			"commit", "--allow-empty-message", "-m", "second")
		key := DeliveryKey(run.RunID, run.WorkflowDigest)
		if err := repo.UpsertDelivery(ctx, workflowledger.DeliveryRecord{
			RunID: run.RunID, IdempotencyKey: key,
			Mode: "draft", BaseRef: "main", HeadRef: "wf/wt-test",
			Provider: "github", Status: "pending", TreeSHA: preHookTree,
		}); err != nil {
			t.Fatal(err)
		}
		seed := deliveryRecordByKey(t, repo, run)
		pr := &fakePRClient{}
		_, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"}))
		if err == nil || !IsRefusal(err) {
			t.Fatalf("Deliver err = %v, want RefusalError when HEAD's parent is not the admitted base", err)
		}
		assertZeroCreates(t, pr)
		rec := deliveryRecordByKey(t, repo, run)
		if !reflect.DeepEqual(rec, seed) {
			t.Fatalf("record = %+v, want byte-identical seed %+v (refusal pre-stage)", rec, seed)
		}
	})
}

// TestDeliverCrashResumeAdoptionPreservesPRIdentity pins the adoption
// invariant: the re-upsert that heals the tree mismatch starts from the
// EXISTING record (mirroring commitStagedTree's carry-forward of `stage`,
// never a fresh deliveryRecord), so the run's known PR identity
// (RemoteID/URL) survives the adoption and a later retry can still prove
// ownership of its own PR instead of misjudging it as foreign.
func TestDeliverCrashResumeAdoptionPreservesPRIdentity(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo, store := newDeliveryFixtureStatusStore(t, workflowledger.RunStatusDeliveryPending, nil)
	_, head, _ := seedCrashResumeTreeMismatch(t, repo, run, worktreeRoot, "77", "https://example.com/pull/77", "")

	pr := &fakePRClient{found: &PRRef{RemoteID: "77", URL: "https://example.com/pull/77", Draft: true}}
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "resume"}))
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if res.Status != "succeeded" || res.RemoteID != "77" {
		t.Fatalf("Result = %+v, want succeeded reusing PR 77", res)
	}
	// The crash-resume path's adoption re-upsert is the ONLY pending event
	// that carries CommitSHA (the seeded crash-state record has none); it
	// must carry the preserved PR identity. A fresh deliveryRecord would
	// leave RemoteID/URL empty and break the next retry's ownership proof.
	events, err := store.Events(ctx, run.RunID)
	if err != nil {
		t.Fatalf("store.Events: %v", err)
	}
	key := DeliveryKey(run.RunID, run.WorkflowDigest)
	adoptionEvents := 0
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
		if payload.Delivery.IdempotencyKey != key || payload.Delivery.Status != "pending" || payload.Delivery.CommitSHA == "" {
			continue
		}
		adoptionEvents++
		if payload.Delivery.CommitSHA != head {
			t.Fatalf("pending adoption event CommitSHA = %q, want %q", payload.Delivery.CommitSHA, head)
		}
		if payload.Delivery.RemoteID != "77" || payload.Delivery.URL != "https://example.com/pull/77" {
			t.Fatalf("pending adoption event RemoteID/URL = %q/%q, want preserved 77/%q (a fresh deliveryRecord would erase PR identity)", payload.Delivery.RemoteID, payload.Delivery.URL, "https://example.com/pull/77")
		}
	}
	if adoptionEvents != 1 {
		t.Fatalf("adoption re-upsert events = %d, want exactly 1", adoptionEvents)
	}
	rec := deliveryRecordByKey(t, repo, run)
	if rec.Status != "succeeded" || rec.RemoteID != "77" || rec.URL != "https://example.com/pull/77" {
		t.Fatalf("record = %+v, want succeeded with PR identity 77 preserved end-to-end", rec)
	}
}

// TestDeliverCrashResumeAdoptsHookMutatedMessage pins that adoption is
// content/parent-based, NOT message-based: a commit-msg hook that appends a
// trailer legitimately changes the commit message, but the crash-resume path
// verifies count, cleanliness, parent and author — never the message text —
// so the run's own commit is still adopted.
func TestDeliverCrashResumeAdoptsHookMutatedMessage(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	// The commit-msg hook appends a trailer to the message.
	hooksDir := filepath.Join(t.TempDir(), "hooks")
	if err := os.Mkdir(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hook := "#!/bin/sh\necho 'Signed-off-by: hook <hook@example.com>' >> \"$1\"\n"
	if err := os.WriteFile(filepath.Join(hooksDir, "commit-msg"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, worktreeRoot, "config", "core.hooksPath", hooksDir)

	_, head, _ := seedCrashResumeTreeMismatch(t, repo, run, worktreeRoot, "", "", "")

	pr := &fakePRClient{}
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "resume"}))
	if err != nil {
		t.Fatalf("Deliver must adopt a message-mutated commit (author+parent checks, not message): %v", err)
	}
	if res.Status != "succeeded" || res.CommitSHA != head {
		t.Fatalf("Result = %+v, want succeeded with CommitSHA %s", res, head)
	}
	if msg := runGitOut(t, worktreeRoot, "log", "-1", "--format=%B"); !strings.Contains(msg, "Signed-off-by: hook") {
		t.Fatalf("commit message = %q, want the hook-appended trailer present", msg)
	}
	if n := pr.createdCount(); n != 1 {
		t.Fatalf("Create calls = %d, want 1", n)
	}
}

// TestDeliverCrashResumeRefusesForeignAmendedFileSet pins the Step-5 finding:
// the adoption branch's four checks (count==1, clean worktree, parent==base,
// author mivia) all pass for a `git commit --amend` with DIFFERENT content,
// because amend preserves the original author, parent and commit count. The
// retry must REFUSE such a genuinely foreign commit: the FILE SET of the
// amended HEAD against base (b.txt + c.txt) differs from the recorded
// tree's file set (b.txt), so the amended content is not what the delivery
// produced. The refusal must leave the seeded record byte-identical.
func TestDeliverCrashResumeRefusesForeignAmendedFileSet(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	// Seed the crash-resume mismatch state: the pending record holds the
	// PRE-hook tree (only b.txt changed), HEAD carries the committed
	// post-hook tree (same file set).
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	runGit(t, worktreeRoot, "add", "-A")
	preHookTree := runGitOut(t, worktreeRoot, "write-tree")
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\nhook-line\n")
	runGit(t, worktreeRoot, "add", "b.txt")
	runGit(t, worktreeRoot, "-c", "user.name=mivia", "-c", "user.email=mivia@localhost",
		"commit", "--allow-empty-message", "-m", "feat: resume")
	// A genuinely foreign amend that ADDS a new file: same author, same
	// parent, same count — only the file set changes.
	writeWorktreeFile(t, worktreeRoot, "c.txt", "foreign\n")
	runGit(t, worktreeRoot, "add", "c.txt")
	runGit(t, worktreeRoot, "-c", "user.name=mivia", "-c", "user.email=mivia@localhost",
		"commit", "--amend", "--no-edit")
	if got := runGitOut(t, worktreeRoot, "rev-parse", "HEAD~1"); got != baseCommit {
		t.Fatalf("test setup: amended HEAD parent = %s, want base %s", got, baseCommit)
	}
	if got := runGitOut(t, worktreeRoot, "log", "-1", "--format=%an <%ae>"); got != "mivia <mivia@localhost>" {
		t.Fatalf("test setup: amended author = %q, want the mivia delivery identity", got)
	}
	if got := runGitOut(t, worktreeRoot, "rev-list", "--count", baseCommit+"..HEAD"); got != "1" {
		t.Fatalf("test setup: rev-list count = %q, want 1", got)
	}
	if got := runGitOut(t, worktreeRoot, "diff", "--name-only", baseCommit+"..HEAD"); got == runGitOut(t, worktreeRoot, "diff", "--name-only", baseCommit+".."+preHookTree) {
		t.Fatal("test setup: the amended file set must differ from the recorded tree's file set")
	}
	key := DeliveryKey(run.RunID, run.WorkflowDigest)
	if err := repo.UpsertDelivery(ctx, workflowledger.DeliveryRecord{
		RunID: run.RunID, IdempotencyKey: key,
		Mode: "draft", BaseRef: "main", HeadRef: "wf/wt-test",
		Provider: "github", Status: "pending", TreeSHA: preHookTree,
	}); err != nil {
		t.Fatal(err)
	}
	seed := deliveryRecordByKey(t, repo, run)
	pr := &fakePRClient{}
	_, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"}))
	if err == nil || !IsRefusal(err) {
		t.Fatalf("Deliver err = %v, want RefusalError: an amended HEAD whose file set differs from the recorded tree is a foreign commit (amend preserves author/parent/count)", err)
	}
	assertZeroCreates(t, pr)
	rec := deliveryRecordByKey(t, repo, run)
	if !reflect.DeepEqual(rec, seed) {
		t.Fatalf("record = %+v, want byte-identical seed %+v (refusal pre-stage)", rec, seed)
	}
}

// TestDeliverCrashResumeAdoptsHookMutatedTreeFreshDiffRef pins the honest
// record (DC-9): when the tree-mismatch adoption heals a hook-mutated commit
// (same file set, gofmt-style content change), the ADOPTED record must carry
// the RETRY's freshly recomputed diff ref — what is actually at HEAD — not
// the stale ref preserved from the seeded record. Before the fix the adoption
// re-upsert started from `existing` and kept existing.DiffRef, so the durable
// record described a diff the pushed content no longer matched.
func TestDeliverCrashResumeAdoptsHookMutatedTreeFreshDiffRef(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo, store := newDeliveryFixtureStatusStore(t, workflowledger.RunStatusDeliveryPending, nil)
	staleDiffRef := "sha256:" + strings.Repeat("a", 64) // the seeded record's pre-amend snapshot ref
	_, head, _ := seedCrashResumeTreeMismatch(t, repo, run, worktreeRoot, "", "", staleDiffRef)

	pr := &fakePRClient{}
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "resume"}))
	if err != nil {
		t.Fatalf("Deliver must ADOPT a hook-mutated commit with the same file set: %v", err)
	}
	if res.Status != "succeeded" || res.CommitSHA != head {
		t.Fatalf("Result = %+v, want succeeded with CommitSHA %s", res, head)
	}
	// The retry's freshly computed diff ref: verifyEligibility snapshots
	// `git diff --stat base..HEAD` plus the porcelain output under the same
	// helpers, so recompute it byte-for-byte with the same runner.
	statOut, err := (RealGit{}).Run(ctx, gc, "diff", "--stat", baseCommit+"..HEAD")
	if err != nil {
		t.Fatal(err)
	}
	porcelainOut, err := (RealGit{}).Run(ctx, gc, "-c", "core.fsmonitor=false", "status", "--porcelain")
	if err != nil {
		t.Fatal(err)
	}
	wantDiffRef := "sha256:" + workflowledger.DigestHex([]byte(boundText(statOut+"\n"+porcelainOut, maxDiffBytes, "diff truncated at 64 KiB")))
	if wantDiffRef == "" || wantDiffRef == staleDiffRef {
		t.Fatalf("test setup: fresh diff ref %q must differ from the stale seeded ref %q", wantDiffRef, staleDiffRef)
	}

	// The ADOPTION re-upsert (the only pending event carrying CommitSHA) must
	// carry the FRESH diff ref, not the stale seeded one.
	events, err := store.Events(ctx, run.RunID)
	if err != nil {
		t.Fatalf("store.Events: %v", err)
	}
	key := DeliveryKey(run.RunID, run.WorkflowDigest)
	adoptionEvents := 0
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
		if payload.Delivery.IdempotencyKey != key || payload.Delivery.Status != "pending" || payload.Delivery.CommitSHA == "" {
			continue
		}
		adoptionEvents++
		if payload.Delivery.DiffRef != wantDiffRef {
			t.Fatalf("pending adoption event DiffRef = %q, want the retry's fresh diff ref %q (the record must describe what is actually published, not the stale %q)", payload.Delivery.DiffRef, wantDiffRef, staleDiffRef)
		}
	}
	if adoptionEvents != 1 {
		t.Fatalf("adoption re-upsert events = %d, want exactly 1", adoptionEvents)
	}
	rec := deliveryRecordByKey(t, repo, run)
	if rec.DiffRef != wantDiffRef {
		t.Fatalf("final record DiffRef = %q, want the retry's fresh diff ref %q", rec.DiffRef, wantDiffRef)
	}
}
