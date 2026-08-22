package clichat

// Coverage-gap tests for deep `stack drive` branches that the main fixtures
// do not reach on their own: the fresh-admission succeeded settle, the
// decompose-continuation run-failure settle, the post-admission
// max_total_chunks halt, the deliver_plan_run=false plan-run settle, and
// reconcileStack's concurrent-writer conflict skip.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/cliworkflow"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// coverPrepare re-prepares the mini-stack workflow from the fixture root, so
// a test that rewrote the workflow TOML compiles the variant it wrote.
func coverPrepare(t *testing.T, it *stackDriveIT) *cliworkflow.PreparedWorkflowRun {
	t.Helper()
	prepared, err := cliworkflow.PrepareWorkflowRun("mini-stack", it.root, it.configPath, []string{"task=x"})
	if err != nil {
		t.Fatalf("cliworkflow.PrepareWorkflowRun() error = %v", err)
	}
	t.Cleanup(prepared.CloseFn)
	return prepared
}

// TestDriveChunkSettlesRunSucceededDirectly pins driveChunk's
// RunStatusSucceeded case (stack_admit.go): a stacking workflow with NO
// delivery policy settles its chunk runs at succeeded directly, and the
// fresh-admission settle must mark the task implemented without halting.
func TestDriveChunkSettlesRunSucceededDirectly(t *testing.T) {
	it := newStackDriveIT(t, "", multiChunkPlanOutput)
	// Rewrite the workflow without the [delivery] block: with no delivery
	// policy the chunk run's success terminal is RunStatusSucceeded, the
	// exact status the driveChunk switch's succeeded case exists for.
	path := filepath.Join(it.root, ".mivia", "workflows", "mini-stack.toml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	idx := bytes.Index(raw, []byte("\n[delivery]"))
	if idx < 0 {
		t.Fatal("mini-stack fixture has no [delivery] block to strip")
	}
	if err := os.WriteFile(path, append(raw[:idx:idx], '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	prepared := coverPrepare(t, it)

	_, chunks, _, _, err := parseStackPlanOutput([]byte(multiChunkPlanOutput))
	if err != nil {
		t.Fatal(err)
	}
	ledger := workflowledger.NewStore(prepared.Store)
	const stackID = "wfr-cover-chunk-succ"
	if err := seedStackLedger(ledger, stackID, chunks); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	halt, err := driveChunk(context.Background(), prepared, ledger, stackID, "c1", &chunks[0], chunkPlanIndex(chunks), "main", "1/2", "approve", false, map[string]string{"task": "x"}, &out, io.Discard)
	if err != nil {
		t.Fatalf("driveChunk() error = %v", err)
	}
	if halt {
		t.Fatalf("driveChunk() halt = true on a succeeded run; output: %s", out.String())
	}
	byID, err := stackTaskMap(ledger, stackID)
	if err != nil {
		t.Fatal(err)
	}
	if got := byID["c1"].Status; got != stackStatusImplemented {
		t.Fatalf("task c1 status = %q, want implemented after the succeeded settle", got)
	}
}

// decomposeRunReadFailsRepo lets run creation succeed and then fails every
// GetRun for the created run, so the controller's Run read after Start
// returns an error: the injectable failure admitDecomposeContinuationRun's
// run-failure settle branch needs.
type decomposeRunReadFailsRepo struct {
	workflowledger.Repository
	mu      sync.Mutex
	created map[string]bool
}

func (r *decomposeRunReadFailsRepo) CreateRun(ctx context.Context, snap workflowledger.RunSnapshot, raw []byte) error {
	if err := r.Repository.CreateRun(ctx, snap, raw); err != nil {
		return err
	}
	r.mu.Lock()
	if r.created == nil {
		r.created = make(map[string]bool)
	}
	r.created[snap.RunID] = true
	r.mu.Unlock()
	return nil
}

func (r *decomposeRunReadFailsRepo) GetRun(ctx context.Context, runID string) (workflowledger.RunSnapshot, error) {
	r.mu.Lock()
	created := r.created[runID]
	r.mu.Unlock()
	if created {
		return workflowledger.RunSnapshot{}, errors.New("run read refused by coverage test")
	}
	return r.Repository.GetRun(ctx, runID)
}

// TestAdmitDecomposeContinuationRunFailureSettles pins the controller Run
// error branch of admitDecomposeContinuationRun: a wave run whose Run read
// fails after a successful Start must surface the wave error.
func TestAdmitDecomposeContinuationRunFailureSettles(t *testing.T) {
	it := newStackDriveIT(t, "", multiChunkPlanOutput)
	prevBuild := cliworkflow.WorkflowRunBuild
	t.Cleanup(func() { cliworkflow.WorkflowRunBuild = prevBuild })
	cliworkflow.WorkflowRunBuild = func(buildRoot string, res *config.Resolved, store *storage.SQLite, repo workflowledger.Repository, compiled *definition.CompiledWorkflow, refBase string, inputs map[string]any, inputSnapshot map[string]string, raw []byte, id string, snap *workflowledger.Snapshot, rawDef []byte, run *workflowledger.RunSnapshot, m map[string]bool, reg *skills.Registry) (cliworkflow.WorkflowControllerBuild, error) {
		if inputSnapshot["stack_mode"] == "decompose_continue" {
			repo = &decomposeRunReadFailsRepo{Repository: repo}
		}
		return prevBuild(buildRoot, res, store, repo, compiled, refBase, inputs, inputSnapshot, raw, id, snap, rawDef, run, m, reg)
	}
	prepared := coverPrepare(t, it)

	var out bytes.Buffer
	_, _, _, err := admitDecomposeContinuationRun(prepared, "wfr-cover-wave-fail", 1, "more scope", map[string]string{"task": "x"}, &out, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "decompose continuation wave 1 failed") {
		t.Fatalf("admitDecomposeContinuationRun() error = %v, want the wave failure", err)
	}
}

// TestAdmitNextWaveIfReadyHaltsWhenWaveExceedsCap pins the post-admission
// max_total_chunks check in admitNextWaveIfReady: two merged chunks under a
// cap of three, plus a continuation wave that alone adds two more chunks,
// must halt the drive instead of seeding the overflow.
func TestAdmitNextWaveIfReadyHaltsWhenWaveExceedsCap(t *testing.T) {
	it := newStackDriveIT(t, "", multiChunkPlanOutput)
	path := filepath.Join(it.root, ".mivia", "workflows", "mini-stack.toml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	capped := strings.Replace(string(raw), `merge_policy = "auto"`, `merge_policy = "auto"`+"\n"+`max_total_chunks = 3`, 1)
	if capped == string(raw) {
		t.Fatal("failed to inject max_total_chunks into the stacking block")
	}
	if err := os.WriteFile(path, []byte(capped), 0o600); err != nil {
		t.Fatal(err)
	}
	prepared := coverPrepare(t, it)

	_, chunks, _, _, err := parseStackPlanOutput([]byte(multiChunkPlanOutput))
	if err != nil {
		t.Fatal(err)
	}
	ledger := workflowledger.NewStore(prepared.Store)
	const stackID = "wfr-cover-cap"
	if err := seedStackLedger(ledger, stackID, chunks); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"c1", "c2"} {
		if err := ledger.TransitionTask(stackID, id, stackStatusMerged); err != nil {
			t.Fatal(err)
		}
	}

	var out bytes.Buffer
	err = admitNextWaveIfReady(prepared, ledger, stackID, chunks, true, "the rest of the task", map[string]string{"task": "x"}, &out, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "exceeding max_total_chunks=3") {
		t.Fatalf("admitNextWaveIfReady() error = %v, want the cap overflow halt", err)
	}
	// The overflow wave's chunks must NOT have been seeded.
	byID, err := stackTaskMap(ledger, stackID)
	if err != nil {
		t.Fatal(err)
	}
	if _, seeded := byID["c3"]; seeded {
		t.Fatal("overflow wave chunk c3 was seeded despite the cap halt")
	}
}

// TestSettleStackPlanRunSkippedDeliveryWhenComplete pins
// settleStackPlanRunIfComplete's deliver_plan_run=false branch: a complete
// stack's parked plan run must settle succeeded with no plan PR. The gate is
// forced through the existing classifyStackPlanRunDeliveryFn seam because
// driving a real merged stack here would only retest the classify logic its
// own tests already pin.
func TestSettleStackPlanRunSkippedDeliveryWhenComplete(t *testing.T) {
	it := newStackDriveIT(t, "", multiChunkPlanOutput)
	prepared := coverPrepare(t, it)

	runID := cliworkflow.NewCLIWorkflowRunID()
	built, err := cliworkflow.WorkflowRunBuild(prepared.Root, prepared.Res, prepared.Store, prepared.Repo, prepared.Compiled, prepared.RefBase, prepared.Inputs, prepared.InputSnapshot, prepared.Raw, runID, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("WorkflowRunBuild() error = %v", err)
	}
	t.Cleanup(built.Cleanup)
	finish, err := cliworkflow.BeginWorkflowExecution(prepared.Root, ContextStorePath(prepared.Root, prepared.Res.Subagents), runID)
	if err != nil {
		t.Fatalf("BeginWorkflowExecution() error = %v", err)
	}
	if err := cliworkflow.WorkflowRunSetAdmission(built); err != nil {
		t.Fatalf("WorkflowRunSetAdmission() error = %v", err)
	}
	if created, err := built.Controller.StartNew(context.Background()); err != nil || !created {
		t.Fatalf("StartNew() created=%v error=%v", created, err)
	}
	finish()
	for _, next := range []workflowledger.RunStatus{workflowledger.RunStatusRunning, workflowledger.RunStatusDeliveryPending} {
		stored, err := prepared.Repo.GetRun(context.Background(), runID)
		if err != nil {
			t.Fatal(err)
		}
		if err := prepared.Repo.CompareAndSetRunStatus(context.Background(), runID, stored.Version, next, nil); err != nil {
			t.Fatal(err)
		}
	}
	// The plan ledger row SkipParkedPlanRunPublication requires.
	ledger := workflowledger.NewStore(prepared.Store)
	if _, err := ledger.StorePlan(workflowledger.Plan{ID: runID, Scope: stackScope(runID), Schema: delivery.PlanSchema}); err != nil {
		t.Fatal(err)
	}

	prevGate := classifyStackPlanRunDeliveryFn
	classifyStackPlanRunDeliveryFn = func(ctx context.Context, root string, store *storage.SQLite, repo workflowledger.Repository, runID string, remoteMergeOracle bool) stackPlanRunGate {
		return stackPlanRunComplete
	}
	t.Cleanup(func() { classifyStackPlanRunDeliveryFn = prevGate })

	var out bytes.Buffer
	if err := settleStackPlanRunIfComplete(context.Background(), prepared, runID, &out); err != nil {
		t.Fatalf("settleStackPlanRunIfComplete() error = %v", err)
	}
	if !strings.Contains(out.String(), "delivery.deliver_plan_run=false") {
		t.Fatalf("settle output = %q, want the skipped-delivery settle message", out.String())
	}
	run, err := prepared.Repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("plan run status = %q, want succeeded after the skipped-delivery settle", run.Status)
	}
}

// TestReconcileStackSkipsTaskOnConcurrentWriterConflict pins the
// ErrTaskConflict branch in reconcileStack through the applyReconcileActionFn
// seam: a conflict on one task's transition must skip that task, not abort
// the whole reconcile pass.
func TestReconcileStackSkipsTaskOnConcurrentWriterConflict(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	ledger := workflowledger.NewMemoryStore()

	const stackID = "stack-cover-conflict"
	if _, err := ledger.StorePlan(workflowledger.Plan{ID: stackID, Scope: stackScope(stackID)}); err != nil {
		t.Fatal(err)
	}
	// A running task with no run row reconciles to a reopen transition, so
	// the apply call has a real status change to conflict on; a second task
	// stays planned so the pass must continue past the skipped one.
	if err := ledger.CreateTask(workflowledger.Task{ID: "c1", PlanRef: stackID, Scope: stackScope(stackID), Status: stackStatusRunning}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.CreateTask(workflowledger.Task{ID: "c2", PlanRef: stackID, Scope: stackScope(stackID), Status: stackStatusPlanned}); err != nil {
		t.Fatal(err)
	}

	prevApply := applyReconcileActionFn
	applyReconcileActionFn = func(ledger *workflowledger.Store, stackID string, act ReconcileAction) error {
		if act.NewStatus == "" || act.NewStatus == act.CurrentStatus {
			return nil
		}
		return workflowledger.ErrTaskConflict
	}
	t.Cleanup(func() { applyReconcileActionFn = prevApply })

	actions, err := reconcileStack(context.Background(), ledger, repo, neverMergedChecker{}, stackID, stackMaxChunkAttempts)
	if err != nil {
		t.Fatalf("reconcileStack() error = %v; want nil with the conflicting task skipped", err)
	}
	if len(actions) != 2 {
		t.Fatalf("reconcileStack() actions = %d, want 2 (the conflict must not abort the pass)", len(actions))
	}
	// Both tasks keep their pre-reconcile statuses: every transition lost to
	// the forced conflict.
	byID, err := stackTaskMap(ledger, stackID)
	if err != nil {
		t.Fatal(err)
	}
	if got := byID["c1"].Status; got != stackStatusRunning {
		t.Fatalf("task c1 status = %q, want unchanged running after the conflict skip", got)
	}
}
