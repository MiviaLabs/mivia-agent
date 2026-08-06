package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// runGitOut runs git and returns trimmed combined output.
func runGitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// gitConfig sets the test identity in a repo.
func gitConfig(t *testing.T, dir string) {
	t.Helper()
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
}

// createRunWithStatus admits a run (CreateRun only admits pending) and CASes
// it along the pending->running->delivery_pending chain to the requested status.
func createRunWithStatus(t *testing.T, repo workflowledger.Repository, snap workflowledger.RunSnapshot, status workflowledger.RunStatus) workflowledger.RunSnapshot {
	t.Helper()
	ctx := context.Background()
	snap.Status = workflowledger.RunStatusPending
	if err := repo.CreateRun(ctx, snap, []byte("snapshot")); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	cur, err := repo.GetRun(ctx, snap.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	for _, next := range []workflowledger.RunStatus{workflowledger.RunStatusRunning, workflowledger.RunStatusDeliveryPending} {
		if cur.Status == status {
			return cur
		}
		if err := repo.CompareAndSetRunStatus(ctx, snap.RunID, cur.Version, next, nil); err != nil {
			t.Fatalf("CAS to %s: %v", next, err)
		}
		cur, err = repo.GetRun(ctx, snap.RunID)
		if err != nil {
			t.Fatalf("GetRun: %v", err)
		}
	}
	return cur
}

// newDeliveryFixtureStatus builds the real-git fixture: a main repo with a
// base commit and bare origin, a worktree on branch wf/wt-test, and a ledger
// with run wfr-test in the given status.
func newDeliveryFixtureStatus(t *testing.T, status workflowledger.RunStatus) (repoRoot, worktreeRoot string, gc GitContext, baseCommit, originURL string, run workflowledger.RunSnapshot, ledgerRepo workflowledger.Repository) {
	t.Helper()
	repoRoot, worktreeRoot, gc, baseCommit, originURL, run, ledgerRepo, _ = newDeliveryFixtureStatusStore(t, status, nil)
	return repoRoot, worktreeRoot, gc, baseCommit, originURL, run, ledgerRepo
}

// newDeliveryFixtureStatusStore is the store-aware core of the delivery
// fixture: when store is nil a fresh in-memory store is created; otherwise
// the repository is built over the given store so a test can inspect the raw
// wf_delivery_upserted event log (see TestDeliverRetryPathWritesNoStageRecord).
func newDeliveryFixtureStatusStore(t *testing.T, status workflowledger.RunStatus, store storage.Store) (repoRoot, worktreeRoot string, gc GitContext, baseCommit, originURL string, run workflowledger.RunSnapshot, ledgerRepo workflowledger.Repository, st storage.Store) {
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
		RunID:          "wfr-test",
		WorkflowName:   "test-wf",
		WorkflowDigest: "digest",
		ActiveStepID:   "success",
		RemoteURL:      originURL,
	}, status)
	return repoRoot, worktreeRoot, gc, baseCommit, originURL, run, ledgerRepo, store
}

func newDeliveryFixture(t *testing.T) (string, string, GitContext, string, string, workflowledger.RunSnapshot, workflowledger.Repository) {
	t.Helper()
	return newDeliveryFixtureStatus(t, workflowledger.RunStatusDeliveryPending)
}

func defaultPolicy(mode string) Policy {
	return Policy{
		Kind:                  "pull_request",
		Mode:                  mode,
		Provider:              "github",
		Base:                  "main",
		TitleTemplate:         "feat: {{ inputs.task }}",
		CommitMessageTemplate: "feat: {{ inputs.task }}\n\nBody.",
	}
}

func newRequest(run workflowledger.RunSnapshot, gc GitContext, baseCommit, originURL string, policy Policy, inputs map[string]string) Request {
	return Request{
		RunID:          run.RunID,
		WorkflowDigest: run.WorkflowDigest,
		Policy:         policy,
		Inputs:         inputs,
		BaseCommit:     baseCommit,
		Branch:         "wf/wt-test",
		GitCtx:         gc,
		OriginURL:      originURL,
	}
}

// fakePRClient records PR boundary calls; Create always succeeds.
type fakePRClient struct {
	mu      sync.Mutex
	found   *PRRef
	repos   []string
	created []PRInput
}

func (f *fakePRClient) FindByHead(context.Context, string, string) (*PRRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.found, nil
}

func (f *fakePRClient) Create(_ context.Context, repo string, in PRInput) (PRRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.repos = append(f.repos, repo)
	f.created = append(f.created, in)
	return PRRef{RemoteID: strconv.Itoa(len(f.created)), URL: "https://example.com/pull/" + strconv.Itoa(len(f.created))}, nil
}

func (f *fakePRClient) createdCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.created)
}

func deliveryRecordByKey(t *testing.T, repo workflowledger.Repository, run workflowledger.RunSnapshot) workflowledger.DeliveryRecord {
	t.Helper()
	rec, err := repo.GetDeliveryByIdempotencyKey(context.Background(), DeliveryKey(run.RunID, run.WorkflowDigest))
	if err != nil {
		t.Fatalf("GetDeliveryByIdempotencyKey: %v", err)
	}
	return rec
}

func wantBody(run workflowledger.RunSnapshot) string {
	return "Automated workflow delivery from Mivia.\n\nRun: " + run.RunID + "\nWorkflow digest: " + run.WorkflowDigest
}

func TestDeliverDraftHappyPath(t *testing.T) {
	ctx := context.Background()
	repoRoot, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	pr := &fakePRClient{}
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "add delivery"}))
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if res.Status != "succeeded" || res.Mode != "draft" || res.BaseRef != "main" || res.HeadRef != "wf/wt-test" || res.Provider != "github" {
		t.Fatalf("Result = %+v, want succeeded/draft/main/wf/wt-test/github", res)
	}
	if res.CommitSHA == "" || res.RemoteID != "1" || res.URL == "" || res.DiffRef == "" {
		t.Fatalf("Result = %+v, want CommitSHA/RemoteID/URL/DiffRef populated", res)
	}
	if n := pr.createdCount(); n != 1 {
		t.Fatalf("Create calls = %d, want 1", n)
	}
	c := pr.created[0]
	if !c.Draft || c.Base != "main" || c.Head != "wf/wt-test" || c.Title != "feat: add delivery" || c.Body != wantBody(run) {
		t.Fatalf("Create = %+v, want Draft=true Base=main Head=wf/wt-test Title=%q Body=%q", c, "feat: add delivery", wantBody(run))
	}
	if pr.repos[0] != strings.TrimSuffix(originURL, ".git") {
		t.Fatalf("Create repo = %q, want normalized origin %q", pr.repos[0], strings.TrimSuffix(originURL, ".git"))
	}
	rec := deliveryRecordByKey(t, repo, run)
	if rec.Status != "succeeded" || rec.CommitSHA != res.CommitSHA || rec.URL != res.URL {
		t.Fatalf("record = %+v, want succeeded with CommitSHA %s and URL %s", rec, res.CommitSHA, res.URL)
	}
	if rec.TreeSHA == "" || rec.TreeSHA != runGitOut(t, worktreeRoot, "rev-parse", "HEAD^{tree}") {
		t.Fatalf("record TreeSHA = %q, want the committed tree %q", rec.TreeSHA, runGitOut(t, worktreeRoot, "rev-parse", "HEAD^{tree}"))
	}
	if msg := runGitOut(t, worktreeRoot, "log", "-1", "--format=%s"); msg != "feat: add delivery" {
		t.Fatalf("commit subject = %q, want %q", msg, "feat: add delivery")
	}
	if refs := runGitOut(t, repoRoot, "ls-remote", originURL); !strings.Contains(refs, "refs/heads/wf/wt-test") {
		t.Fatalf("ls-remote origin lacks refs/heads/wf/wt-test:\n%s", refs)
	}
	if stored, err := repo.GetRun(ctx, run.RunID); err != nil {
		t.Fatal(err)
	} else if stored.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("run status = %q, want delivery_pending (caller CASes to succeeded)", stored.Status)
	}
}

// writeWorktreeFile creates a file in the worktree to seed an intended change.
func writeWorktreeFile(t *testing.T, worktreeRoot, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(worktreeRoot, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDeliverReadyNotDraft(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	pr := &fakePRClient{}
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("ready"), map[string]string{"task": "x"}))
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if res.Status != "succeeded" || res.Mode != "ready" {
		t.Fatalf("Result = %+v, want succeeded/ready", res)
	}
	if n := pr.createdCount(); n != 1 {
		t.Fatalf("Create calls = %d, want 1", n)
	}
	if pr.created[0].Draft {
		t.Fatal("Create Draft = true, want false for mode ready")
	}
}

func TestDeliverDraftRefusesReadyExistingPR(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	pr := &fakePRClient{found: &PRRef{RemoteID: "12", URL: "https://example.com/pull/12", Draft: false}}

	_, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"}))
	if err == nil {
		t.Fatal("Deliver error = nil, want refusal for a ready existing PR")
	}
	assertZeroCreates(t, pr)
	rec := deliveryRecordByKey(t, repo, run)
	if rec.Status != "failed" {
		t.Fatalf("record status = %q, want failed", rec.Status)
	}
}

func TestDeliverDraftReusesDraftExistingPR(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	pr := &fakePRClient{found: &PRRef{RemoteID: "12", URL: "https://example.com/pull/12", Draft: true}}

	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"}))
	if err != nil {
		t.Fatalf("Deliver error: %v", err)
	}
	if res.RemoteID != "12" || res.URL != "https://example.com/pull/12" {
		t.Fatalf("Result = %+v, want existing draft PR", res)
	}
	assertZeroCreates(t, pr)
}

func TestDeliverReadyReusesReadyExistingPR(t *testing.T) {
	// A ready-mode run that created a ready PR on a failed earlier attempt
	// must resume it on retry instead of refusing its own PR.
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	pr := &fakePRClient{found: &PRRef{RemoteID: "13", URL: "https://example.com/pull/13", Draft: false}}

	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("ready"), map[string]string{"task": "x"}))
	if err != nil {
		t.Fatalf("Deliver error: %v", err)
	}
	if res.RemoteID != "13" || res.URL != "https://example.com/pull/13" {
		t.Fatalf("Result = %+v, want existing ready PR", res)
	}
	assertZeroCreates(t, pr)
}

func TestDeliverReadyRefusesDraftExistingPR(t *testing.T) {
	// A ready-mode run must not repurpose a draft PR it does not own.
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	pr := &fakePRClient{found: &PRRef{RemoteID: "14", URL: "https://example.com/pull/14", Draft: true}}

	_, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("ready"), map[string]string{"task": "x"}))
	if err == nil {
		t.Fatal("Deliver error = nil, want refusal for a draft existing PR in ready mode")
	}
	assertZeroCreates(t, pr)
}

func TestDeliverDuplicateResume(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	pr := &fakePRClient{}
	req := newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"})
	first, err := Deliver(ctx, repo, RealGit{}, pr, req)
	if err != nil {
		t.Fatalf("first Deliver: %v", err)
	}
	second, err := Deliver(ctx, repo, RealGit{}, pr, req)
	if err != nil {
		t.Fatalf("second Deliver: %v", err)
	}
	if second != first {
		t.Fatalf("second result %+v != first %+v", second, first)
	}
	if n := pr.createdCount(); n != 1 {
		t.Fatalf("Create calls = %d, want 1 across duplicate deliveries", n)
	}
}

func TestDeliverNoDiff(t *testing.T) {
	ctx := context.Background()
	_, _, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	pr := &fakePRClient{}
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"}))
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if res.Status != "no_diff" || res.Mode != "draft" || res.BaseRef != "main" || res.HeadRef != "wf/wt-test" || res.Provider != "github" || res.URL != "" {
		t.Fatalf("Result = %+v, want no_diff/draft/main/wf/wt-test/github with no URL", res)
	}
	if n := pr.createdCount(); n != 0 {
		t.Fatalf("Create calls = %d, want 0", n)
	}
	rec := deliveryRecordByKey(t, repo, run)
	if rec.Status != "no_diff" || rec.DiffRef != "" || rec.CommitSHA != "" {
		t.Fatalf("record = %+v, want no_diff with empty DiffRef/CommitSHA", rec)
	}
}

func TestDeliverWrongStatus(t *testing.T) {
	ctx := context.Background()
	_, _, gc, baseCommit, originURL, run, repo := newDeliveryFixtureStatus(t, workflowledger.RunStatusRunning)
	pr := &fakePRClient{}
	_, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"}))
	if err == nil || !IsRefusal(err) {
		t.Fatalf("Deliver err = %v, want RefusalError", err)
	}
	assertNoRecord(t, repo, run)
	assertZeroCreates(t, pr)
}

func TestDeliverModeNonePolicy(t *testing.T) {
	ctx := context.Background()
	_, _, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	pr := &fakePRClient{}
	_, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("none"), map[string]string{"task": "x"}))
	if err == nil || !IsRefusal(err) {
		t.Fatalf("Deliver err = %v, want RefusalError for mode none", err)
	}
	assertNoRecord(t, repo, run)
	assertZeroCreates(t, pr)
}

func TestDeliverBranchMismatch(t *testing.T) {
	ctx := context.Background()
	_, _, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	req := newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"})
	req.Branch = "wrong"
	pr := &fakePRClient{}
	_, err := Deliver(ctx, repo, RealGit{}, pr, req)
	if err == nil || !IsRefusal(err) {
		t.Fatalf("Deliver err = %v, want RefusalError", err)
	}
	assertZeroCreates(t, pr)
}

func TestDeliverBaseMoved(t *testing.T) {
	ctx := context.Background()
	repoRoot, _, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, repoRoot, "a2.txt", "x\n")
	runGit(t, repoRoot, "add", "a2.txt")
	runGit(t, repoRoot, "commit", "-m", "advance main")
	pr := &fakePRClient{}
	_, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"}))
	if err == nil || !IsRefusal(err) {
		t.Fatalf("Deliver err = %v, want RefusalError for moved base", err)
	}
	assertNoRecord(t, repo, run)
	assertZeroCreates(t, pr)
}

func TestDeliverOriginMismatch(t *testing.T) {
	ctx := context.Background()
	_, _, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	req := newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"})
	req.OriginURL = "https://github.com/other/repo"
	pr := &fakePRClient{}
	_, err := Deliver(ctx, repo, RealGit{}, pr, req)
	if err == nil || !IsRefusal(err) {
		t.Fatalf("Deliver err = %v, want RefusalError for origin mismatch", err)
	}
	assertZeroCreates(t, pr)
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
// record as the stage state instead of rewriting it: the only delivery
// upserts this attempt mints are pushed and succeeded, so the key's
// wf_delivery_upserted log stays seed(pending) + pushed + succeeded = 3
// events with no extra pending stage upsert.
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
	// seed's pending, plus the attempt's pushed and succeeded. Any extra
	// pending event would mean the retry path rewrote the stage record.
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
	if pending != 1 || pushed != 1 || succeeded != 1 {
		t.Fatalf("wf_delivery_upserted for key: pending=%d pushed=%d succeeded=%d, want 1/1/1 (seed + pushed + succeeded; no extra stage upsert)", pending, pushed, succeeded)
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

// failOncePRClient fails the first Create call, then delegates to a fake.
// It simulates a transient PR-API failure after the commit was pushed.
type failOncePRClient struct {
	fake   *fakePRClient
	mu     sync.Mutex
	failed bool
}

func (f *failOncePRClient) FindByHead(ctx context.Context, repo, head string) (*PRRef, error) {
	return f.fake.FindByHead(ctx, repo, head)
}

func (f *failOncePRClient) Create(ctx context.Context, repo string, in PRInput) (PRRef, error) {
	f.mu.Lock()
	if !f.failed {
		f.failed = true
		f.mu.Unlock()
		return PRRef{}, errors.New("create: transient failure")
	}
	f.mu.Unlock()
	return f.fake.Create(ctx, repo, in)
}

// TestDeliverDeterministicCommitSHA asserts the commit SHA survives a failed
// attempt: the retry resumes the pushed commit instead of re-committing, so
// both attempts record the same CommitSHA and only one PR is created.
func TestDeliverDeterministicCommitSHA(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	req := newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"})
	fake := &fakePRClient{}
	pr := &failOncePRClient{fake: fake}
	if _, err := Deliver(ctx, repo, RealGit{}, pr, req); err == nil {
		t.Fatal("first Deliver = nil error, want transient Create failure")
	}
	rec1 := deliveryRecordByKey(t, repo, run)
	if rec1.Status != "failed" || rec1.CommitSHA == "" || rec1.TreeSHA == "" {
		t.Fatalf("record after first attempt = %+v, want failed with CommitSHA and TreeSHA preserved", rec1)
	}
	res, err := Deliver(ctx, repo, RealGit{}, pr, req)
	if err != nil {
		t.Fatalf("second Deliver: %v", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("Status = %q, want succeeded", res.Status)
	}
	rec2 := deliveryRecordByKey(t, repo, run)
	if rec2.CommitSHA != rec1.CommitSHA {
		t.Fatalf("CommitSHA changed across attempts: %s -> %s (want identical)", rec1.CommitSHA, rec2.CommitSHA)
	}
	if rec2.TreeSHA != rec1.TreeSHA {
		t.Fatalf("TreeSHA changed across attempts: %s -> %s (want identical)", rec1.TreeSHA, rec2.TreeSHA)
	}
	if n := fake.createdCount(); n != 1 {
		t.Fatalf("successful Create calls = %d, want exactly 1", n)
	}
}

func TestDeliverPushFailureIsTransient(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	badOrigin := filepath.Join(t.TempDir(), "gone.git")
	runGit(t, worktreeRoot, "remote", "set-url", "origin", badOrigin)
	req := newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"})
	req.OriginURL = badOrigin
	pr := &fakePRClient{}
	if _, err := Deliver(ctx, repo, RealGit{}, pr, req); err == nil || IsRefusal(err) {
		t.Fatalf("Deliver err = %v, want a transient (non-refusal) error", err)
	}
	if n := pr.createdCount(); n != 0 {
		t.Fatalf("Create calls = %d, want 0", n)
	}
	rec := deliveryRecordByKey(t, repo, run)
	if rec.Status != "failed" || rec.ErrorRef == "" {
		t.Fatalf("record = %+v, want failed with ErrorRef", rec)
	}
	if stored, err := repo.GetRun(ctx, run.RunID); err != nil {
		t.Fatal(err)
	} else if stored.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("run status = %q, want delivery_pending (untouched)", stored.Status)
	}
}

func TestDeliverForeignCommitRefused(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	runGit(t, worktreeRoot, "add", "-A")
	runGit(t, worktreeRoot, "commit", "-m", "foreign")
	pr := &fakePRClient{}
	_, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"}))
	if err == nil || !IsRefusal(err) {
		t.Fatalf("Deliver err = %v, want RefusalError for foreign commits", err)
	}
	assertZeroCreates(t, pr)
	// Refusal is pre-stage: this attempt must not have written any record.
	assertNoRecord(t, repo, run)
}

func TestDeliverTemplateInjectionSingleArgv(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	inputs := map[string]string{
		"task":    "x\n--draft\n--base evil",
		"summary": "x --draft --base evil",
	}
	// The renderer (pinned by policy_test.go) rejects newlines in titles, so
	// the newline payload rides the commit message; both reach their consumer as ONE argv element.
	policy := Policy{
		Kind:                  "pull_request",
		Mode:                  "draft",
		Provider:              "github",
		Base:                  "main",
		TitleTemplate:         "{{ inputs.summary }}",
		CommitMessageTemplate: "{{ inputs.task }}",
	}
	pr := &fakePRClient{}
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, policy, inputs))
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("Status = %q, want succeeded", res.Status)
	}
	if n := pr.createdCount(); n != 1 {
		t.Fatalf("Create calls = %d, want 1", n)
	}
	c := pr.created[0]
	if c.Title != "x --draft --base evil" || !c.Draft || c.Body != wantBody(run) {
		t.Fatalf("Create = %+v, want exact rendered Title, Draft=true, Body=%q", c, wantBody(run))
	}
	if msg := runGitOut(t, worktreeRoot, "log", "-1", "--format=%B"); strings.TrimSuffix(msg, "\n") != "x\n--draft\n--base evil" {
		t.Fatalf("commit message = %q, want the newline payload intact", msg)
	}

	t.Run("newline title refused by renderer", func(t *testing.T) {
		_, wt2, gc2, base2, origin2, run2, repo2 := newDeliveryFixture(t)
		writeWorktreeFile(t, wt2, "b.txt", "change\n")
		bad := Policy{Kind: "pull_request", Mode: "draft", Provider: "github", Base: "main",
			TitleTemplate: "{{ inputs.task }}", CommitMessageTemplate: "{{ inputs.task }}"}
		pr2 := &fakePRClient{}
		if _, err := Deliver(ctx, repo2, RealGit{}, pr2, newRequest(run2, gc2, base2, origin2, bad, inputs)); err == nil {
			t.Fatal("Deliver with newline title = nil error, want failure")
		}
		if n := pr2.createdCount(); n != 0 {
			t.Fatalf("Create calls = %d, want 0", n)
		}
		rec := deliveryRecordByKey(t, repo2, run2)
		if rec.Status != "failed" || rec.ErrorRef == "" {
			t.Fatalf("record = %+v, want failed with ErrorRef", rec)
		}
	})
}

func TestFormatOutcome(t *testing.T) {
	if got := FormatOutcome(Result{Status: "succeeded", Mode: "draft", URL: "https://github.com/o/r/pull/7"}, nil); got != "PR created: https://github.com/o/r/pull/7 (mode=draft)" {
		t.Fatalf("succeeded outcome = %q", got)
	}
	if got := FormatOutcome(Result{Status: "no_diff", Mode: "draft", BaseRef: "main", HeadRef: "wf/x", Provider: "github"}, nil); got != "no diff to publish; run completed without a PR" {
		t.Fatalf("no_diff outcome = %q", got)
	}
	if got := FormatOutcome(Result{}, &RefusalError{Reason: "origin remote changed since admission"}); got != "delivery refused: origin remote changed since admission" {
		t.Fatalf("refused outcome = %q", got)
	}
	if got := FormatOutcome(Result{}, errors.New("push: boom")); got != "delivery attempt failed: push: boom; retry with: mivia workflow deliver <runid> --allow-publish" {
		t.Fatalf("transient outcome = %q", got)
	}
}

// assertNoRecord asserts the idempotency key has no delivery record yet
// (pre-stage refusals must not write one).
func assertNoRecord(t *testing.T, repo workflowledger.Repository, run workflowledger.RunSnapshot) {
	t.Helper()
	if _, err := repo.GetDeliveryByIdempotencyKey(context.Background(), DeliveryKey(run.RunID, run.WorkflowDigest)); !errors.Is(err, workflowledger.ErrNotFound) {
		t.Fatalf("delivery record exists after refusal, err = %v", err)
	}
}

func assertZeroCreates(t *testing.T, pr *fakePRClient) {
	t.Helper()
	if n := pr.createdCount(); n != 0 {
		t.Fatalf("Create calls = %d, want 0", n)
	}
}
