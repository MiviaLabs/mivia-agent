package localengine

// coverage_gaps_test.go drives uncovered statement lines in engine.go,
// engine_cancel.go, engine_delete.go, engine_deliver.go, engine_resume.go
// and engine_stack.go: the engine guards, the admission and resume error
// branches, and the stack-drive settle internals, using direct in-package
// access plus a small failing-repo wrapper.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// --- shared local fixtures -------------------------------------------------

func gapsRunGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := append([]string{"-C", dir}, args...)
	out, err := exec.Command("git", cmd...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, string(out))
	}
}

func gapsInitRepo(t *testing.T, root string) {
	t.Helper()
	gapsRunGit(t, root, "init", "-q", "-b", "main")
	gapsRunGit(t, root, "config", "user.email", "test@example.com")
	gapsRunGit(t, root, "config", "user.name", "Test")
	gapsRunGit(t, root, "commit", "-q", "--allow-empty", "-m", "init")
}

func gapsWriteWorkflow(t *testing.T, root, name, body string) {
	t.Helper()
	dir := filepath.Join(root, ".mivia", "workflows")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

const gapsTwoStepTOML = `version = 1
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

func gapsTwoStepWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	gapsInitRepo(t, root)
	gapsWriteWorkflow(t, root, "two-step", gapsTwoStepTOML)
	return root
}

func gapsStaticEngine(t *testing.T, root string) *Engine {
	t.Helper()
	return &Engine{
		WorkspaceRoot: root,
		Repo:          workflowledger.NewMemoryRepository(),
		NewRunner: func() controller.AgentStepRunner {
			return &StaticStepRunner{}
		},
	}
}

func gapsStart(t *testing.T, e *Engine, req workflowledger.StartRequest) workflowledger.StartResult {
	t.Helper()
	res, err := e.Start(context.Background(), req)
	if err != nil {
		t.Fatalf("Start(%+v): %v", req, err)
	}
	if res.RunID == "" {
		t.Fatalf("Start(%+v) returned no run id", req)
	}
	return res
}

func gapsWaitStatus(t *testing.T, e *Engine, runID string, want workflowledger.RunStatus, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		run, err := e.Repo.GetRun(context.Background(), runID)
		if err != nil {
			t.Fatalf("GetRun: %v", err)
		}
		if run.Status == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s status = %q, want %q", runID, run.Status, want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// gapsRepo wraps a repository and fails or rewrites selected calls.
type gapsRepo struct {
	workflowledger.Repository
	mu               sync.Mutex
	getRunCalls      int
	failGetRunAt     int // 1-based call index that must fail; 0 disables
	failGetRunAfter  int // fail EVERY call after this 1-based index; 0 disables
	getRunErr        error
	notFoundOnce     bool
	foundSnapshot    *workflowledger.RunSnapshot
	snapshotErr      error
	deleteRunErr     error
	deliveryErr      error
	failListAttempts bool
	terminalFrom     int
	terminalSnapshot *workflowledger.RunSnapshot
}

func (r *gapsRepo) ListStepAttempts(ctx context.Context, runID string) ([]workflowledger.StepAttempt, error) {
	if r.failListAttempts {
		return nil, errors.New("list attempts refused by test")
	}
	return r.Repository.ListStepAttempts(ctx, runID)
}

func (r *gapsRepo) GetRun(ctx context.Context, runID string) (workflowledger.RunSnapshot, error) {
	r.mu.Lock()
	r.getRunCalls++
	index := r.getRunCalls
	failAt := r.failGetRunAt
	failAfter := r.failGetRunAfter
	r.mu.Unlock()
	if r.getRunErr != nil {
		return workflowledger.RunSnapshot{}, r.getRunErr
	}
	if failAt > 0 && index == failAt {
		return workflowledger.RunSnapshot{}, errors.New("get run refused by test")
	}
	// Fail-after semantics stay deterministic under concurrent background
	// GetRun noise (engine trace writers): an exact-index fault is consumed
	// silently by any racing reader, but a fail-after fault keeps failing
	// every later call, including the one the test targets.
	if failAfter > 0 && index > failAfter {
		return workflowledger.RunSnapshot{}, errors.New("get run refused by test")
	}
	if r.notFoundOnce && index == 1 {
		return workflowledger.RunSnapshot{}, workflowledger.ErrNotFound
	}
	if r.foundSnapshot != nil && index > 1 {
		return *r.foundSnapshot, nil
	}
	if r.terminalFrom > 0 && index >= r.terminalFrom {
		return *r.terminalSnapshot, nil
	}
	return r.Repository.GetRun(ctx, runID)
}

func (r *gapsRepo) GetRunSnapshot(ctx context.Context, runID string) ([]byte, error) {
	if r.snapshotErr != nil {
		return nil, r.snapshotErr
	}
	return r.Repository.GetRunSnapshot(ctx, runID)
}

func (r *gapsRepo) DeleteRun(ctx context.Context, runID string) error {
	if r.deleteRunErr != nil {
		return r.deleteRunErr
	}
	return r.Repository.DeleteRun(ctx, runID)
}

func (r *gapsRepo) GetDeliveryByIdempotencyKey(ctx context.Context, key string) (workflowledger.DeliveryRecord, error) {
	if r.deliveryErr != nil {
		return workflowledger.DeliveryRecord{}, r.deliveryErr
	}
	return r.Repository.GetDeliveryByIdempotencyKey(ctx, key)
}

// --- engine guards ----------------------------------------------------------

func TestGapEngineGuardsOnIncompleteEngine(t *testing.T) {
	var incomplete *Engine
	if _, err := incomplete.Start(context.Background(), workflowledger.StartRequest{}); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("Start on incomplete engine error = %v", err)
	}
	if _, err := incomplete.Cancel(context.Background(), "run"); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("Cancel on incomplete engine error = %v", err)
	}
	if _, err := incomplete.Delete(context.Background(), "run", false); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("Delete on incomplete engine error = %v", err)
	}
}

// --- Start branches ---------------------------------------------------------

func TestGapStartUnknownWorkflowFails(t *testing.T) {
	e := gapsStaticEngine(t, gapsTwoStepWorkspace(t))
	_, err := e.Start(context.Background(), workflowledger.StartRequest{Workflow: "missing", Inputs: map[string]any{"task": "x"}})
	if err == nil {
		t.Fatal("Start on an unknown workflow must fail")
	}
}

func TestGapStartInvocationKeyReusesTerminalRun(t *testing.T) {
	e := gapsStaticEngine(t, gapsTwoStepWorkspace(t))
	key := "gap-key-terminal"
	res := gapsStart(t, e, workflowledger.StartRequest{Workflow: "two-step", Inputs: map[string]any{"task": "x"}, InvocationKey: key})
	gapsWaitStatus(t, e, res.RunID, workflowledger.RunStatusSucceeded, 15*time.Second)
	again, err := e.Start(context.Background(), workflowledger.StartRequest{Workflow: "two-step", Inputs: map[string]any{"task": "x"}, InvocationKey: key})
	if err != nil {
		t.Fatalf("second Start with the same key: %v", err)
	}
	if again.RunID != res.RunID || again.Status != string(workflowledger.RunStatusSucceeded) {
		t.Fatalf("second Start = %+v, want the same succeeded run %+v", again, res)
	}
}

func TestGapStartInvocationGetRunErrorFails(t *testing.T) {
	root := gapsTwoStepWorkspace(t)
	inner := workflowledger.NewMemoryRepository()
	wrapped := &gapsRepo{Repository: inner, getRunErr: errors.New("ledger read refused by test")}
	e := &Engine{WorkspaceRoot: root, Repo: wrapped, NewRunner: func() controller.AgentStepRunner { return &StaticStepRunner{} }}
	_, err := e.Start(context.Background(), workflowledger.StartRequest{Workflow: "two-step", Inputs: map[string]any{"task": "x"}, InvocationKey: "gap-key-err"})
	if err == nil || !strings.Contains(err.Error(), "ledger read refused") {
		t.Fatalf("Start with a failing GetRun error = %v", err)
	}
}

func TestGapStartInvocationAdmissionWaitCanceled(t *testing.T) {
	root := gapsTwoStepWorkspace(t)
	e := &Engine{WorkspaceRoot: root, Repo: workflowledger.NewMemoryRepository(), NewRunner: func() controller.AgentStepRunner { return &StaticStepRunner{} }}
	key := "gap-key-canceled"
	runID := workflowledger.InvocationRunID(key)
	owner, release := e.beginInvocationAdmission(runID)
	if !owner {
		t.Fatal("the first admission must own the slot")
	}
	t.Cleanup(func() { e.finishInvocationAdmission(runID, release) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := e.Start(ctx, workflowledger.StartRequest{Workflow: "two-step", Inputs: map[string]any{"task": "x"}, InvocationKey: key})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Start with a canceled context while waiting error = %v", err)
	}
}

func TestGapStartInvocationAdmissionWaitSeesExistingRun(t *testing.T) {
	root := gapsTwoStepWorkspace(t)
	key := "gap-key-existing"
	runID := workflowledger.InvocationRunID(key)
	wrapped := &gapsRepo{
		Repository:    workflowledger.NewMemoryRepository(),
		notFoundOnce:  true,
		foundSnapshot: &workflowledger.RunSnapshot{RunID: runID, WorkflowName: "two-step", Status: workflowledger.RunStatusRunning},
	}
	e := &Engine{WorkspaceRoot: root, Repo: wrapped, NewRunner: func() controller.AgentStepRunner { return &StaticStepRunner{} }}
	owner, release := e.beginInvocationAdmission(runID)
	if !owner {
		t.Fatal("the first admission must own the slot")
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		e.finishInvocationAdmission(runID, release)
	}()
	res, err := e.Start(context.Background(), workflowledger.StartRequest{Workflow: "two-step", Inputs: map[string]any{"task": "x"}, InvocationKey: key})
	if err != nil {
		t.Fatalf("Start after the first admission released: %v", err)
	}
	if res.RunID != runID || res.Status != string(workflowledger.RunStatusRunning) {
		t.Fatalf("Start = %+v, want the admitted run %s running", res, runID)
	}
}

func TestGapStartInvocationAdmissionWaitGetRunError(t *testing.T) {
	root := gapsTwoStepWorkspace(t)
	key := "gap-key-admit-err"
	runID := workflowledger.InvocationRunID(key)
	wrapped := &gapsRepo{Repository: workflowledger.NewMemoryRepository()}
	wrapped.notFoundOnce = true
	wrapped.getRunErr = nil
	// The second GetRun (after the wait) must fail with a plain error.
	wrapped.failGetRunAt = 2
	e := &Engine{WorkspaceRoot: root, Repo: wrapped, NewRunner: func() controller.AgentStepRunner { return &StaticStepRunner{} }}
	owner, release := e.beginInvocationAdmission(runID)
	if !owner {
		t.Fatal("the first admission must own the slot")
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		e.finishInvocationAdmission(runID, release)
	}()
	_, err := e.Start(context.Background(), workflowledger.StartRequest{Workflow: "two-step", Inputs: map[string]any{"task": "x"}, InvocationKey: key})
	if err == nil || !strings.Contains(err.Error(), "did not admit") {
		t.Fatalf("Start with a failing re-read error = %v", err)
	}
}

// --- Cancel / Delete / Deliver branches --------------------------------------

// gapsDrainLaunch waits for the engine's launch goroutine to run its final
// trace write, so the goroutine never races t.TempDir's workspace removal
// ("directory not empty" flake). Wait covers a registered run; a run that
// errored before registration falls back to polling the durable trace file
// that the goroutine writes last.
func gapsDrainLaunch(t *testing.T, e *Engine, runID string) {
	t.Helper()
	waitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = e.Wait(waitCtx, runID)
	trace := filepath.Join(e.WorkspaceRoot, ".mivia", "runs", runID+".json")
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(trace); err == nil {
			return
		}
		e.mu.Lock()
		active := e.active[runID] != nil
		e.mu.Unlock()
		if !active {
			return
		}
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestGapStartFinalGetRunError(t *testing.T) {
	// A successful launch reads the fresh run once more for the response;
	// the second engine-internal GetRun is that final read.
	root := gapsTwoStepWorkspace(t)
	wrapped := &gapsRepo{Repository: workflowledger.NewMemoryRepository()}
	wrapped.failGetRunAt = 2
	e := &Engine{WorkspaceRoot: root, Repo: wrapped, NewRunner: func() controller.AgentStepRunner { return &StaticStepRunner{} }}
	_, err := e.Start(context.Background(), workflowledger.StartRequest{Workflow: "two-step", Inputs: map[string]any{"task": "x"}})
	if err == nil || !strings.Contains(err.Error(), "get run refused by test") {
		t.Fatalf("Start with a failing final GetRun error = %v", err)
	}
	// Drain the launched goroutine (its post-settle trace write) before the
	// test returns, or t.TempDir cleanup races the run JSON write.
	runs, listErr := wrapped.Repository.ListRuns(context.Background())
	if listErr != nil || len(runs) == 0 {
		t.Fatalf("ListRuns on the wrapped repo: %v (runs %d)", listErr, len(runs))
	}
	gapsDrainLaunch(t, e, runs[0].RunID)
}

func TestGapCancelSucceededRunReportsTerminalSuccess(t *testing.T) {
	e := gapsStaticEngine(t, gapsTwoStepWorkspace(t))
	res := gapsStart(t, e, workflowledger.StartRequest{Workflow: "two-step", Inputs: map[string]any{"task": "x"}})
	gapsWaitStatus(t, e, res.RunID, workflowledger.RunStatusSucceeded, 15*time.Second)
	out, err := e.Cancel(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("Cancel of a succeeded run: %v", err)
	}
	if out.Status != string(workflowledger.RunStatusSucceeded) {
		t.Fatalf("Cancel result = %+v, want the terminal status reported", out)
	}
}

func TestGapDeleteRunErrorPropagates(t *testing.T) {
	root := gapsTwoStepWorkspace(t)
	wrapped := &gapsRepo{Repository: workflowledger.NewMemoryRepository(), deleteRunErr: errors.New("delete refused by test")}
	e := &Engine{WorkspaceRoot: root, Repo: wrapped, NewRunner: func() controller.AgentStepRunner { return &StaticStepRunner{} }}
	inner := workflowledger.NewMemoryRepository()
	seeder := gapsStaticEngine(t, root)
	seeder.Repo = inner
	res := gapsStart(t, seeder, workflowledger.StartRequest{Workflow: "two-step", Inputs: map[string]any{"task": "x"}})
	gapsWaitStatus(t, seeder, res.RunID, workflowledger.RunStatusSucceeded, 15*time.Second)
	// Point the wrapped engine at the same underlying repository data by
	// reusing the seeder's repo inside the wrapper.
	wrapped.Repository = inner
	if _, err := e.Delete(context.Background(), res.RunID, false); err == nil || !strings.Contains(err.Error(), "delete refused") {
		t.Fatalf("Delete with a failing DeleteRun error = %v", err)
	}
}

func TestGapDeliverReplayRecordErrorPropagates(t *testing.T) {
	root := gapsTwoStepWorkspace(t)
	inner := workflowledger.NewMemoryRepository()
	wrapped := &gapsRepo{Repository: inner, deliveryErr: errors.New("delivery record refused by test")}
	e := &Engine{WorkspaceRoot: root, Repo: wrapped, NewRunner: func() controller.AgentStepRunner { return &StaticStepRunner{} }}
	seeder := gapsStaticEngine(t, root)
	seeder.Repo = inner
	res := gapsStart(t, seeder, workflowledger.StartRequest{Workflow: "two-step", Inputs: map[string]any{"task": "x"}})
	gapsWaitStatus(t, seeder, res.RunID, workflowledger.RunStatusSucceeded, 15*time.Second)
	_, err := e.Deliver(context.Background(), res.RunID, true)
	if err == nil || !strings.Contains(err.Error(), "replay delivery") {
		t.Fatalf("Deliver with a failing delivery-record read error = %v", err)
	}
}

// --- resume branches ---------------------------------------------------------

func gapsDeliveryWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	gapsInitRepo(t, root)
	originDir := filepath.Join(t.TempDir(), "origin.git")
	gapsRunGit(t, filepath.Dir(originDir), "init", "-q", "--bare", filepath.Base(originDir))
	gapsRunGit(t, root, "remote", "add", "origin", originDir)
	gapsRunGit(t, root, "push", "-u", "origin", "main")
	gapsWriteWorkflow(t, root, "deliver-me", `version = 1
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

[[transitions]]
from = "one"
to = "success"
[transitions.match]
status = "succeeded"
`)
	return root
}

func TestGapResumeDeliveryPendingIsRefused(t *testing.T) {
	e := gapsStaticEngine(t, gapsDeliveryWorkspace(t))
	res := gapsStart(t, e, workflowledger.StartRequest{Workflow: "deliver-me", Inputs: map[string]any{"task": "x"}})
	gapsWaitStatus(t, e, res.RunID, workflowledger.RunStatusDeliveryPending, 15*time.Second)
	_, err := e.Start(context.Background(), workflowledger.StartRequest{Resume: true, RunID: res.RunID})
	if err == nil || !strings.Contains(err.Error(), "waiting for delivery") {
		t.Fatalf("resume of a delivery_pending run error = %v", err)
	}
}

func TestGapResumeGetRunErrorPropagates(t *testing.T) {
	root := gapsTwoStepWorkspace(t)
	wrapped := &gapsRepo{Repository: workflowledger.NewMemoryRepository(), getRunErr: errors.New("resume read refused by test")}
	e := &Engine{WorkspaceRoot: root, Repo: wrapped, NewRunner: func() controller.AgentStepRunner { return &StaticStepRunner{} }}
	_, err := e.Start(context.Background(), workflowledger.StartRequest{Resume: true, RunID: "wfr-missing"})
	if err == nil || !strings.Contains(err.Error(), "resume read refused") {
		t.Fatalf("resume with a failing GetRun error = %v", err)
	}
}

// gapsWaitTerminal blocks until the run reaches any terminal status, so the
// engine's background goroutine has stopped writing into the workspace
// before t.TempDir removes it.
func gapsWaitTerminal(t *testing.T, e *Engine, runID string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		run, err := e.Repo.GetRun(context.Background(), runID)
		if err != nil {
			t.Fatalf("GetRun: %v", err)
		}
		if workflowledger.IsTerminalRunStatus(run.Status) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s never settled: %q", runID, run.Status)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func gapsBlockedEngine(t *testing.T) (*Engine, <-chan struct{}, workflowledger.StartResult) {
	t.Helper()
	root := gapsTwoStepWorkspace(t)
	block := make(chan struct{})
	e := &Engine{
		WorkspaceRoot: root,
		Repo:          workflowledger.NewMemoryRepository(),
		NewRunner: func() controller.AgentStepRunner {
			return &StaticStepRunner{BlockUntil: block}
		},
	}
	res := gapsStart(t, e, workflowledger.StartRequest{Workflow: "two-step", Inputs: map[string]any{"task": "x"}})
	gapsWaitStatus(t, e, res.RunID, workflowledger.RunStatusRunning, 15*time.Second)
	t.Cleanup(func() { close(block) })
	return e, block, res
}

func TestGapResumeSnapshotErrorPropagates(t *testing.T) {
	owner, _, res := gapsBlockedEngine(t)
	root := owner.WorkspaceRoot
	wrapped := &gapsRepo{Repository: owner.Repo, snapshotErr: errors.New("snapshot refused by test")}
	fresh := &Engine{WorkspaceRoot: root, Repo: wrapped, NewRunner: func() controller.AgentStepRunner { return &StaticStepRunner{} }}
	_, err := fresh.Start(context.Background(), workflowledger.StartRequest{Resume: true, RunID: res.RunID})
	if err == nil || !strings.Contains(err.Error(), "snapshot refused") {
		t.Fatalf("resume with a failing snapshot read error = %v", err)
	}
	// Settle the run so the engine's background goroutine stops writing
	// into the workspace before t.TempDir removes it.
	_, _ = owner.Cancel(context.Background(), res.RunID)
	gapsWaitTerminal(t, owner, res.RunID)
}

func TestGapResumeForeignClaimIsRefused(t *testing.T) {
	owner, _, res := gapsBlockedEngine(t)
	// Stop the owning controller so its claim is released, then hold a
	// fresh foreign claim with a live lease.
	if err := owner.Interrupt(res.RunID); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	if err := owner.Repo.ClaimRun(context.Background(), res.RunID, "foreign-host"); err != nil {
		t.Fatalf("foreign claim: %v", err)
	}
	fresh := &Engine{WorkspaceRoot: owner.WorkspaceRoot, Repo: owner.Repo, NewRunner: func() controller.AgentStepRunner { return &StaticStepRunner{} }}
	_, err := fresh.Start(context.Background(), workflowledger.StartRequest{Resume: true, RunID: res.RunID})
	if err == nil || !strings.Contains(err.Error(), "another host") {
		t.Fatalf("resume against a fresh foreign claim error = %v", err)
	}
	_ = owner.Repo.ReleaseRun(context.Background(), res.RunID, "foreign-host")
	_, _ = owner.Cancel(context.Background(), res.RunID)
	gapsWaitTerminal(t, owner, res.RunID)
}

func TestGapResumeGetRunAfterLaunchError(t *testing.T) {
	owner, _, res := gapsBlockedEngine(t)
	// Stop the owning controller so the resume owns the run exclusively.
	if err := owner.Interrupt(res.RunID); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	// Fail-after, not fail-at: the fresh engine's own launch goroutine races
	// its trace-writer GetRun against the main-goroutine read after the
	// launch, so an exact-index fault can be consumed by that noise. With
	// fail-after, the first engine-internal GetRun (the resume status read)
	// still succeeds and every later call - including the post-launch read
	// that builds the result - fails deterministically.
	wrapped := &gapsRepo{Repository: owner.Repo, failGetRunAfter: 1}
	fresh := &Engine{WorkspaceRoot: owner.WorkspaceRoot, Repo: wrapped, NewRunner: func() controller.AgentStepRunner { return &StaticStepRunner{} }}
	_, err := fresh.Start(context.Background(), workflowledger.StartRequest{Resume: true, RunID: res.RunID})
	if err == nil || !strings.Contains(err.Error(), "get run refused by test") {
		t.Fatalf("resume with a failing final GetRun error = %v", err)
	}
	// Drain the FRESH engine's launch goroutine too (its post-settle trace
	// write) before the test returns, or t.TempDir cleanup races the run
	// JSON write. Interrupt is best-effort: the resumed run may already have
	// settled itself (state conflict against the blocked owner run), in
	// which case its goroutine has already exited.
	_ = fresh.Interrupt(res.RunID)
	gapsDrainLaunch(t, fresh, res.RunID)
	_, _ = owner.Cancel(context.Background(), res.RunID)
	gapsWaitTerminal(t, owner, res.RunID)
}

func TestGapResumePostLaunchGetRunErrorPropagates(t *testing.T) {
	owner, _, res := gapsBlockedEngine(t)
	// Stop the owning controller so the resume owns the run exclusively.
	if err := owner.Interrupt(res.RunID); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	// Fail-after 2, not fail-after 1: on resume the wrapped repo sees the
	// resume's own status read (1) and the controller start's duplicate
	// admission re-read (2) before the launch. With fail-after 1 the fault
	// stops ctrl.Start instead of the post-launch read; with fail-after 2
	// the run launches and only the read that builds the StartResult fails.
	wrapped := &gapsRepo{Repository: owner.Repo, failGetRunAfter: 2}
	fresh := &Engine{WorkspaceRoot: owner.WorkspaceRoot, Repo: wrapped, NewRunner: func() controller.AgentStepRunner { return &StaticStepRunner{} }}
	_, err := fresh.Start(context.Background(), workflowledger.StartRequest{Resume: true, RunID: res.RunID})
	if err == nil || !strings.Contains(err.Error(), "get run refused by test") {
		t.Fatalf("resume with a failing post-launch GetRun error = %v", err)
	}
	// Drain the FRESH engine's launch goroutine before t.TempDir cleanup.
	_ = fresh.Interrupt(res.RunID)
	gapsDrainLaunch(t, fresh, res.RunID)
	_, _ = owner.Cancel(context.Background(), res.RunID)
	gapsWaitTerminal(t, owner, res.RunID)
}

// --- stack drive internals ---------------------------------------------------

type gapsMergedPR struct{}

func (gapsMergedPR) FindByHead(context.Context, string, string) (*delivery.PRRef, error) {
	ref := delivery.PRRef{RemoteID: "pr-1", URL: "https://example.invalid/pr/1"}
	return &ref, nil
}

func (gapsMergedPR) Create(context.Context, string, delivery.PRInput) (delivery.PRRef, error) {
	return delivery.PRRef{RemoteID: "pr-1", URL: "https://example.invalid/pr/1"}, nil
}

func (gapsMergedPR) IsMerged(context.Context, string, string) (bool, error) {
	return true, nil
}

// gapsSeedStackRun admits a fake chunk run with the given status and the
// stable admission key <stackID>:<chunkID>.
func gapsSeedStackRun(t *testing.T, repo workflowledger.Repository, stackID, chunkID string, status workflowledger.RunStatus, remoteURL string) {
	t.Helper()
	key := stackID + ":" + chunkID
	runID := workflowledger.InvocationRunID(key)
	snap := workflowledger.RunSnapshot{
		RunID: runID, WorkflowName: "two-step", InvocationKey: key,
		Status: workflowledger.RunStatusPending, RemoteURL: remoteURL, WorktreeName: "workflow-" + runID,
	}
	if err := repo.CreateRun(context.Background(), snap, []byte("{}")); err != nil {
		t.Fatalf("CreateRun(%s): %v", chunkID, err)
	}
	created, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := repo.CompareAndSetRunStatus(context.Background(), runID, created.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatalf("CAS %s to running: %v", chunkID, err)
	}
	running, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(context.Background(), runID, running.Version, status, &now); err != nil {
		t.Fatalf("CAS %s to %s: %v", chunkID, status, err)
	}
}

func gapsSeedLedger(t *testing.T, chunks ...string) *workflowledger.Store {
	t.Helper()
	ledgerStore := workflowledger.NewStore(storage.NewMemory())
	plans := make([]delivery.ChunkPlan, 0, len(chunks))
	for _, id := range chunks {
		plans = append(plans, delivery.ChunkPlan{ID: id, Title: id, Files: []string{id + ".go"}})
	}
	if err := delivery.SeedStackLedger(context.Background(), ledgerStore, "stack-gap", plans); err != nil {
		t.Fatalf("SeedStackLedger: %v", err)
	}
	for _, id := range chunks {
		if err := ledgerStore.TransitionTask("stack-gap", id, delivery.StatusRunning); err != nil {
			t.Fatalf("seed %s running: %v", id, err)
		}
	}
	return ledgerStore
}

func TestGapProcessSettledChunksClassifiesOutcomes(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	ledgerStore := gapsSeedLedger(t, "c1", "c2", "c3")
	gapsSeedStackRun(t, repo, "stack-gap", "c1", workflowledger.RunStatusSucceeded, "")
	if err := repo.UpsertDelivery(context.Background(), workflowledger.DeliveryRecord{RunID: workflowledger.InvocationRunID("stack-gap:c1"), IdempotencyKey: "d1", Status: "no_diff"}); err != nil {
		t.Fatal(err)
	}
	gapsSeedStackRun(t, repo, "stack-gap", "c2", workflowledger.RunStatusSucceeded, "")
	if err := repo.UpsertDelivery(context.Background(), workflowledger.DeliveryRecord{RunID: workflowledger.InvocationRunID("stack-gap:c2"), IdempotencyKey: "d2", Status: "pushed", CommitSHA: "abc123"}); err != nil {
		t.Fatal(err)
	}
	gapsSeedStackRun(t, repo, "stack-gap", "c3", workflowledger.RunStatusSucceeded, "")

	e := &Engine{Repo: repo}
	byID, err := delivery.TaskMap(context.Background(), ledgerStore, "stack-gap")
	if err != nil {
		t.Fatal(err)
	}
	if !e.processSettledChunks(context.Background(), ledgerStore, "stack-gap", byID, false) {
		t.Fatal("processSettledChunks reported no progress")
	}
	after, err := delivery.TaskMap(context.Background(), ledgerStore, "stack-gap")
	if err != nil {
		t.Fatal(err)
	}
	if got := after["c1"].Status; got != delivery.StatusMerged {
		t.Fatalf("no_diff chunk status = %q, want merged", got)
	}
	if got := after["c2"].Status; got != delivery.StatusPublished {
		t.Fatalf("pushed chunk status = %q, want published", got)
	}
	if got := after["c3"].Status; got != delivery.StatusImplemented {
		t.Fatalf("plain succeeded chunk status = %q, want implemented", got)
	}
}

func TestGapMarkMergedChunksWithMergedPROracle(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	ledgerStore := gapsSeedLedger(t, "c4")
	if err := ledgerStore.TransitionTask("stack-gap", "c4", delivery.StatusPublished); err != nil {
		t.Fatal(err)
	}
	gapsSeedStackRun(t, repo, "stack-gap", "c4", workflowledger.RunStatusSucceeded, "https://github.com/acme/widgets")
	e := &Engine{Repo: repo, PR: gapsMergedPR{}}
	byID, err := delivery.TaskMap(context.Background(), ledgerStore, "stack-gap")
	if err != nil {
		t.Fatal(err)
	}
	if !e.markMergedChunks(context.Background(), ledgerStore, "stack-gap", byID) {
		t.Fatal("markMergedChunks reported no progress with a merged PR oracle")
	}
	after, err := delivery.TaskMap(context.Background(), ledgerStore, "stack-gap")
	if err != nil {
		t.Fatal(err)
	}
	if got := after["c4"].Status; got != delivery.StatusMerged {
		t.Fatalf("published chunk status = %q, want merged", got)
	}
}

func TestGapReopenOrFailStopsAtMaxAttempts(t *testing.T) {
	ledgerStore := gapsSeedLedger(t, "c5")
	e := &Engine{}
	for i := 0; i < delivery.MaxChunkAttempts; i++ {
		if !e.reopenOrFailStackTask(context.Background(), ledgerStore, "stack-gap", "c5") {
			t.Fatalf("reopen %d reported no progress", i+1)
		}
		if err := ledgerStore.TransitionTask("stack-gap", "c5", delivery.StatusRunning); err != nil {
			t.Fatalf("reset c5 running after reopen %d: %v", i+1, err)
		}
	}
	// The budget is spent: the next failure must mark the chunk failed.
	if !e.reopenOrFailStackTask(context.Background(), ledgerStore, "stack-gap", "c5") {
		t.Fatal("final reopen-or-fail reported no progress")
	}
	task, err := ledgerStore.GetTask("stack-gap", "c5")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != delivery.StatusFailed {
		t.Fatalf("exhausted chunk status = %q, want failed", task.Status)
	}
}

func TestGapAdmitWaveReopensChunkWhenStartFails(t *testing.T) {
	// An engine whose workspace has no workflows fails every chunk Start,
	// so admitWave must roll the claimed task back to reopened.
	repo := workflowledger.NewMemoryRepository()
	ledgerStore := gapsSeedLedger(t, "c6")
	// admitWave only CASes a chunk out of an admissible-pre status.
	if err := ledgerStore.TransitionTask("stack-gap", "c6", delivery.StatusPlanned); err != nil {
		t.Fatal(err)
	}
	e := &Engine{WorkspaceRoot: t.TempDir(), Repo: repo, NewRunner: func() controller.AgentStepRunner { return &StaticStepRunner{} }}
	planRun := workflowledger.RunSnapshot{RunID: "wfr-plan-gap", WorkflowName: "two-step"}
	chunks := []delivery.ChunkPlan{{ID: "c6", Title: "c6", Files: []string{"c6.go"}}}
	plans := map[string]*delivery.ChunkPlan{"c6": &chunks[0]}
	e.admitWave(context.Background(), planRun, ledgerStore, "stack-gap", plans, []string{"c6"}, []string{"c6"}, nil, "main", false)
	task, err := ledgerStore.GetTask("stack-gap", "c6")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != delivery.StatusReopened {
		t.Fatalf("chunk status after a failed admission = %q, want reopened", task.Status)
	}
}

func TestGapCancelDeliveryPendingRunFails(t *testing.T) {
	// CancelRun refuses a delivery_pending run; the run is not terminal, so
	// Cancel must surface the refusal error.
	e := gapsStaticEngine(t, gapsDeliveryWorkspace(t))
	res := gapsStart(t, e, workflowledger.StartRequest{Workflow: "deliver-me", Inputs: map[string]any{"task": "x"}})
	gapsWaitStatus(t, e, res.RunID, workflowledger.RunStatusDeliveryPending, 15*time.Second)
	if _, err := e.Cancel(context.Background(), res.RunID); err == nil || !strings.Contains(err.Error(), "waiting for delivery") {
		t.Fatalf("cancel of a delivery_pending run error = %v", err)
	}
}

func TestGapCancelReportsTerminalWhenSettledDuringCancel(t *testing.T) {
	// CancelRun fails while listing attempts, but the run reads back
	// terminal on the engine's reconciliation read, so Cancel must report
	// the settled status instead of the stale error.
	owner, _, res := gapsBlockedEngine(t)
	realRepo := owner.Repo
	if err := owner.Interrupt(res.RunID); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	terminal := workflowledger.RunSnapshot{RunID: res.RunID, WorkflowName: "two-step", Status: workflowledger.RunStatusCanceled}
	wrapped := &gapsRepo{Repository: owner.Repo, failListAttempts: true, terminalFrom: 2, terminalSnapshot: &terminal}
	fresh := &Engine{WorkspaceRoot: owner.WorkspaceRoot, Repo: wrapped, NewRunner: func() controller.AgentStepRunner { return &StaticStepRunner{} }}
	out, err := fresh.Cancel(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("cancel of a run that settled terminal error = %v", err)
	}
	if out.Status != string(workflowledger.RunStatusCanceled) {
		t.Fatalf("cancel result = %+v, want the terminal status reported", out)
	}
	// Settle through the real repository so background work stops before
	// t.TempDir removes the workspace.
	settler := &Engine{WorkspaceRoot: owner.WorkspaceRoot, Repo: realRepo, NewRunner: func() controller.AgentStepRunner { return &StaticStepRunner{} }}
	_, _ = settler.Cancel(context.Background(), res.RunID)
	gapsWaitTerminal(t, settler, res.RunID)
}

func TestGapCancelFinalGetRunError(t *testing.T) {
	// A successful cancel reads the run once more for the response; the
	// third engine-internal GetRun is that final read.
	owner, _, res := gapsBlockedEngine(t)
	wrapped := &gapsRepo{Repository: owner.Repo, failGetRunAt: 3}
	owner.Repo = wrapped
	_, err := owner.Cancel(context.Background(), res.RunID)
	if err == nil || !strings.Contains(err.Error(), "get run refused by test") {
		t.Fatalf("cancel with a failing final GetRun error = %v", err)
	}
	gapsWaitTerminal(t, owner, res.RunID)
}

func TestGapDriveStackLoopRereadsAfterMergeProgress(t *testing.T) {
	// One chunk whose run succeeded with pushed evidence and a merged PR
	// oracle: the loop's markMergedChunks pass progresses, forcing the
	// durable-state re-read (the branch after it), then the stack completes
	// and finishStack runs against an engine whose workspace cannot admit
	// the integration run, which it logs and returns.
	repo := workflowledger.NewMemoryRepository()
	ledgerStore := gapsSeedLedger(t, "c7")
	gapsSeedStackRun(t, repo, "stack-gap", "c7", workflowledger.RunStatusSucceeded, "https://github.com/acme/widgets")
	if err := repo.UpsertDelivery(context.Background(), workflowledger.DeliveryRecord{RunID: workflowledger.InvocationRunID("stack-gap:c7"), IdempotencyKey: "d7", Status: "pushed", CommitSHA: "def456"}); err != nil {
		t.Fatal(err)
	}
	e := &Engine{WorkspaceRoot: t.TempDir(), Repo: repo, PR: gapsMergedPR{}}
	compiled := &definition.CompiledWorkflow{Stacking: &definition.StackingConfig{Enabled: true, PlanStep: "plan", ImplementStep: "implement", MergePolicy: "auto"}}
	planRun := workflowledger.RunSnapshot{RunID: "wfr-plan-loop", WorkflowName: "two-step"}
	chunks := []delivery.ChunkPlan{{ID: "c7", Title: "c7", Files: []string{"c7.go"}}}
	e.driveStackLoop(context.Background(), planRun, compiled, ledgerStore, "stack-gap", chunks, []string{"c7"}, nil, "main")
	after, err := delivery.TaskMap(context.Background(), ledgerStore, "stack-gap")
	if err != nil {
		t.Fatal(err)
	}
	if got := after["c7"].Status; got != delivery.StatusMerged {
		t.Fatalf("chunk status = %q, want merged by the merged-PR oracle", got)
	}
}

func TestGapStartNewReturnsExistingRunReadError(t *testing.T) {
	// A second start that resolves to an existing run id (deterministic
	// NewRunID) reaches StartNew's idempotent re-entry; the engine's read
	// of the existing run then fails and must surface the error.
	root := gapsTwoStepWorkspace(t)
	inner := workflowledger.NewMemoryRepository()
	first := &Engine{WorkspaceRoot: root, Repo: inner, NewRunner: func() controller.AgentStepRunner { return &StaticStepRunner{} }}
	res := gapsStart(t, first, workflowledger.StartRequest{Workflow: "two-step", Inputs: map[string]any{"task": "x"}})
	gapsWaitStatus(t, first, res.RunID, workflowledger.RunStatusSucceeded, 15*time.Second)
	wrapped := &gapsRepo{Repository: inner, failGetRunAt: 2}
	second := &Engine{WorkspaceRoot: root, Repo: wrapped, NewRunID: func() string { return res.RunID }, NewRunner: func() controller.AgentStepRunner { return &StaticStepRunner{} }}
	_, err := second.Start(context.Background(), workflowledger.StartRequest{Workflow: "two-step", Inputs: map[string]any{"task": "x"}})
	if err == nil || !strings.Contains(err.Error(), "get run refused by test") {
		t.Fatalf("idempotent re-entry with a failing run read error = %v", err)
	}
}
