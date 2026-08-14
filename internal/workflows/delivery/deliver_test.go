package delivery

import (
	"context"
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

// gitConfig sets the test identity in a repo and pins line endings to LF:
// the delivery git context reads no system config (GIT_CONFIG_NOSYSTEM=1),
// so a Windows autocrlf checkout would otherwise produce CRLF working trees
// that the pinned status/diff commands report as modifications. Fixtures
// must be deterministic on every machine.
func gitConfig(t *testing.T, dir string) {
	t.Helper()
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	runGit(t, dir, "config", "core.autocrlf", "false")
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
	return newDeliveryFixtureStatusStoreOrigin(t, status, store, "")
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
// baseRefOID is attached to created PRs so tests can exercise the post-create
// base verification (AR-7).
type fakePRClient struct {
	mu         sync.Mutex
	found      *PRRef
	repos      []string
	created    []PRInput
	baseRefOID string
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
	return PRRef{RemoteID: strconv.Itoa(len(f.created)), URL: "https://example.com/pull/" + strconv.Itoa(len(f.created)), BaseRefOID: f.baseRefOID}, nil
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
	return "<sub>Co-authored-by: Mivia Agent <noreply@mivia.app></sub>\n\n" +
		"<details>\n<summary>Mivia Agent run details</summary>\n\n" +
		"- Run: [" + run.RunID + "](https://mivia.app/runs/" + run.RunID + ")\n" +
		"- Workflow digest: [" + run.WorkflowDigest + "](https://mivia.app/workflows/digest/" + run.WorkflowDigest + ")\n" +
		"\n</details>"
}

// assertNoBranchOnOrigin asserts the delivery branch never reached origin,
// which pins that validation failures precede any push.
func assertNoBranchOnOrigin(t *testing.T, repoRoot, originURL string) {
	t.Helper()
	if refs := runGitOut(t, repoRoot, "ls-remote", originURL); strings.Contains(refs, "refs/heads/wf/wt-test") {
		t.Fatalf("ls-remote origin has refs/heads/wf/wt-test, want no push before validation:\n%s", refs)
	}
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

// TestDeliverNoDiffSkipsCommitMessagePolicy pins the audit fix: the optional
// workspace commit-message policy is only enforced when a commit will actually
// be created. A clean-at-base run (no diff) never fires the repo's commit-msg
// hook, so a present policy with a non-conforming/empty rendered subject must
// NOT refuse: the run settles no_diff without a commit.
func TestDeliverNoDiffSkipsCommitMessagePolicy(t *testing.T) {
	ctx := context.Background()
	repoRoot, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorkspacePolicy(t, repoRoot, worktreeRoot, `{"version": 1, "requireScope": true, "maxSubjectLength": 72}`)
	pol := defaultPolicy("draft")
	pol.CommitMessageTemplate = "" // renders an empty subject; would violate requireScope if validated
	pr := &fakePRClient{}
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, pol, map[string]string{"task": "x"}))
	if err != nil {
		t.Fatalf("Deliver with no diff and a present commit-message policy = %v, want no refusal (no commit will be made)", err)
	}
	if res.Status != "no_diff" {
		t.Fatalf("Result = %+v, want no_diff", res)
	}
	if n := pr.createdCount(); n != 0 {
		t.Fatalf("Create calls = %d, want 0", n)
	}
	rec := deliveryRecordByKey(t, repo, run)
	if rec.Status != "no_diff" {
		t.Fatalf("record = %+v, want no_diff", rec)
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

// failOncePRClient fails the first Create call (transient PR-API failure), then delegates.
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

// TestDeliverFetchFailureIsTransient: an unreachable origin at fetch time is a
// recoverable transport failure - the run stays delivery_pending, no PR is
// created, and (pre-stage) no delivery record is written.
func TestDeliverFetchFailureIsTransient(t *testing.T) {
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
	assertNoRecord(t, repo, run)
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

// TestBaseStillContainsFetchesPinnedOriginURLNotMutableOriginName pins the
// AR-7 TOCTOU guard: baseStillContains must fetch by the pinned req.OriginURL
// (as verifyRemoteBaseAncestry already does), never by the local "origin"
// remote name, because the local remote config is mutable and could be
// repointed between admission and this post-PR-creation check. The test
// repoints the local "origin" remote to an unreachable path between the two
// checks: if baseStillContains fetched by "origin" it would fail (the object
// is not yet local and the broken remote cannot supply it); fetching by the
// pinned OriginURL succeeds regardless of what "origin" now points to.
func TestBaseStillContainsFetchesPinnedOriginURLNotMutableOriginName(t *testing.T) {
	ctx := context.Background()
	repoRoot, worktreeRoot, gc, baseCommit, originURL, run, _ := newDeliveryFixture(t)

	// Advance the admitted base on a SEPARATE clone (its own object store),
	// so the new commit (the PR's base, prBase) never lands in repoRoot's or
	// worktreeRoot's local objects and a real fetch is required to see it.
	scratch := t.TempDir()
	runGit(t, filepath.Dir(scratch), "clone", originURL, scratch)
	runGit(t, scratch, "checkout", "-b", "main", "origin/main")
	gitConfig(t, scratch)
	writeWorktreeFile(t, scratch, "c.txt", "advance\n")
	runGit(t, scratch, "add", "-A")
	runGit(t, scratch, "commit", "-m", "advance base")
	runGit(t, scratch, "push", "origin", "main")
	prBase := runGitOut(t, scratch, "rev-parse", "HEAD")

	// Repoint the local "origin" remote to an unreachable path, simulating a
	// mutable-remote repoint mid-attempt. req.OriginURL keeps the ADMITTED
	// URL, unaffected by the repoint.
	runGit(t, worktreeRoot, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "does-not-exist.git"))
	req := newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"})

	if err := baseStillContains(ctx, RealGit{}, req, baseCommit, prBase); err != nil {
		t.Fatalf("baseStillContains = %v, want nil: it must fetch the pinned OriginURL (%s), not the repointed local \"origin\" remote", err, originURL)
	}
	_ = repoRoot
}

// TestBaseStillContainsFailsClosedWhenPinnedOriginURLUnreachable is the
// converse of the above: when req.OriginURL (the pinned, admitted remote) is
// unreachable, baseStillContains must fail closed even though the local
// "origin" remote is perfectly healthy - proving the fetch really targets
// req.OriginURL and not the mutable local remote name.
func TestBaseStillContainsFailsClosedWhenPinnedOriginURLUnreachable(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, _ := newDeliveryFixture(t)

	scratch := t.TempDir()
	runGit(t, filepath.Dir(scratch), "clone", originURL, scratch)
	runGit(t, scratch, "checkout", "-b", "main", "origin/main")
	gitConfig(t, scratch)
	writeWorktreeFile(t, scratch, "c.txt", "advance\n")
	runGit(t, scratch, "add", "-A")
	runGit(t, scratch, "commit", "-m", "advance base")
	runGit(t, scratch, "push", "origin", "main")
	prBase := runGitOut(t, scratch, "rev-parse", "HEAD")

	// The local "origin" remote is left pointing at the healthy originURL,
	// but req.OriginURL (the pinned URL a real repoint attack would still
	// leave untouched) is broken.
	_ = worktreeRoot
	req := newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"})
	req.OriginURL = filepath.Join(t.TempDir(), "does-not-exist.git")

	if err := baseStillContains(ctx, RealGit{}, req, baseCommit, prBase); err == nil {
		t.Fatal("baseStillContains = nil, want a fetch error when the pinned OriginURL is unreachable, even though the local \"origin\" remote is healthy")
	}
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
	// The commit subject is always title (buildCommitMessage), never
	// CommitMessageTemplate directly - CommitMessageTemplate is body-only -
	// so the full message is title + blank line + the rendered body.
	wantMsg := "x --draft --base evil\n\nx\n--draft\n--base evil"
	if msg := runGitOut(t, worktreeRoot, "log", "-1", "--format=%B"); strings.TrimSuffix(msg, "\n") != wantMsg {
		t.Fatalf("commit message = %q, want %q (the newline payload intact in the body)", msg, wantMsg)
	}

	// The newline payload folds to one line and still publishes. The defense
	// against argv injection is the --title= equals form, which the assertion
	// above proves: the payload stays ONE argument. The fold makes the title
	// safer still, because the published title holds no newline at all.
	t.Run("newline title folded, not refused", func(t *testing.T) {
		_, wt2, gc2, base2, origin2, run2, repo2 := newDeliveryFixture(t)
		writeWorktreeFile(t, wt2, "b.txt", "change\n")
		folding := Policy{Kind: "pull_request", Mode: "draft", Provider: "github", Base: "main",
			TitleTemplate: "{{ inputs.task }}", CommitMessageTemplate: "{{ inputs.task }}"}
		pr2 := &fakePRClient{}
		if _, err := Deliver(ctx, repo2, RealGit{}, pr2, newRequest(run2, gc2, base2, origin2, folding, inputs)); err != nil {
			t.Fatalf("Deliver with newline title = %v, want success", err)
		}
		if n := pr2.createdCount(); n != 1 {
			t.Fatalf("Create calls = %d, want 1", n)
		}
		got := pr2.created[0].Title
		if strings.ContainsAny(got, "\n\r") {
			t.Fatalf("published title %q must hold no line break", got)
		}
		if got != "x --draft --base evil" {
			t.Fatalf("title = %q, want the folded single line", got)
		}
	})
}

// TestDeliverStageCallbackRecordsNoDiffStages pins the stage observability
// contract (G11): a delivery run with a Stage callback records each numbered
// stage with a stable name, in order. The no_diff path is the cheapest: guard,
// eligibility, then no_diff.
func TestDeliverStageCallbackRecordsNoDiffStages(t *testing.T) {
	ctx := context.Background()
	_, _, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	pr := &fakePRClient{}
	var stages []string
	req := newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"})
	req.Stage = func(stage, detail string) { stages = append(stages, stage) }
	res, err := Deliver(ctx, repo, RealGit{}, pr, req)
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if res.Status != "no_diff" {
		t.Fatalf("Result = %+v, want no_diff", res)
	}
	want := []string{"guard", "eligibility", "no_diff"}
	if !reflect.DeepEqual(stages, want) {
		t.Fatalf("stages = %v, want %v", stages, want)
	}
}

// TestDeliverStageCallbackRecordsSuccessStages pins the full ordered stage
// sequence of a publishing delivery: guard, eligibility, commit, push, pr,
// success.
func TestDeliverStageCallbackRecordsSuccessStages(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	pr := &fakePRClient{}
	var stages []string
	req := newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"})
	req.Stage = func(stage, detail string) { stages = append(stages, stage) }
	res, err := Deliver(ctx, repo, RealGit{}, pr, req)
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("Result = %+v, want succeeded", res)
	}
	want := []string{"guard", "eligibility", "commit", "push", "pr", "success"}
	if !reflect.DeepEqual(stages, want) {
		t.Fatalf("stages = %v, want %v", stages, want)
	}
}

// TestDeliverStageCallbackRecordsFailedStage pins the failed stage on an
// in-flight failure: a transient PR create failure records every stage up to
// and including failed.
func TestDeliverStageCallbackRecordsFailedStage(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	req := newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"})
	var stages []string
	req.Stage = func(stage, detail string) { stages = append(stages, stage) }
	pr := &failOncePRClient{fake: &fakePRClient{}}
	if _, err := Deliver(ctx, repo, RealGit{}, pr, req); err == nil {
		t.Fatal("Deliver error = nil, want the transient Create failure")
	}
	want := []string{"guard", "eligibility", "commit", "push", "pr", "failed"}
	if !reflect.DeepEqual(stages, want) {
		t.Fatalf("stages = %v, want %v", stages, want)
	}
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
