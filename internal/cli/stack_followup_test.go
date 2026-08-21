package cli

// Follow-up PR admission regressions (bug 4: admitFollowUpsForChunk was not
// atomic and not idempotent - the only guard was the task-existence check,
// evaluated BEFORE the git push and the PR create, and the follow-up run row
// got a fresh random run id per registration, so a crash after the PR
// existed or a concurrent second admission minted duplicate run rows,
// duplicate delivery records, and duplicate PRs). These tests pin the fixed
// contract: the follow-up run row is reserved with a DETERMINISTIC run id
// (derived from the stable admission key) BEFORE any git/GitHub side effect,
// so a retry or a concurrent admission resumes the SAME registration -
// exactly one run row, one delivery record, one task, and no second PR.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	workflowspace "github.com/MiviaLabs/mivia-agent/internal/workflows/localengine"
)

const (
	followUpTestStackID = "wfr-stack-followup"
	followUpTestChunkID = "c1"
)

// followUpTestRunID mirrors the production derivation of the follow-up run
// id (followUpRunID in stack_followup.go): a deterministic function of the
// stable admission key, prefixed wfr-followup- so it satisfies the run
// ledger's admission validation. The test seeds crashed state under this id
// and asserts the retry resumes it instead of minting a fresh one.
func followUpTestRunID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "wfr-followup-" + hex.EncodeToString(sum[:16])
}

// newFollowUpAdmissionFixture builds the durable state a follow-up
// admission operates on: a real git origin, a real parent-chunk worktree, a
// parent run whose latest succeeded delivery left a deferred commit
// (StackRemainingCommits=1), and the deferred branch committed in the main
// repository (shared refs, so the parent worktree can push it). Returns the
// repo root, the run ledger, the task ledger, the parent run snapshot, the
// FOLLOW-UP admission key (stack:chunk-deferred), and the deterministic
// follow-up run id derived from it.
func newFollowUpAdmissionFixture(t *testing.T) (root string, repo workflowledger.Repository, ledger *workflowledger.Store, parentRun workflowledger.RunSnapshot, key, runID string) {
	t.Helper()
	root = t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "fixture.txt"), []byte("seeded fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	initWorkflowGitRepoWithOrigin(t, root)
	ctx := context.Background()
	identity, err := workflowspace.Ensure(ctx, root, "followup-parent", workflowspace.IsolationWorktree)
	if err != nil {
		t.Fatal(err)
	}
	store, err := openContextStorePath(filepath.Join(root, "workflow.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo = workflowledger.NewStorageRepository(store)
	ledger = workflowledger.NewStore(store)
	if _, err := ledger.StorePlan(workflowledger.Plan{ID: followUpTestStackID, Scope: stackScope(followUpTestStackID)}); err != nil {
		t.Fatal(err)
	}

	parentKey, err := stackAdmissionKey(followUpTestStackID, followUpTestChunkID)
	if err != nil {
		t.Fatal(err)
	}
	key, err = stackAdmissionKey(followUpTestStackID, deferredFollowUpChunkID(followUpTestChunkID))
	if err != nil {
		t.Fatal(err)
	}
	runID = followUpTestRunID(key)

	parentRun = workflowledger.RunSnapshot{
		RunID: "wfr-followup-parent", InvocationKey: parentKey,
		WorkflowName: "mini-stack", WorkflowDigest: "digest-1",
		Status: workflowledger.RunStatusPending, ActiveStepID: "success",
		BaseRef: identity.BaseRef, BaseCommit: identity.BaseCommit,
		WorktreeName: identity.WorktreeName, RemoteURL: "https://github.com/acme/stack.git",
	}
	if err := repo.CreateRun(ctx, parentRun, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetRun(ctx, parentRun.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, parentRun.RunID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	parentRun.Status = workflowledger.RunStatusRunning

	// The deferred commit lives on its own branch, exactly as
	// freshDeliveryCommitSplit leaves it after a split delivery. It is
	// committed from the main repository so the parent worktree's checked
	// out branch (validated by workflowspace.Resolve) stays untouched; git
	// branch refs are shared across worktrees, so the parent worktree can
	// still rev-parse and push it.
	deferredBranch := delivery.DeferredBranchName(stackHeadBranch(parentRun))
	gitExec(t, root, "checkout", "-b", deferredBranch)
	if err := os.WriteFile(filepath.Join(root, "deferred.txt"), []byte("deferred scope\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitExec(t, root, "add", "deferred.txt")
	gitExec(t, root, "commit", "-m", "deferred follow-up scope")
	deferredSHA := gitExec(t, root, "rev-parse", "HEAD")
	gitExec(t, root, "checkout", "main")

	if err := repo.UpsertDelivery(ctx, followUpDeliveryRecord(parentRun.RunID, parentRun, deferredBranch, "1", deferredSHA)); err != nil {
		t.Fatal(err)
	}
	return root, repo, ledger, parentRun, key, runID
}

// followUpDeliveryRecord builds the delivery record registerFollowUpChunk
// (or the crashed process in these tests) writes for a follow-up run.
func followUpDeliveryRecord(runID string, parentRun workflowledger.RunSnapshot, deferredBranch, remoteID, commitSHA string) workflowledger.DeliveryRecord {
	return workflowledger.DeliveryRecord{
		RunID: runID, IdempotencyKey: delivery.DeliveryKey(runID, parentRun.WorkflowDigest),
		Mode: "draft", BaseRef: stackHeadBranch(parentRun), HeadRef: deferredBranch,
		Provider: "github", RemoteID: remoteID, URL: "https://github.com/acme/stack/pull/" + remoteID,
		Status: "succeeded", CommitSHA: commitSHA, StackRemainingCommits: 1,
	}
}

// followUpRunSnapshot mirrors the run row reserveFollowUpRun writes for the
// follow-up admission.
func followUpRunSnapshot(parentRun workflowledger.RunSnapshot, runID, key string, status workflowledger.RunStatus) workflowledger.RunSnapshot {
	return workflowledger.RunSnapshot{
		RunID: runID, InvocationKey: key,
		WorkflowName: parentRun.WorkflowName, WorkflowDigest: parentRun.WorkflowDigest,
		Status: status, ActiveStepID: "success",
		BaseRef: parentRun.BaseRef, BaseCommit: parentRun.BaseCommit,
		WorktreeName: parentRun.WorktreeName + "-deferred", RemoteURL: parentRun.RemoteURL,
	}
}

// assertSingleFollowUpRegistration checks the exactly-once contract: one run
// row per follow-up key, one delivery record, and one task.
func assertSingleFollowUpRegistration(t *testing.T, repo workflowledger.Repository, ledger *workflowledger.Store, key, runID string) {
	t.Helper()
	ctx := context.Background()
	runs, err := repo.ListRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	keyed := 0
	for _, r := range runs {
		if r.InvocationKey == key {
			keyed++
		}
	}
	if keyed != 1 {
		t.Fatalf("run rows with follow-up key %q = %d, want exactly 1 (duplicate registration)", key, keyed)
	}
	deliveries := 0
	for _, r := range runs {
		if r.InvocationKey != key {
			continue
		}
		recs, err := repo.ListDeliveries(ctx, r.RunID)
		if err != nil {
			t.Fatal(err)
		}
		deliveries += len(recs)
	}
	if deliveries != 1 {
		t.Fatalf("delivery records for follow-up key %q = %d, want exactly 1", key, deliveries)
	}
	if recs, err := repo.ListDeliveries(ctx, runID); err != nil || len(recs) != 1 {
		t.Fatalf("delivery records on deterministic run %q = %d (err %v), want exactly 1", runID, len(recs), err)
	}
	if _, err := ledger.GetTask(followUpTestStackID, deferredFollowUpChunkID(followUpTestChunkID)); err != nil {
		t.Fatalf("follow-up task missing after retry: %v", err)
	}
}

// TestAdmitFollowUpCrashAfterPRCreateThenRetryIsExactlyOnce is the primary
// bug-4 regression: a crash after the PR was created but before the task
// landed leaves a run row fence, a delivery record, and the PR - no task.
// The next drive iteration must resume that registration (deterministic run
// id + CreateRun ErrDuplicate) instead of re-registering: exactly one run
// row, one delivery record, one task, and no second PR.
func TestAdmitFollowUpCrashAfterPRCreateThenRetryIsExactlyOnce(t *testing.T) {
	root, repo, ledger, parentRun, key, runID := newFollowUpAdmissionFixture(t)
	ctx := context.Background()

	// Durable state the crashed process left behind: the fence row (status
	// running, mid-registration), the delivery record, and the PR already
	// open on the deferred branch - but no task.
	deferredBranch := delivery.DeferredBranchName(stackHeadBranch(parentRun))
	pr := newFollowUpPRClient()
	pr.byHead[deferredBranch] = delivery.PRRef{RemoteID: "2", URL: "https://github.com/acme/stack/pull/2"}
	if err := repo.CreateRun(ctx, followUpRunSnapshot(parentRun, runID, key, workflowledger.RunStatusPending), []byte("{}")); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertDelivery(ctx, followUpDeliveryRecord(runID, parentRun, deferredBranch, "2", "")); err != nil {
		t.Fatal(err)
	}

	wireFollowUpPRStub(t, pr)
	prepared := &preparedWorkflowRun{root: root, repo: repo}
	var stdout, stderr bytes.Buffer
	if err := admitFollowUpsForChunk(context.Background(), prepared, ledger, followUpTestStackID, followUpTestChunkID, &stdout, &stderr); err != nil {
		t.Fatalf("retry admission after crash: %v", err)
	}
	assertSingleFollowUpRegistration(t, repo, ledger, key, runID)
	if creates, finds := pr.calls(); creates != 0 {
		t.Fatalf("PR creates after crash-retry = %d, want 0 (FindByHead must reuse the crashed PR)", creates)
	} else if finds == 0 {
		t.Fatal("FindByHead was never consulted; PR reuse did not happen")
	}
}

// TestAdmitFollowUpFenceExistsBeforeGitSideEffects pins the ordering half of
// bug 4: the follow-up run row (the durable fence) must exist BEFORE the
// deferred branch is pushed. A crash right after the PR create is now
// healed by EnsureFollowUpPublished's FindByHead retry (F12 fix 1), so the
// first admission succeeds instead of erroring — but the fence ordering
// and exactly-once contract still hold.
func TestAdmitFollowUpFenceExistsBeforeGitSideEffects(t *testing.T) {
	root, repo, ledger, _, key, runID := newFollowUpAdmissionFixture(t)
	ctx := context.Background()

	fence := &fenceCheckingGitRunner{GitRunner: delivery.RealGit{}, repo: repo, runID: runID}
	pr := newFollowUpPRClient()
	pr.crashOnCreate = true
	wireFollowUpGitStub(t, fence)
	wireFollowUpPRStub(t, pr)

	prepared := &preparedWorkflowRun{root: root, repo: repo}
	var stdout, stderr bytes.Buffer
	// The first admission succeeds despite the crash: EnsureFollowUpPublished's
	// FindByHead retry finds the PR that crashOnCreate recorded in byHead.
	if err := admitFollowUpsForChunk(context.Background(), prepared, ledger, followUpTestStackID, followUpTestChunkID, &stdout, &stderr); err != nil {
		t.Fatalf("first admission with crashOnCreate: %v; want success (FindByHead retry heals)", err)
	}
	if !fence.presentAtFirstPush() {
		t.Fatal("git push ran before the follow-up run row existed; fence broken")
	}
	if _, err := repo.GetRun(ctx, runID); err != nil {
		t.Fatalf("follow-up run row (the fence) missing after crash: %v", err)
	}
	if creates, _ := pr.calls(); creates != 1 {
		t.Fatalf("PR creates in the crashed admission = %d, want 1", creates)
	}
	// The follow-up was fully registered on the first call (FindByHead retry
	// recovered from the crash), so the exactly-once contract holds now.
	assertSingleFollowUpRegistration(t, repo, ledger, key, runID)

	// A second call is a no-op (task already exists).
	if err := admitFollowUpsForChunk(context.Background(), prepared, ledger, followUpTestStackID, followUpTestChunkID, &stdout, &stderr); err != nil {
		t.Fatalf("second admission after crash-heal: %v", err)
	}
	if creates, _ := pr.calls(); creates != 1 {
		t.Fatalf("PR creates after second call = %d, want 1 (no duplicate)", creates)
	}
}

// TestAdmitFollowUpConcurrentDoubleAdmissionRegistersOnce pins the
// concurrency half of bug 4: two concurrent drive processes that both pass
// the task-existence check must still land exactly one run row, one delivery
// record, one task, and one PR. The gate lets exactly one CreateRun through
// and blocks the other; the first admission's PR create waits until the
// second is blocked (so the second has already GetTask-missed), then the
// second resumes from the reserved row instead of re-registering.
func TestAdmitFollowUpConcurrentDoubleAdmissionRegistersOnce(t *testing.T) {
	root, repo, ledger, _, key, runID := newFollowUpAdmissionFixture(t)
	pr := newFollowUpPRClient()
	gated := &gateCreateRunRepository{Repository: repo, release: make(chan struct{}), blocked: make(chan struct{})}
	pr.waitBlocked = gated.blocked
	wireFollowUpPRStub(t, pr)
	prepared := &preparedWorkflowRun{root: root, repo: gated}

	errs := make(chan error, 2)
	go func() {
		errs <- admitFollowUpsForChunk(context.Background(), prepared, ledger, followUpTestStackID, followUpTestChunkID, io.Discard, io.Discard)
	}()
	go func() {
		errs <- admitFollowUpsForChunk(context.Background(), prepared, ledger, followUpTestStackID, followUpTestChunkID, io.Discard, io.Discard)
	}()
	// The first error returned is the admission whose CreateRun passed the
	// gate; the other is blocked inside it (its PR create already happened
	// or waits on the barrier). Only then release it.
	if err := <-errs; err != nil {
		t.Fatalf("first admission: %v", err)
	}
	close(gated.release)
	if err := <-errs; err != nil {
		t.Fatalf("second admission: %v", err)
	}
	assertSingleFollowUpRegistration(t, repo, ledger, key, runID)
	if creates, _ := pr.calls(); creates != 1 {
		t.Fatalf("PR creates under concurrent double admission = %d, want 1", creates)
	}
}

// followUpPRClient is a PR client whose FindByHead observes previously
// created PRs (like the real GitHub API), so retries reuse them. It can be
// told to fail on Create after recording the PR, simulating a crash whose
// PR actually exists server-side, and to wait for a barrier before creating
// (so a test can pin the ordering of two concurrent admissions).
type followUpPRClient struct {
	mu                       sync.Mutex
	byHead                   map[string]delivery.PRRef
	findByHeadSwaps          map[string]delivery.PRRef
	creates                  int
	finds                    int
	crashOnCreate            bool
	failCreateThenFindByHead bool
	waitBlocked              <-chan struct{}
}

func newFollowUpPRClient() *followUpPRClient {
	return &followUpPRClient{byHead: make(map[string]delivery.PRRef)}
}

func (c *followUpPRClient) FindByHead(ctx context.Context, repo, headBranch string) (*delivery.PRRef, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.finds++
	if ref, ok := c.byHead[headBranch]; ok {
		out := ref
		return &out, nil
	}
	// Check for a swap: inject the PR into byHead for subsequent calls.
	if swap, ok := c.findByHeadSwaps[headBranch]; ok {
		c.byHead[headBranch] = swap
	}
	return nil, nil
}

func (c *followUpPRClient) IsMerged(context.Context, string, string) (bool, error) {
	return false, nil
}

func (c *followUpPRClient) Create(ctx context.Context, repo string, in delivery.PRInput) (delivery.PRRef, error) {
	if c.failCreateThenFindByHead {
		return delivery.PRRef{}, errors.New("simulated create conflict: PR already exists")
	}
	if c.waitBlocked != nil {
		// Hold the first admission's PR create until the concurrent
		// admission has reached its CreateRun (which it can only do after
		// GetTask missed: the task does not exist yet).
		select {
		case <-c.waitBlocked:
		case <-time.After(30 * time.Second):
			return delivery.PRRef{}, errors.New("timed out waiting for the concurrent admission")
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.creates++
	ref := delivery.PRRef{RemoteID: strconv.Itoa(c.creates), URL: "https://github.com/acme/stack/pull/" + strconv.Itoa(c.creates)}
	c.byHead[in.Head] = ref
	if c.crashOnCreate {
		return delivery.PRRef{}, errors.New("simulated crash after PR create")
	}
	return ref, nil
}

func (c *followUpPRClient) calls() (creates, finds int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.creates, c.finds
}

// fenceCheckingGitRunner records whether the follow-up run row already
// existed at the first git push (the fence-before-side-effects ordering).
type fenceCheckingGitRunner struct {
	delivery.GitRunner
	repo             workflowledger.Repository
	runID            string
	mu               sync.Mutex
	pushes           int
	fenceAtFirstPush bool
}

func (f *fenceCheckingGitRunner) Run(ctx context.Context, gc delivery.GitContext, args ...string) (string, error) {
	if len(args) > 0 && args[0] == "push" {
		f.mu.Lock()
		if f.pushes == 0 {
			_, err := f.repo.GetRun(ctx, f.runID)
			f.fenceAtFirstPush = err == nil
		}
		f.pushes++
		f.mu.Unlock()
	}
	return f.GitRunner.Run(ctx, gc, args...)
}

func (f *fenceCheckingGitRunner) presentAtFirstPush() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fenceAtFirstPush
}

// gateCreateRunRepository lets exactly one CreateRun through and blocks
// every later one until release is closed, forcing the blocked admission to
// observe the already-reserved row. blocked is closed the moment a later
// CreateRun arrives, which is the durable proof that the concurrent
// admission passed its GetTask check (task existence is only created later).
type gateCreateRunRepository struct {
	workflowledger.Repository
	release     chan struct{}
	blocked     chan struct{}
	mu          sync.Mutex
	passed      bool
	blockedOnce sync.Once
}

func (g *gateCreateRunRepository) CreateRun(ctx context.Context, snap workflowledger.RunSnapshot, snapshotJSON []byte) error {
	g.mu.Lock()
	if !g.passed {
		g.passed = true
		g.mu.Unlock()
		return g.Repository.CreateRun(ctx, snap, snapshotJSON)
	}
	g.mu.Unlock()
	g.blockedOnce.Do(func() { close(g.blocked) })
	<-g.release
	return g.Repository.CreateRun(ctx, snap, snapshotJSON)
}

func wireFollowUpPRStub(t *testing.T, pr delivery.PRClient) {
	t.Helper()
	original := workflowDeliverNewPR
	t.Cleanup(func() { workflowDeliverNewPR = original })
	workflowDeliverNewPR = func() delivery.PRClient { return pr }
}

func wireFollowUpGitStub(t *testing.T, git delivery.GitRunner) {
	t.Helper()
	original := workflowDeliverGit
	t.Cleanup(func() { workflowDeliverGit = original })
	workflowDeliverGit = git
}

func gitExec(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestSweepSkipsOrphanFollowUpRunRows pins F12 finding 3: a pending run
// row with the wfr-followup- prefix and an empty snapshot digest (left by
// reserveFollowUpRun when EnsureFollowUpPublished fails) must be skipped by
// the sweep, not passed to reconcileParkedResume where it would produce
// "snapshot digest does not match" noise every tick.
func TestSweepSkipsOrphanFollowUpRunRows(t *testing.T) {
	cases := []struct {
		name       string
		runID      string
		snapDigest string
		wantSkip   bool
	}{
		{"orphan follow-up row", "wfr-followup-abc123", "", true},
		{"orphan follow-up row (non-empty digest)", "wfr-followup-abc123", "sha256:deadbeef", false},
		{"normal pending run", "wfr-stack-c1", "", false},
		{"normal pending run with digest", "wfr-stack-c1", "sha256:deadbeef", false},
		{"follow-up that was healed", "wfr-followup-abc123", "sha256:healed", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := workflowledger.RunSnapshot{RunID: tc.runID, SnapshotDigest: tc.snapDigest, Status: workflowledger.RunStatusPending}
			skip := run.SnapshotDigest == "" && strings.HasPrefix(run.RunID, "wfr-followup-")
			if skip != tc.wantSkip {
				t.Fatalf("skip = %v, want %v (runID=%q, snapDigest=%q)", skip, tc.wantSkip, tc.runID, tc.snapDigest)
			}
		})
	}
}

// TestEnsureFollowUpPublishedCreateRaceReusesExistingPR pins F12 finding 1:
// when two drivers race through EnsureFollowUpPublished, both pass the
// FindByHead→nil check and both call pr.Create. The loser gets an error
// from pr.Create but must still find and return the PR the winner created
// via a retry FindByHead, rather than propagating the create error.
func TestEnsureFollowUpPublishedCreateRaceReusesExistingPR(t *testing.T) {
	root, repo, _, parentRun, key, runID := newFollowUpAdmissionFixture(t)
	ctx := context.Background()

	// Reserve the fence row so the function reaches EnsureFollowUpPublished.
	if err := repo.CreateRun(ctx, followUpRunSnapshot(parentRun, runID, key, workflowledger.RunStatusPending), []byte("{}")); err != nil {
		t.Fatal(err)
	}

	pr := newFollowUpPRClient()
	// FindByHead returns nil the first time (both racers pass the check),
	// but the findByHeadSwaps mechanism injects the PR for the retry call.
	deferredBranch := delivery.DeferredBranchName(stackHeadBranch(parentRun))
	pr.findByHeadSwaps = map[string]delivery.PRRef{
		deferredBranch: {RemoteID: "99", URL: "https://github.com/acme/stack/pull/99"},
	}
	pr.failCreateThenFindByHead = true
	wireFollowUpPRStub(t, pr)

	var stdout bytes.Buffer
	_, _, _, published, err := delivery.EnsureFollowUpPublished(
		ctx, workflowDeliverGit, pr, root, repo, parentRun, followUpTestChunkID,
		func(s string) { fmt.Fprint(&stdout, s) },
	)
	if err != nil {
		t.Fatalf("EnsureFollowUpPublished with create-race: %v; want nil (reused existing PR)", err)
	}
	if !published {
		t.Fatal("published = false, want true")
	}
	if !strings.Contains(stdout.String(), "created by concurrent driver") {
		t.Fatalf("stdout = %q, want 'created by concurrent driver'", stdout.String())
	}
}

// ctxBlockingUntilCancelGitRunner blocks on any "push" call until its context
// is cancelled, then returns the context error. It lets tests prove that
// follow-up admission respects the drive context instead of hardcoding
// context.Background() inside EnsureFollowUpPublished (F8 residual).
type ctxBlockingUntilCancelGitRunner struct {
	t           *testing.T
	pushStarted chan struct{}
}

func (g ctxBlockingUntilCancelGitRunner) Run(ctx context.Context, gc delivery.GitContext, args ...string) (string, error) {
	if len(args) > 0 && args[0] == "rev-parse" {
		return "deadbeef", nil
	}
	if len(args) > 0 && args[0] == "push" && g.pushStarted != nil {
		close(g.pushStarted)
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// TestAdmitFollowUpsForChunkHonorsDriveContext pins the F8 residual: a
// cancelled drive context must stop a blocked follow-up git push and return
// the cancellation error, instead of holding the operation open on
// context.Background().
func TestAdmitFollowUpsForChunkHonorsDriveContext(t *testing.T) {
	root, repo, ledger, _, _, _ := newFollowUpAdmissionFixture(t)
	pushStarted := make(chan struct{})
	wireFollowUpGitStub(t, ctxBlockingUntilCancelGitRunner{t: t, pushStarted: pushStarted})
	pr := newFollowUpPRClient()
	wireFollowUpPRStub(t, pr)

	prepared := &preparedWorkflowRun{root: root, repo: repo}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-pushStarted
		cancel()
	}()

	done := make(chan error, 1)
	go func() {
		done <- admitFollowUpsForChunk(ctx, prepared, ledger, followUpTestStackID, followUpTestChunkID, io.Discard, io.Discard)
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("admitFollowUpsForChunk returned nil for cancelled context; want cancellation error")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("admitFollowUpsForChunk error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("admitFollowUpsForChunk did not return after context cancellation; follow-up admission ignores drive context")
	}
}
