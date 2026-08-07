package localengine_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/localengine"
)

// deliverMeTOML is the same delivery workflow used by the integration suite.
const deliverMeTOML = `version = 1
name = "deliver-me"
initial_step = "one"

[inputs.task]
type = "string"
required = true
max_bytes = 100

[delivery]
kind = "pull_request"
mode = "draft"
provider = "github"
base = "main"

[[steps]]
id = "one"
kind = "agent"
agent = "one"
on_failure = "failure"

[[transitions]]
from = "one"
to = "success"
[transitions.match]
status = "succeeded"
`

// twoStepTOML is a non-delivery workflow used to exercise start/resume worktree
// handling without publication.
const twoStepTOML = `version = 1
name = "two-step"
initial_step = "one"

[inputs.task]
type = "string"
required = true
max_bytes = 100

[[steps]]
id = "one"
kind = "agent"
agent = "one"
on_failure = "failure"

[[steps]]
id = "two"
kind = "agent"
agent = "two"
on_failure = "failure"

[[transitions]]
from = "one"
to = "two"
[transitions.match]
status = "succeeded"

[[transitions]]
from = "two"
to = "success"
[transitions.match]
status = "succeeded"
`

// TestEngineDeliverResolvesWorktreeGitDir is the regression test for the
// empty-GitDir delivery bug: Engine.Deliver pinned every git command with
// GIT_DIR= (empty) plus GIT_WORK_TREE=<workspace root>, so git refused every
// command with "cannot resolve worktree HEAD branch" and delivery was
// impossible through the engine/tool path. The run's delivery workspace is
// seeded directly (like the delivery package fixture) so the test exercises
// the real pinned-git path end to end: resolve worktree identity, verify the
// real git dir, commit the intended diff, push to origin, and open a PR.
func TestEngineDeliverResolvesWorktreeGitDir(t *testing.T) {
	repoRoot, originURL, run, repo := newSeededDeliveryFixture(t)
	pr := &recordingPR{}
	engine := &localengine.Engine{WorkspaceRoot: repoRoot, Repo: repo, PR: pr}

	res, err := engine.Deliver(context.Background(), run.RunID, true)
	if err != nil {
		t.Fatalf("Engine.Deliver: %v", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("deliver result = %+v, want succeeded", res)
	}
	created := pr.createdPRs()
	if len(created) != 1 {
		t.Fatalf("PR create calls = %d, want 1: %+v", len(created), created)
	}
	if created[0].Base != "main" || created[0].Head != "wf/wt-test" {
		t.Fatalf("PR input = %+v, want base=main head=wf/wt-test", created[0])
	}
	fresh, err := repo.GetRun(context.Background(), run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run status = %q, want succeeded", fresh.Status)
	}
	rec, err := repo.GetDeliveryByIdempotencyKey(context.Background(), delivery.DeliveryKey(run.RunID, run.WorkflowDigest))
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != "succeeded" {
		t.Fatalf("delivery record status = %q, want succeeded", rec.Status)
	}
	// The push stage must have published the branch to the origin remote.
	if out := runGitOutT(t, originURL, "show-ref", "--verify", "--hash", "refs/heads/wf/wt-test"); out == "" {
		t.Fatal("branch wf/wt-test was not pushed to origin")
	}
}

// TestEngineStartCreatesRunWorktreeAndDelivers pins that startNew creates the
// run's git worktree (like the CLI's selectWorkflowWorkspace) and that a
// subsequent Engine.Deliver on the same engine resolves that worktree, commits
// the intended diff, pushes, and opens a PR. The current engine never created
// the worktree, so delivery refused at "worktree HEAD is on branch" even with
// a valid git dir.
func TestEngineStartCreatesRunWorktreeAndDelivers(t *testing.T) {
	repoRoot, originURL := newRealDeliveryRepo(t)
	writeFileT(t, filepath.Join(repoRoot, ".mivia", "workflows", "deliver-me.toml"), deliverMeTOML)

	repo := workflowledger.NewMemoryRepository()
	pr := &recordingPR{}
	engine := &localengine.Engine{
		WorkspaceRoot: repoRoot,
		Repo:          repo,
		NewRunner: func() controller.AgentStepRunner {
			return &localengine.StaticStepRunner{Output: json.RawMessage(`{"ok":true}`)}
		},
		NewRunID: func() string { return "wfr-test" },
		PR:       pr,
	}
	started, err := engine.Start(context.Background(), agenttools.StartRequest{
		Workflow: "deliver-me", Inputs: map[string]any{"task": "build"},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := engine.Wait(waitCtx, started.RunID); err != nil {
		t.Fatal(err)
	}
	run, err := repo.GetRun(context.Background(), started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("run status = %q, want delivery_pending", run.Status)
	}

	// startNew must have created a real worktree on branch wf/<run>.
	worktreeRoot := filepath.Join(repoRoot, ".mivia", "worktrees", "workflow-wfr-test")
	if _, err := os.Stat(worktreeRoot); err != nil {
		t.Fatalf("run worktree %s was not created: %v", worktreeRoot, err)
	}
	if headBranch := runGitOutT(t, worktreeRoot, "rev-parse", "--abbrev-ref", "HEAD"); headBranch != "wf/workflow-wfr-test" {
		t.Fatalf("worktree HEAD branch = %q, want wf/workflow-wfr-test", headBranch)
	}
	// An uncommitted change makes the intended diff non-empty.
	writeFileT(t, filepath.Join(worktreeRoot, "a.txt"), "base\nchanged\n")

	res, err := engine.Deliver(context.Background(), started.RunID, true)
	if err != nil {
		t.Fatalf("Engine.Deliver: %v", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("deliver result = %+v, want succeeded", res)
	}
	created := pr.createdPRs()
	if len(created) != 1 {
		t.Fatalf("PR create calls = %d, want 1: %+v", len(created), created)
	}
	if created[0].Base != "main" || created[0].Head != "wf/workflow-wfr-test" {
		t.Fatalf("PR input = %+v, want base=main head=wf/workflow-wfr-test", created[0])
	}
	if out := runGitOutT(t, originURL, "show-ref", "--verify", "--hash", "refs/heads/wf/workflow-wfr-test"); out == "" {
		t.Fatal("branch wf/workflow-wfr-test was not pushed to origin")
	}
	fresh, err := repo.GetRun(context.Background(), started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run status = %q, want succeeded", fresh.Status)
	}
}

// TestEngineStartRejectsDeliveryWithoutOrigin pins that startNew fails a
// delivery-active workflow when the workspace git repository has no origin
// remote. The run must not be created and must not reach delivery_pending.
func TestEngineStartRejectsDeliveryWithoutOrigin(t *testing.T) {
	repoRoot := newLocalRepoNoOrigin(t)
	writeFileT(t, filepath.Join(repoRoot, ".mivia", "workflows", "deliver-me.toml"), deliverMeTOML)

	repo := workflowledger.NewMemoryRepository()
	engine := &localengine.Engine{
		WorkspaceRoot: repoRoot,
		Repo:          repo,
		NewRunner: func() controller.AgentStepRunner {
			return &localengine.StaticStepRunner{Output: json.RawMessage(`{"ok":true}`)}
		},
		NewRunID: func() string { return "wfr-no-origin" },
	}
	_, err := engine.Start(context.Background(), agenttools.StartRequest{
		Workflow: "deliver-me", Inputs: map[string]any{"task": "build"},
	})
	if err == nil {
		t.Fatal("expected Start to fail without an origin remote")
	}
	if !strings.Contains(err.Error(), "origin remote") {
		t.Fatalf("error = %q, want origin remote mention", err.Error())
	}
	if _, err := repo.GetRun(context.Background(), "wfr-no-origin"); !errors.Is(err, workflowledger.ErrNotFound) {
		t.Fatalf("expected no run record, got err=%v", err)
	}
}

// TestEngineResumeRefusesMissingRunWorktree prevents resume from replacing a
// lost worktree with a clean base checkout and discarding unfinished edits.
func TestEngineResumeRefusesMissingRunWorktree(t *testing.T) {
	repoRoot, _ := newRealDeliveryRepo(t)
	writeFileT(t, filepath.Join(repoRoot, ".mivia", "workflows", "two-step.toml"), twoStepTOML)

	repo := workflowledger.NewMemoryRepository()
	block := make(chan struct{})
	entered := make(chan struct{}, 1)
	engine := &localengine.Engine{
		WorkspaceRoot: repoRoot,
		Repo:          repo,
		NewRunner: func() controller.AgentStepRunner {
			return &localengine.StaticStepRunner{
				Output:     json.RawMessage(`{"ok":true}`),
				BlockUntil: block,
				OnStep: func(controller.AgentStepRequest) {
					select {
					case entered <- struct{}{}:
					default:
					}
				},
			}
		},
		NewRunID: func() string { return "wfr-test" },
	}
	started, err := engine.Start(context.Background(), agenttools.StartRequest{
		Workflow: "two-step", Inputs: map[string]any{"task": "x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("step did not start")
	}
	if err := engine.Interrupt(started.RunID); err != nil {
		t.Fatal(err)
	}
	close(block)

	// Remove the run worktree as if a prior process cleaned it up.
	worktreeRoot := filepath.Join(repoRoot, ".mivia", "worktrees", "workflow-wfr-test")
	if _, err := os.Stat(worktreeRoot); err != nil {
		t.Fatalf("worktree missing before removal: %v", err)
	}
	if err := vcs.RemoveWithPrefix(context.Background(), repoRoot, "workflow-wfr-test", "wf/"); err != nil {
		t.Fatal(err)
	}
	_, err = engine.Start(context.Background(), agenttools.StartRequest{
		Resume: true, RunID: started.RunID, Force: true,
	})
	if err == nil || !strings.Contains(err.Error(), "unfinished edits cannot be recovered") {
		t.Fatalf("resume error = %v, want missing-worktree refusal", err)
	}
}

// TestEngineDeliverRefusalWithoutWorktree is the regression test for the
// wedged-delivery bug: a deliveryGitCtx refusal (here a run with no recorded
// worktree) returned as a plain error, so the run stayed in delivery_pending
// forever. Engine.Deliver must settle it: Refused=true with status
// delivery_failed, and the ledger run must be terminal delivery_failed.
func TestEngineDeliverRefusalWithoutWorktree(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	run := createDeliveryPendingRun(t, repo, workflowledger.RunSnapshot{
		RunID: "wfr-test", WorkflowName: "deliver-me", WorkflowDigest: "digest",
		ActiveStepID: "success", BaseRef: "main", BaseCommit: "deadbeef",
	})
	engine := &localengine.Engine{WorkspaceRoot: t.TempDir(), Repo: repo}

	res, err := engine.Deliver(context.Background(), run.RunID, true)
	if err != nil {
		t.Fatalf("Engine.Deliver: %v", err)
	}
	if !res.Refused || res.Status != string(workflowledger.RunStatusDeliveryFailed) {
		t.Fatalf("deliver result = %+v, want Refused=true status=delivery_failed", res)
	}
	fresh, err := repo.GetRun(context.Background(), run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != workflowledger.RunStatusDeliveryFailed {
		t.Fatalf("run status = %q, want delivery_failed", fresh.Status)
	}
}

func assertWorktreeHEAD(t *testing.T, root, want string) {
	t.Helper()
	if got := runGitOutT(t, root, "rev-parse", "HEAD"); got != want {
		t.Fatalf("recreated worktree HEAD = %q, want admitted base %q", got, want)
	}
}

// --- fixtures and helpers ---

// newLocalRepoNoOrigin builds a git repository with one commit on main and no
// origin remote, so delivery-active workflows cannot resolve a remote URL.
func newLocalRepoNoOrigin(t *testing.T) string {
	t.Helper()
	repoRoot := t.TempDir()
	runGitT(t, repoRoot, "init", "-b", "main")
	runGitT(t, repoRoot, "config", "user.email", "test@example.com")
	runGitT(t, repoRoot, "config", "user.name", "Test")
	writeFileT(t, filepath.Join(repoRoot, "a.txt"), "base\n")
	runGitT(t, repoRoot, "add", "a.txt")
	runGitT(t, repoRoot, "commit", "-m", "base")
	return repoRoot
}

// newRealDeliveryRepo builds a main repo with one base commit on main, a bare
// origin remote, and the base pushed to origin.
func newRealDeliveryRepo(t *testing.T) (repoRoot, originURL string) {
	t.Helper()
	repoRoot = t.TempDir()
	runGitT(t, repoRoot, "init", "-b", "main")
	runGitT(t, repoRoot, "config", "user.email", "test@example.com")
	runGitT(t, repoRoot, "config", "user.name", "Test")
	writeFileT(t, filepath.Join(repoRoot, "a.txt"), "base\n")
	runGitT(t, repoRoot, "add", "a.txt")
	runGitT(t, repoRoot, "commit", "-m", "base")
	originDir := filepath.Join(t.TempDir(), "origin.git")
	runGitT(t, filepath.Dir(originDir), "init", "--bare", filepath.Base(originDir))
	runGitT(t, repoRoot, "remote", "add", "origin", originDir)
	runGitT(t, repoRoot, "push", "-u", "origin", "main")
	return repoRoot, originDir
}

// newSeededDeliveryFixture builds the delivery-package-style real-git fixture
// for one delivery_pending run: main repo + origin, a run worktree at
// <main>/.mivia/worktrees/wt-test on branch wf/wt-test (the CLI layout that
// workflowspace.Resolve accepts), an uncommitted change, and a ledger run in
// delivery_pending state.
func newSeededDeliveryFixture(t *testing.T) (repoRoot, originURL string, run workflowledger.RunSnapshot, repo workflowledger.Repository) {
	t.Helper()
	repoRoot, originURL = newRealDeliveryRepo(t)
	baseCommit := runGitOutT(t, repoRoot, "rev-parse", "HEAD")

	worktreeRoot := filepath.Join(repoRoot, ".mivia", "worktrees", "wt-test")
	runGitT(t, repoRoot, "worktree", "add", "-b", "wf/wt-test", worktreeRoot, baseCommit)
	runGitT(t, worktreeRoot, "config", "user.email", "test@example.com")
	runGitT(t, worktreeRoot, "config", "user.name", "Test")
	// Uncommitted change so the intended diff is non-empty.
	writeFileT(t, filepath.Join(worktreeRoot, "a.txt"), "base\nchanged\n")

	repo = workflowledger.NewMemoryRepository()
	run = createDeliveryPendingRun(t, repo, workflowledger.RunSnapshot{
		RunID: "wfr-test", WorkflowName: "deliver-me", WorkflowDigest: "digest",
		ActiveStepID: "success", BaseRef: "main", BaseCommit: baseCommit,
		WorktreeName: "wt-test", RemoteURL: originURL,
	})
	return repoRoot, originURL, run, repo
}

// createDeliveryPendingRun admits a pending run and CASes it along the
// pending->running->delivery_pending chain. The snapshot carries the deliver-me
// definition so Engine.Deliver can rebuild the compiled delivery policy.
func createDeliveryPendingRun(t *testing.T, repo workflowledger.Repository, snap workflowledger.RunSnapshot) workflowledger.RunSnapshot {
	t.Helper()
	ctx := context.Background()
	raw, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{
		SchemaVersion:    workflowledger.SnapshotSchemaVersion,
		DefinitionTOML:   []byte(deliverMeTOML),
		DefinitionDigest: "digest",
		Inputs:           map[string]string{"task": "build"},
		Delivery:         &workflowledger.DeliverySnapshot{Mode: "draft", Provider: "github", Base: "main"},
	})
	if err != nil {
		t.Fatalf("MarshalSnapshot: %v", err)
	}
	snap.Status = workflowledger.RunStatusPending
	if err := repo.CreateRun(ctx, snap, raw); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	cur, err := repo.GetRun(ctx, snap.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	for _, next := range []workflowledger.RunStatus{workflowledger.RunStatusRunning, workflowledger.RunStatusDeliveryPending} {
		if cur.Status == workflowledger.RunStatusDeliveryPending {
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

// recordingPR records PR boundary calls; FindByHead returns no existing PR.
type recordingPR struct {
	mu      sync.Mutex
	created []delivery.PRInput
}

func (r *recordingPR) FindByHead(context.Context, string, string) (*delivery.PRRef, error) {
	return nil, nil
}

func (r *recordingPR) Create(_ context.Context, _ string, in delivery.PRInput) (delivery.PRRef, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.created = append(r.created, in)
	return delivery.PRRef{RemoteID: strconv.Itoa(len(r.created)), URL: "https://example.com/pull/" + strconv.Itoa(len(r.created))}, nil
}

func (r *recordingPR) createdPRs() []delivery.PRInput {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]delivery.PRInput(nil), r.created...)
}

func runGitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func runGitOutT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFileT(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
