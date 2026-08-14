package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// seedCrashResumeTreeMismatch builds the state a crashed hook-mutated attempt
// leaves behind: the pending record holds the PRE-hook tree snapshot while
// HEAD carries the POST-hook tree committed by the mivia delivery identity.
// It returns the HEAD commit and the committed (post-hook) tree. diffRef seeds
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
	runGit(t, worktreeRoot, "-c", "user.name=mivia-agent[bot]", "-c", "user.email=4525471+mivia-agent[bot]@users.noreply.github.com",
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

// deliveryUpsertedRecords returns every wf_delivery_upserted payload recorded
// for the run's delivery key, in event order. Unknown statuses fail the test,
// mirroring the strict counting the stage-state invariants rely on.
func deliveryUpsertedRecords(t *testing.T, store storage.Store, run workflowledger.RunSnapshot) []workflowledger.DeliveryRecord {
	t.Helper()
	events, err := store.Events(context.Background(), run.RunID)
	if err != nil {
		t.Fatalf("store.Events: %v", err)
	}
	key := DeliveryKey(run.RunID, run.WorkflowDigest)
	var recs []workflowledger.DeliveryRecord
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
		case "pending", "pushed", "succeeded", "failed":
			recs = append(recs, payload.Delivery)
		default:
			t.Fatalf("event %s carries unexpected status %q", ev.ID, payload.Delivery.Status)
		}
	}
	return recs
}

// recomputeFreshDiffRef re-derives the diff ref verifyEligibility computes
// for HEAD (`git diff --stat base..HEAD` + porcelain, bounded the same way),
// so tests can assert the adopted record carries what is actually at HEAD.
func recomputeFreshDiffRef(t *testing.T, ctx context.Context, gc GitContext, baseCommit string) string {
	t.Helper()
	statOut, err := (RealGit{}).Run(ctx, gc, "diff", "--stat", baseCommit+"..HEAD")
	if err != nil {
		t.Fatal(err)
	}
	porcelainOut, err := (RealGit{}).Run(ctx, gc, "-c", "core.fsmonitor=false", "status", "--porcelain")
	if err != nil {
		t.Fatal(err)
	}
	return "sha256:" + workflowledger.DigestHex([]byte(boundText(statOut+"\n"+porcelainOut, maxDiffBytes, "diff truncated at 64 KiB")))
}

// assertRefusalPreStage runs Deliver and asserts a RefusalError, zero PR
// creates, and a byte-identical seeded record: a refusal is pre-stage, so
// nothing is written - not even a failed record.
func assertRefusalPreStage(t *testing.T, repo workflowledger.Repository, run workflowledger.RunSnapshot, req Request) {
	t.Helper()
	seed := deliveryRecordByKey(t, repo, run)
	pr := &fakePRClient{}
	_, err := Deliver(context.Background(), repo, RealGit{}, pr, req)
	if err == nil || !IsRefusal(err) {
		t.Fatalf("Deliver err = %v, want RefusalError (%s)", err, t.Name())
	}
	assertZeroCreates(t, pr)
	rec := deliveryRecordByKey(t, repo, run)
	if !reflect.DeepEqual(rec, seed) {
		t.Fatalf("record = %+v, want byte-identical seed %+v (refusal pre-stage)", rec, seed)
	}
}

// TestDeliverAdoptsHookMutatedTree pins bug 1: a pre-commit hook that mutates
// the staged tree makes the committed tree differ from the pre-hook snapshot,
// so the FIRST attempt must adopt the actual committed tree and deliver,
// instead of refusing forever on the retry (HEAD^{tree} can never equal the
// pre-hook treeSHA).
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
	// The recorded tree is the ADOPTED committed tree, so a crash between the
	// commit and the pushed record can still resume by tree verification.
	if rec.TreeSHA != committedTree {
		t.Fatalf("record TreeSHA = %q, want adopted committed tree %q", rec.TreeSHA, committedTree)
	}
}

// TestDeliverHookMutationRetryWritesNoStageRecord pins the critical invariant
// for bug 1's adoption: after a hook-mutated commit was adopted (pending
// record re-upserted with the ACTUAL tree + CommitSHA), a retry after a
// transient failure must NOT mint another pending event - the retry path
// reuses the stage state byte-for-byte and only adds the push / PR-identity /
// succeeded records.
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

	// Fresh: TWO pending events (pre-hook tree snapshot + adopted tree
	// re-upsert with CommitSHA), one pushed, one failed. Retry: NO pending
	// event - any additional one would break the byte-identical stage-state
	// invariant.
	counts := map[string]int{}
	for _, d := range deliveryUpsertedRecords(t, store, run) {
		counts[d.Status]++
	}
	if counts["pending"] != 2 || counts["pushed"] != 3 || counts["succeeded"] != 1 || counts["failed"] != 1 {
		t.Fatalf("wf_delivery_upserted for key: pending=%d pushed=%d succeeded=%d failed=%d, want 2/3/1/1", counts["pending"], counts["pushed"], counts["succeeded"], counts["failed"])
	}
}

// TestDeliverPostCommitCrashResume covers a crash between the commit and the
// durable record: the record carries only TreeSHA (resume by tree) or already
// carries CommitSHA (resume by commit).
func TestDeliverPostCommitCrashResume(t *testing.T) {
	ctx := context.Background()

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
// (CommitSHA == head) and delivers: the retry must reuse the existing events
// with no extra pending stage upsert (the PR-identity record is the only extra push).
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

	// Exactly the seed's pending, the attempt's push record, the PR-identity
	// record (pushed again with RemoteID/URL, only when newly learned), and
	// succeeded - any extra pending would mean the retry rewrote the stage
	// record.
	counts := map[string]int{}
	for _, d := range deliveryUpsertedRecords(t, store, run) {
		counts[d.Status]++
	}
	if counts["pending"] != 1 || counts["pushed"] != 2 || counts["succeeded"] != 1 {
		t.Fatalf("wf_delivery_upserted for key: pending=%d pushed=%d succeeded=%d, want 1/2/1", counts["pending"], counts["pushed"], counts["succeeded"])
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
	assertRefusalPreStage(t, repo, run, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"}))
}

// TestDeliverCrashResumeAdoptsHookMutatedTree pins THE regression: a
// tree-mutating pre-commit hook (gofmt -w + git add) legitimately changes the
// tree between the pending record's snapshot and the commit, and a crash
// before the adoption re-upsert leaves the record holding the PRE-hook tree.
// The retry must NOT refuse the run's OWN commit as foreign: it verifies HEAD
// is our delivery commit (count==1, clean worktree, parent==base, author
// mivia) and ADOPTS it.
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
// of the adoption: when HEAD^{tree} differs from the recorded tree AND HEAD is
// NOT provably our delivery commit (a different author, or a parent that is
// not the admitted base), the retry must refuse as foreign and leave the seed
// byte-identical (pre-stage, nothing written).
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
		assertRefusalPreStage(t, repo, run, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"}))
	})

	t.Run("parent not base", func(t *testing.T) {
		_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
		writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
		runGit(t, worktreeRoot, "add", "-A")
		preHookTree := runGitOut(t, worktreeRoot, "write-tree")
		writeWorktreeFile(t, worktreeRoot, "b.txt", "change\nhook-line\n")
		runGit(t, worktreeRoot, "add", "b.txt")
		runGit(t, worktreeRoot, "-c", "user.name=mivia-agent[bot]", "-c", "user.email=4525471+mivia-agent[bot]@users.noreply.github.com",
			"commit", "--allow-empty-message", "-m", "first")
		// A SECOND mivia commit on top: HEAD's parent is not the admitted base.
		writeWorktreeFile(t, worktreeRoot, "c.txt", "extra\n")
		runGit(t, worktreeRoot, "add", "-A")
		runGit(t, worktreeRoot, "-c", "user.name=mivia-agent[bot]", "-c", "user.email=4525471+mivia-agent[bot]@users.noreply.github.com",
			"commit", "--allow-empty-message", "-m", "second")
		key := DeliveryKey(run.RunID, run.WorkflowDigest)
		if err := repo.UpsertDelivery(ctx, workflowledger.DeliveryRecord{
			RunID: run.RunID, IdempotencyKey: key,
			Mode: "draft", BaseRef: "main", HeadRef: "wf/wt-test",
			Provider: "github", Status: "pending", TreeSHA: preHookTree,
		}); err != nil {
			t.Fatal(err)
		}
		assertRefusalPreStage(t, repo, run, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"}))
	})
}

// TestDeliverCrashResumeAdoptionPreservesPRIdentity pins the adoption
// invariant: the re-upsert that heals the tree mismatch starts from the
// EXISTING record (mirroring commitStagedTree's carry-forward of `stage`,
// never a fresh deliveryRecord), so the run's known PR identity
// (RemoteID/URL) survives and a later retry can still prove ownership of its
// own PR instead of misjudging it as foreign.
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
	// The adoption re-upsert is the ONLY pending event carrying CommitSHA (the
	// seeded crash-state record has none); it must carry the preserved PR
	// identity - a fresh deliveryRecord would leave RemoteID/URL empty and
	// break the next retry's ownership proof.
	adoptionEvents := 0
	for _, d := range deliveryUpsertedRecords(t, store, run) {
		if d.Status != "pending" || d.CommitSHA == "" {
			continue
		}
		adoptionEvents++
		if d.CommitSHA != head {
			t.Fatalf("pending adoption event CommitSHA = %q, want %q", d.CommitSHA, head)
		}
		if d.RemoteID != "77" || d.URL != "https://example.com/pull/77" {
			t.Fatalf("pending adoption event RemoteID/URL = %q/%q, want preserved 77/%q (a fresh deliveryRecord would erase PR identity)", d.RemoteID, d.URL, "https://example.com/pull/77")
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
// verifies count, cleanliness, parent and author - never the message text.
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
// the adoption checks (count==1, clean worktree, parent==base, author mivia)
// all pass for a `git commit --amend` with DIFFERENT content, because amend
// preserves author, parent and count. The retry must REFUSE such a genuinely
// foreign commit: the FILE SET of the amended HEAD against base differs from
// the recorded tree's file set, so the amended content is not what the
// delivery produced. The refusal leaves the seed byte-identical.
func TestDeliverCrashResumeRefusesForeignAmendedFileSet(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	// Seed the crash-resume mismatch state: the pending record holds the
	// PRE-hook tree (only b.txt changed), HEAD carries the committed post-hook
	// tree (same file set).
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	runGit(t, worktreeRoot, "add", "-A")
	preHookTree := runGitOut(t, worktreeRoot, "write-tree")
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\nhook-line\n")
	runGit(t, worktreeRoot, "add", "b.txt")
	runGit(t, worktreeRoot, "-c", "user.name=mivia-agent[bot]", "-c", "user.email=4525471+mivia-agent[bot]@users.noreply.github.com",
		"commit", "--allow-empty-message", "-m", "feat: resume")
	// A genuinely foreign amend that ADDS a new file: same author, same
	// parent, same count - only the file set changes.
	writeWorktreeFile(t, worktreeRoot, "c.txt", "foreign\n")
	runGit(t, worktreeRoot, "add", "c.txt")
	runGit(t, worktreeRoot, "-c", "user.name=mivia-agent[bot]", "-c", "user.email=4525471+mivia-agent[bot]@users.noreply.github.com",
		"commit", "--amend", "--no-edit")
	if got := runGitOut(t, worktreeRoot, "rev-parse", "HEAD~1"); got != baseCommit {
		t.Fatalf("test setup: amended HEAD parent = %s, want base %s", got, baseCommit)
	}
	if got := runGitOut(t, worktreeRoot, "log", "-1", "--format=%an <%ae>"); got != "mivia-agent[bot] <4525471+mivia-agent[bot]@users.noreply.github.com>" {
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
	assertRefusalPreStage(t, repo, run, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"}))
}

// TestDeliverCrashResumeAdoptsHookMutatedTreeFreshDiffRef pins the honest
// record (DC-9): when the tree-mismatch adoption heals a hook-mutated commit
// (same file set, gofmt-style content change), the ADOPTED record must carry
// the RETRY's freshly recomputed diff ref - what is actually at HEAD - not
// the stale ref preserved from the seed.
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
	wantDiffRef := recomputeFreshDiffRef(t, ctx, gc, baseCommit)
	if wantDiffRef == "" || wantDiffRef == staleDiffRef {
		t.Fatalf("test setup: fresh diff ref %q must differ from the stale seeded ref %q", wantDiffRef, staleDiffRef)
	}

	// The ADOPTION re-upsert (the only pending event carrying CommitSHA) must
	// carry the FRESH diff ref, not the stale seeded one.
	adoptionEvents := 0
	for _, d := range deliveryUpsertedRecords(t, store, run) {
		if d.Status != "pending" || d.CommitSHA == "" {
			continue
		}
		adoptionEvents++
		if d.DiffRef != wantDiffRef {
			t.Fatalf("pending adoption event DiffRef = %q, want the retry's fresh diff ref %q (not the stale %q)", d.DiffRef, wantDiffRef, staleDiffRef)
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

// seedRecordedDeliveryCommit builds the rejected-attempt shape: a mivia
// delivery commit C1 recorded as failed with CommitSHA/TreeSHA and a known PR
// identity (RemoteID 77). It returns C1 and its tree.
func seedRecordedDeliveryCommit(t *testing.T, repo workflowledger.Repository, run workflowledger.RunSnapshot, worktreeRoot string) (head, tree string) {
	t.Helper()
	ctx := context.Background()
	writeWorktreeFile(t, worktreeRoot, "b.txt", "implemented\n")
	runGit(t, worktreeRoot, "add", "-A")
	runGit(t, worktreeRoot, "-c", "user.name=mivia-agent[bot]", "-c", "user.email=4525471+mivia-agent[bot]@users.noreply.github.com",
		"commit", "--allow-empty-message", "-m", "feat: task\n\nBody.")
	head = runGitOut(t, worktreeRoot, "rev-parse", "HEAD")
	tree = runGitOut(t, worktreeRoot, "rev-parse", "HEAD^{tree}")
	key := DeliveryKey(run.RunID, run.WorkflowDigest)
	if err := repo.UpsertDelivery(ctx, workflowledger.DeliveryRecord{
		RunID: run.RunID, IdempotencyKey: key, Status: "failed",
		Mode: "draft", BaseRef: "main", HeadRef: "wf/wt-test", Provider: "github",
		CommitSHA: head, TreeSHA: tree,
		RemoteID: "77", URL: "https://example.com/pull/77",
	}); err != nil {
		t.Fatal(err)
	}
	return head, tree
}

// TestDeliverRetryAdoptsUnrecordedFollowUpCommit pins bug
// delivery-followup-upsert-failure-strands-run: commitWorktreeFollowUp
// commits the follow-up delivery commit (HEAD advances to C2 on top of the
// recorded C1) and THEN re-upserts the record; when that UpsertDelivery fails
// transiently, the record still holds C1 while HEAD is C2, and the next retry
// previously fell into the default RefusalError that stranded the run at
// delivery_failed with no return edge. The retry must verify HEAD is the run's
// OWN unrecorded follow-up commit (clean worktree, count==1, parent ==
// recorded CommitSHA, author mivia) and ADOPT it. This test fails before the
// fix at attempt 2 (RefusalError) and passes after.
func TestDeliverRetryAdoptsUnrecordedFollowUpCommit(t *testing.T) {
	ctx := context.Background()
	repoRoot, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	// Seed the rejected-attempt shape: a mivia delivery commit C1 recorded as
	// failed with a known PR identity, plus an uncommitted repair edit.
	firstHead, _ := seedRecordedDeliveryCommit(t, repo, run, worktreeRoot)
	writeWorktreeFile(t, worktreeRoot, "c.txt", "repair\n")

	// Attempt 1: only the follow-up ADOPTION re-upsert fails (after
	// commitWorktreeFollowUp commits C2: Status "failed" preserved from the
	// seed, CommitSHA == C2 != firstHead), stranding the record at C1 while
	// HEAD advances to C2 - the exact state the finding describes.
	failing := &failUpsertRepo{
		Repository: repo,
		failErr:    errors.New("store: adoption upsert failed"),
		failWhen: func(d workflowledger.DeliveryRecord) bool {
			return d.Status == "failed" && d.CommitSHA != firstHead
		},
	}
	pr := &fakePRClient{found: &PRRef{RemoteID: "77", URL: "https://example.com/pull/77", Draft: true}}
	if _, err := Deliver(ctx, failing, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"})); err == nil || IsRefusal(err) {
		t.Fatalf("attempt 1 err = %v, want a transient (non-refusal) adoption upsert failure", err)
	} else if !strings.Contains(err.Error(), "store: adoption upsert failed") {
		t.Fatalf("attempt 1 err = %v, want the injected adoption-upsert failure", err)
	}
	rec1 := deliveryRecordByKey(t, repo, run)
	if rec1.CommitSHA != firstHead {
		t.Fatalf("record after attempt 1 CommitSHA = %s, want the recorded commit %s (the failed adoption must not rewrite it)", rec1.CommitSHA, firstHead)
	}
	if headAfterAttempt1 := runGitOut(t, worktreeRoot, "rev-parse", "HEAD"); headAfterAttempt1 == firstHead {
		t.Fatal("HEAD unchanged; commitWorktreeFollowUp did not commit the repair edit")
	}
	assertZeroCreates(t, pr)

	// Attempt 2: adopt the run's OWN unrecorded follow-up commit (HEAD == C2,
	// recorded CommitSHA still C1) instead of refusing it as foreign.
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"}))
	if err != nil {
		t.Fatalf("attempt 2 Deliver = %v, want the run's own unrecorded follow-up commit adopted", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("Result = %+v, want succeeded", res)
	}
	head := runGitOut(t, worktreeRoot, "rev-parse", "HEAD")
	if res.CommitSHA != head || res.CommitSHA == firstHead {
		t.Fatalf("Result.CommitSHA = %s, want the follow-up HEAD %s (a new commit, not the recorded C1)", res.CommitSHA, head)
	}
	if res.RemoteID != "77" || res.URL != "https://example.com/pull/77" {
		t.Fatalf("Result.RemoteID/URL = %q/%q, want the preserved PR identity 77", res.RemoteID, res.URL)
	}
	if n := pr.createdCount(); n != 0 {
		t.Fatalf("Create calls = %d, want 0 (PR 77 must be reused, not created)", n)
	}
	rec := deliveryRecordByKey(t, repo, run)
	if rec.Status != "succeeded" || rec.CommitSHA != head {
		t.Fatalf("record = %+v, want succeeded with CommitSHA %s", rec, head)
	}
	if rec.TreeSHA != runGitOut(t, worktreeRoot, "rev-parse", "HEAD^{tree}") {
		t.Fatalf("record TreeSHA = %q, want the adopted HEAD tree %q", rec.TreeSHA, runGitOut(t, worktreeRoot, "rev-parse", "HEAD^{tree}"))
	}
	if rec.RemoteID != "77" || rec.URL != "https://example.com/pull/77" {
		t.Fatalf("record = %+v, want the PR identity 77 preserved through the adoption", rec)
	}
	if wantDiffRef := recomputeFreshDiffRef(t, ctx, gc, baseCommit); rec.DiffRef != wantDiffRef {
		t.Fatalf("record DiffRef = %q, want the retry's fresh diff ref %q", rec.DiffRef, wantDiffRef)
	}
	// The branch reached origin with the adopted follow-up commit (the
	// attempt-1 failure happened before any push).
	if refs := runGitOut(t, repoRoot, "ls-remote", originURL); !strings.Contains(refs, "refs/heads/wf/wt-test") {
		t.Fatalf("origin has no wf/wt-test branch after the adopted retry push:\n%s", refs)
	}
}

// TestDeliverRetryRefusesForeignCommitAboveRecordedCommit pins the refusal
// boundary of the follow-up adoption branch: the retry ADOPTS an unrecorded
// follow-up commit only when HEAD is provably the run's own (clean worktree,
// count==1, parent == recorded CommitSHA, author mivia). Every other shape
// over the same seeded record must refuse as foreign, leave the seed
// byte-identical (pre-stage, nothing written), and create zero PRs - the
// widening must not weaken the foreign-worktree guard. These subcases pass
// before and after the fix.
func TestDeliverRetryRefusesForeignCommitAboveRecordedCommit(t *testing.T) {
	t.Run("foreign author on top of the recorded commit", func(t *testing.T) {
		_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
		firstHead, _ := seedRecordedDeliveryCommit(t, repo, run, worktreeRoot)
		// One commit by the TEST identity sits directly on the recorded
		// commit: count==1 and parent==C1 pass, the author check refuses.
		writeWorktreeFile(t, worktreeRoot, "c.txt", "foreign\n")
		runGit(t, worktreeRoot, "add", "-A")
		runGit(t, worktreeRoot, "commit", "-m", "foreign") // user.name=Test
		if parent := runGitOut(t, worktreeRoot, "rev-parse", "HEAD~1"); parent != firstHead {
			t.Fatalf("test setup: foreign HEAD parent = %s, want the recorded commit %s", parent, firstHead)
		}
		assertRefusalPreStage(t, repo, run, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"}))
	})

	t.Run("second mivia commit makes count two", func(t *testing.T) {
		_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
		firstHead, _ := seedRecordedDeliveryCommit(t, repo, run, worktreeRoot)
		// TWO more mivia commits on top of the recorded commit: count != 1.
		writeWorktreeFile(t, worktreeRoot, "c.txt", "one\n")
		runGit(t, worktreeRoot, "add", "-A")
		runGit(t, worktreeRoot, "-c", "user.name=mivia-agent[bot]", "-c", "user.email=4525471+mivia-agent[bot]@users.noreply.github.com",
			"commit", "--allow-empty-message", "-m", "one")
		writeWorktreeFile(t, worktreeRoot, "d.txt", "two\n")
		runGit(t, worktreeRoot, "add", "-A")
		runGit(t, worktreeRoot, "-c", "user.name=mivia-agent[bot]", "-c", "user.email=4525471+mivia-agent[bot]@users.noreply.github.com",
			"commit", "--allow-empty-message", "-m", "two")
		if got := runGitOut(t, worktreeRoot, "rev-list", "--count", firstHead+"..HEAD"); got != "2" {
			t.Fatalf("test setup: rev-list count = %q, want 2", got)
		}
		assertRefusalPreStage(t, repo, run, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"}))
	})

	t.Run("dirty worktree above the stranded follow-up commit", func(t *testing.T) {
		_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
		firstHead, _ := seedRecordedDeliveryCommit(t, repo, run, worktreeRoot)
		// A mivia follow-up commit sits on the recorded commit (the stranded
		// shape from the primary test's attempt 1), and a NEW uncommitted edit
		// makes the worktree dirty: HEAD != recorded CommitSHA AND
		// porcelainEmpty == false, so the clean-worktree gate must refuse.
		writeWorktreeFile(t, worktreeRoot, "c.txt", "followup\n")
		runGit(t, worktreeRoot, "add", "-A")
		runGit(t, worktreeRoot, "-c", "user.name=mivia-agent[bot]", "-c", "user.email=4525471+mivia-agent[bot]@users.noreply.github.com",
			"commit", "--allow-empty-message", "-m", "followup")
		if parent := runGitOut(t, worktreeRoot, "rev-parse", "HEAD~1"); parent != firstHead {
			t.Fatalf("test setup: follow-up HEAD parent = %s, want the recorded commit %s", parent, firstHead)
		}
		writeWorktreeFile(t, worktreeRoot, "d.txt", "uncommitted\n")
		if got := runGitOut(t, worktreeRoot, "-c", "core.fsmonitor=false", "status", "--porcelain"); got == "" {
			t.Fatal("test setup: worktree must be dirty")
		}
		assertRefusalPreStage(t, repo, run, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"}))
	})
}
