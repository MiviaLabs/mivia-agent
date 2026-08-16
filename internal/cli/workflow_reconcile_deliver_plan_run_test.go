package cli

import (
	"context"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/tasks"
)

// miniStackDeliverPlanRunWorkflowTOML is miniStackWorkflowTOML with
// deliver_plan_run = true: the plan run's own PR is published after the
// stack drives to completion. The non-default setting is gated (no
// checked-in workflow uses it) and exercises the F10 guard: the recovery
// sweep must drive the undriven stack BEFORE delivering the plan run.
const miniStackDeliverPlanRunWorkflowTOML = `version = 1
name = "mini-stack"
description = "Minimal stacking + delivery workflow for the drive-ordering regression."
initial_step = "plan"

[inputs.task]
type = "string"
required = true

[[steps]]
id = "plan"
kind = "agent"
agent = "one"

[[steps]]
id = "implement"
kind = "agent"
agent = "two"

[[transitions]]
from = "plan"
to = "implement"
match = { status = "succeeded" }

[[transitions]]
from = "implement"
to = "success"
match = { status = "succeeded" }

[stacking]
plan_step = "plan"
implement_step = "implement"
merge_policy = "auto"

[delivery]
kind = "pull_request"
mode = "draft"
provider = "github"
base = "main"
title_template = "feat: {{ inputs.task }}"
deliver_plan_run = true
`

// seedParkedStackingPlanRunWithDeliverPlanRun seeds a delivery_pending
// stacking plan run with deliver_plan_run = true and a seeded-but-incomplete
// two-chunk stack ledger — the F10 prerequisite shape.
func seedParkedStackingPlanRunWithDeliverPlanRun(t *testing.T, root, storePath string, repo workflowledger.Repository) string {
	t.Helper()
	return seedParkedStackingPlanRunDeliverPlanRunTOML(t, root, storePath, repo, miniStackDeliverPlanRunWorkflowTOML, "auto")
}

// seedParkedStackingPlanRunDeliverPlanRunTOML is seedParkedStackingPlanRunTOML
// for workflows with deliver_plan_run = true: it asserts DeliverPlanRun is
// true instead of asserting it is false.
func seedParkedStackingPlanRunDeliverPlanRunTOML(t *testing.T, root, storePath string, repo workflowledger.Repository, rawTOML, wantMergePolicy string) string {
	t.Helper()
	rawDefinition := []byte(rawTOML)
	wf, _, err := definition.ParseWorkflowTOML(rawDefinition, "mini-stack.toml")
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile(&wf)
	if err != nil {
		t.Fatal(err)
	}
	if compiled.Delivery == nil || !compiled.Delivery.DeliverPlanRun {
		t.Fatal("deliver_plan_run=true workflow must set DeliverPlanRun")
	}
	if compiled.Stacking == nil {
		t.Fatal("mini-stack workflow must resolve a stacking config")
	}
	if compiled.Stacking.MergePolicy != wantMergePolicy {
		t.Fatalf("mini-stack workflow merge_policy = %q, want %q", compiled.Stacking.MergePolicy, wantMergePolicy)
	}
	snapshot := miniStackSnapshot(t, root, compiled, rawDefinition)
	rawSnapshot, err := workflowledger.MarshalSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	runID := "wfr-parked-plan-dpr"
	run := workflowledger.RunSnapshot{
		RunID: runID, WorkflowName: compiled.Name, WorkflowDigest: compiled.Digest,
		SnapshotDigest: workflowledger.SnapshotDigest(rawSnapshot),
		InputDigest:    workflowledger.InputDigest(snapshot.Inputs),
		Status:         workflowledger.RunStatusPending, ActiveStepID: compiled.InitialStep,
	}
	if err := repo.CreateRun(ctx, run, rawSnapshot); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	stored, err = repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, workflowledger.RunStatusDeliveryPending, nil); err != nil {
		t.Fatal(err)
	}
	_, chunks, _, _, err := parseStackPlanOutput([]byte(multiChunkPlanOutput))
	if err != nil || len(chunks) != 2 {
		t.Fatalf("parse multi-chunk plan = %v, %v; want 2 chunks", chunks, err)
	}
	store, err := openContextStorePath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := seedStackLedger(tasks.NewStore(store), runID, chunks); err != nil {
		t.Fatal(err)
	}
	return runID
}

// TestReconcileParkedDeliveryLeavesUndrivenStackParkedWithDeliverPlanRun
// proves the F10 fix: when deliver_plan_run = true, the recovery sweep does
// NOT deliver the plan run over an undriven multi-chunk stack. Without the
// drive-before-delivery guard, skipParkedPlanRunPublication returns false,
// reconcileParkedDelivery falls to deliverRunWithStore, and the plan run
// settles succeeded (no_diff) with zero chunks driven — the deliver-before-
// drive bug. The guard detects the undriven stack and leaves the run parked.
func TestReconcileParkedDeliveryLeavesUndrivenStackParkedWithDeliverPlanRun(t *testing.T) {
	root, storePath, configPath, prRecorder := newDeliveryFixture(t)
	repo := openDeliveryStore(t, storePath)
	planRunID := seedParkedStackingPlanRunWithDeliverPlanRun(t, root, storePath, repo)
	// stackDecomposedChunks (the F10 guard predicate) needs a succeeded
	// decompose step attempt; the seeding helper only writes the task ledger.
	seedSucceededDecomposeAttempt(t, repo, planRunID, []byte(multiChunkPlanOutput))

	e := newSessionWorkflowEngine(root, configPath)
	e.reconcileParkedRuns(context.Background(), false)

	ctx := context.Background()
	run, err := repo.GetRun(ctx, planRunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("plan run status = %q, want delivery_pending (undriven stack must stay parked even with deliver_plan_run=true)", run.Status)
	}
	if creates, finds := prRecorder.calls(); creates != 0 || finds != 0 {
		t.Fatalf("PR client calls: creates=%d finds=%d, want zero (deliverRunWithStore must not run over an undriven stack)", creates, finds)
	}
	if _, err := repo.GetDeliveryByIdempotencyKey(ctx, delivery.DeliveryKey(planRunID, run.WorkflowDigest)); err == nil {
		t.Fatal("plan run has a delivery record, want none (deliverRunWithStore must not run)")
	}
}

// TestReconcileParkedDeliveryGuardPassesDrivenStack proves that the
// drive-before-delivery guard (F10) passes a driven stack through to
// deliverRunWithStore. The guard is a two-step check:
// stackDecomposedChunks identifies a multi-chunk plan run, then
// stackDriveCompleted confirms completion. A completed stack skips the
// drive and falls through to deliverRunWithStore. Contrast with
// TestReconcileParkedDeliveryLeavesUndrivenStackParkedWithDeliverPlanRun
// which proves the guard blocks an undriven stack end-to-end through
// reconcileParkedRuns (the run stays delivery_pending). This test
// verifies the guard predicates directly because the unit-test sandbox
// cannot complete delivery (deliverRunWithStore fails with a RefusalError
// that bypasses settlement, leaving the run delivery_pending — identical
// to the blocked case — so status-based assertions cannot distinguish the
// two outcomes).
func TestReconcileParkedDeliveryGuardPassesDrivenStack(t *testing.T) {
	root, storePath, _, _ := newDeliveryFixture(t)
	repo := openDeliveryStore(t, storePath)
	planRunID := seedParkedStackingPlanRunWithDeliverPlanRun(t, root, storePath, repo)
	completeParkedStackDrive(t, storePath, repo, planRunID)

	ctx := context.Background()
	// Predicate 1: stackDecomposedChunks recognizes a multi-chunk plan run.
	_, isStack := stackDecomposedChunks(ctx, repo, planRunID)
	if !isStack {
		t.Fatal("stackDecomposedChunks must report a multi-chunk stacking plan run after completeParkedStackDrive")
	}
	// Predicate 2: stackDriveCompleted reports the stack is fully driven.
	store, err := openContextStorePath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	policy := stackPlanMergePolicy(ctx, repo, planRunID)
	if !stackDriveCompleted(ctx, root, store, repo, planRunID, policy, true) {
		t.Fatal("stackDriveCompleted must report true after completeParkedStackDrive (guard must not re-drive)")
	}
}
