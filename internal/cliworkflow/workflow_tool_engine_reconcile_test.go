package cliworkflow

import (
	"context"
	cliagents "github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// parkedSweepHolder is the claim holder the parked-run sweep's resume build
// claims with in wireParkedResumeStubs; tests that CAS the run under the
// claim bind the same holder context.
const parkedSweepHolder = "parked-sweep-holder"

// TestSessionReconcileParkedRunsDelivers proves a delivery_pending run left
// over from an earlier session (a restart or a crash) is published when the
// harness wires its workflow surface. Delivery authorization comes from the
// workflow's [delivery] policy - no allow_publish flag and no manual override
// is involved (the session launch path carries none). The run must settle
// succeeded and exactly one PR must be created.
func TestSessionReconcileParkedRunsDelivers(t *testing.T) {
	root, storePath, configPath, prRecorder := newDeliveryFixture(t)
	runID := runFixtureToDeliveryPending(t, root, configPath)
	repo := openDeliveryStore(t, storePath)
	seedWorktreeChange(t, root, runID, repo)

	res, err := config.Load(config.LoadOptions{ConfigPath: configPath, AllowMissingConfig: true})
	if err != nil {
		t.Fatal(err)
	}
	ApplyWorkflowStoreRoot(res, root)

	var opts tools.DefaultOptions
	// A non-nil event-bus provider marks production wiring and arms the
	// parked-run sweep; the nil-provider test paths never sweep.
	WireWorkflowToolOptions(&opts, root, res, func() *events.Bus { return nil }, false, nil)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		run, err := repo.GetRun(context.Background(), runID)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status != workflowledger.RunStatusDeliveryPending {
			break
		}
		select {
		case <-time.After(20 * time.Millisecond):
		}
	}
	run, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run status = %q, want succeeded (parked run published by the session-start sweep)", run.Status)
	}
	if creates, finds := prRecorder.calls(); creates != 1 || finds != 1 {
		t.Fatalf("PR client calls: creates=%d finds=%d, want one create and one find", creates, finds)
	}
}

// TestConfigureChatWorkspaceRunRecoverySweepFalseSkipsSweep is F14's
// regression test: a one-shot, non-interactive caller (sessions usage,
// compact) passes runRecoverySweep=false and must not trigger the
// parked-run recovery sweep - no PR published and no run status change -
// even though the workspace has an unfinished delivery_pending run and
// .mivia/workflows/ present (the only two conditions the pre-fix code
// checked before deciding to sweep).
func TestConfigureChatWorkspaceRunRecoverySweepFalseSkipsSweep(t *testing.T) {
	root, storePath, configPath, prRecorder := newDeliveryFixture(t)
	runID := runFixtureToDeliveryPending(t, root, configPath)
	repo := openDeliveryStore(t, storePath)
	seedWorktreeChange(t, root, runID, repo)

	res, err := config.Load(config.LoadOptions{ConfigPath: configPath, AllowMissingConfig: true})
	if err != nil {
		t.Fatal(err)
	}
	ApplyWorkflowStoreRoot(res, root)

	sess := chat.NewSession(res, nil)
	cleanup, err := cliagents.ConfigureChatWorkspace(sess, root, true, res, &cliagents.AgentSessionState{}, true, false, false)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	// The pre-fix sweep settles a delivery_pending run within milliseconds
	// (see TestSessionReconcileParkedRunsDelivers); poll until the status
	// is stable (time.After is allowed; time.Sleep is not).
	var run workflowledger.RunSnapshot
	var stableCount int
	for stableCount < 2 {
		var err error
		run, err = repo.GetRun(context.Background(), runID)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status == workflowledger.RunStatusDeliveryPending {
			stableCount++
		} else {
			stableCount = 0
		}
		if stableCount < 2 {
			select {
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	if run.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("run status = %q, want delivery_pending (runRecoverySweep=false must not sweep)", run.Status)
	}
	if creates, finds := prRecorder.calls(); creates != 0 || finds != 0 {
		t.Fatalf("PR client calls: creates=%d finds=%d, want zero (no sweep should run)", creates, finds)
	}
}

// seedParkedStackingPlanRun creates a delivery_pending plan-mode run of the
// mini-stack workflow (stacking enabled, delivery active,
// delivery.deliver_plan_run unset = false, merge_policy=auto) and seeds its
// stack ledger with a two-chunk plan whose chunk tasks are still PLANNED - the
// exact shape a stack drive leaves when it aborts AFTER seeding the ledger but
// BEFORE any chunk merged. Seeding alone is NOT the completion marker: the
// recovery sweep must keep such a run delivery_pending until the operator
// finishes the stack with 'mivia stack drive'. Tests that model a drive that
// completed call completeParkedStackDrive on top of this.
func seedParkedStackingPlanRun(t *testing.T, root, storePath string, repo workflowledger.Repository) string {
	t.Helper()
	return seedParkedStackingPlanRunTOML(t, root, storePath, repo, miniStackWorkflowTOML, "auto")
}

// seedGrantPolicyParkedStackingPlanRun is seedParkedStackingPlanRun under the
// default grant merge policy (merge_policy unset = "approve"): the driver does
// not auto-merge, so a delivery_pending integration run awaits the publish
// grant and IS the driver's completion state (policy-A).
func seedGrantPolicyParkedStackingPlanRun(t *testing.T, root, storePath string, repo workflowledger.Repository) string {
	t.Helper()
	grantTOML := strings.Replace(miniStackWorkflowTOML, "merge_policy = \"auto\"\n", "", 1)
	return seedParkedStackingPlanRunTOML(t, root, storePath, repo, grantTOML, "approve")
}

// seedParkedStackingPlanRunTOML is the shared seeding body behind the
// seedParkedStackingPlanRun variants: the given stacking-enabled delivery
// plan-mode workflow (delivery.deliver_plan_run unset = false) admitted as a
// delivery_pending run whose task ledger carries a two-chunk plan with every
// chunk task still PLANNED.
func seedParkedStackingPlanRunTOML(t *testing.T, root, storePath string, repo workflowledger.Repository, rawTOML, wantMergePolicy string) string {
	t.Helper()
	rawDefinition := []byte(rawTOML)
	wf, _, err := definition.ParseWorkflowTOML(rawDefinition, "mini-stack.toml")
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := definition.Compile(&wf)
	if err != nil {
		t.Fatal(err)
	}
	if compiled.Delivery == nil || compiled.Delivery.DeliverPlanRun {
		t.Fatal("mini-stack workflow must keep the plan run unpublished (deliver_plan_run=false)")
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
	runID := "wfr-parked-plan"
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
	_, chunks, _, _, err := ParseStackPlanOutputFunc([]byte(multiChunkPlanOutput))
	if err != nil || len(chunks) != 2 {
		t.Fatalf("parse multi-chunk plan = %v, %v; want 2 chunks", chunks, err)
	}
	store, err := OpenContextStorePath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := SeedStackLedgerFunc(workflowledger.NewStore(store), runID, chunks); err != nil {
		t.Fatal(err)
	}
	return runID
}

// mergeParkedStackChunks records the durable merged-chunk state a stack drive
// leaves once every chunk task is merged: the succeeded decompose output the
// driver reads (LoadStackPlanOutputFunc) plus every chunk task transitioned to
// merged. It models the crash/expiry window AFTER all chunks merged but
// BEFORE the final integration run was admitted and settled - the window the
// recovery sweep must never settle the plan run succeeded over.
func mergeParkedStackChunks(t *testing.T, storePath string, repo workflowledger.Repository, runID string) {
	t.Helper()
	seedSucceededDecomposeAttempt(t, repo, runID, []byte(multiChunkPlanOutput))
	store, err := OpenContextStorePath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	_, chunks, _, _, err := ParseStackPlanOutputFunc([]byte(multiChunkPlanOutput))
	if err != nil || len(chunks) != 2 {
		t.Fatalf("parse multi-chunk plan = %v, %v; want 2 chunks", chunks, err)
	}
	ledger := workflowledger.NewStore(store)
	for _, c := range chunks {
		if err := ledger.TransitionTask(runID, c.ID, delivery.StatusMerged); err != nil {
			t.Fatalf("transition chunk %s to merged: %v", c.ID, err)
		}
	}
}

// completeParkedStackDrive records the durable state a stack drive leaves when
// it drives to completion: the succeeded decompose output the driver reads
// (LoadStackPlanOutputFunc), every chunk task transitioned to merged, AND the
// final integration run admitted and settled - the same state
// waitIntegrationRunSettled resolves before the driver reports the stack
// complete (StackDriveCompleted checks all three).
func completeParkedStackDrive(t *testing.T, storePath string, repo workflowledger.Repository, runID string) {
	t.Helper()
	mergeParkedStackChunks(t, storePath, repo, runID)
	seedStackIntegrationRun(t, repo, runID, workflowledger.RunStatusSucceeded)
}

// seedStackIntegrationRun seeds the final full-suite integration run a
// completed stack drive admits once every chunk is merged: a run row whose
// stable invocation key is <stack-id>:integration (stackAdmissionKey; runID
// here IS the stack id, the keying LoadStackPlanOutputFunc uses), settled at the
// given status. StackDriveCompleted requires this run to be found and settled
// - not pending/running/waiting_approval - before a plan run counts as driven
// to completion, mirroring waitIntegrationRunSettled's nil-return conditions.
func seedStackIntegrationRun(t *testing.T, repo workflowledger.Repository, runID string, status workflowledger.RunStatus) {
	t.Helper()
	key, err := delivery.AdmissionKey(runID, delivery.IntegrationChunkID)
	if err != nil {
		t.Fatal(err)
	}
	run := workflowledger.RunSnapshot{
		RunID: "wfr-" + runID + "-integration", InvocationKey: key, WorkflowName: "mini-stack",
		Status: workflowledger.RunStatusPending,
	}
	raw, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{Inputs: map[string]string{"task": "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateRun(context.Background(), run, raw); err != nil {
		t.Fatal(err)
	}
	step := func(to workflowledger.RunStatus) {
		t.Helper()
		stored, err := repo.GetRun(context.Background(), run.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.CompareAndSetRunStatus(context.Background(), run.RunID, stored.Version, to, nil); err != nil {
			t.Fatal(err)
		}
	}
	switch status {
	case workflowledger.RunStatusRunning:
		step(workflowledger.RunStatusRunning)
	case workflowledger.RunStatusDeliveryPending, workflowledger.RunStatusSucceeded:
		step(workflowledger.RunStatusRunning)
		step(status)
	default:
		t.Fatalf("seedStackIntegrationRun: unsupported status %q", status)
	}
}

// TestSessionReconcileParkedRunsSkipsStackingPlanRunNotOptedIn proves the
// recovery sweep never publishes a delivery_pending stacking plan run whose
// own publication is disabled (delivery.deliver_plan_run=false, the default)
// after its multi-chunk stack drove TO COMPLETION: every chunk task merged is
// the durable completion marker the gate checks, and the crash window between
// that drive and SettlePlanRunSkippedDelivery's CAS must not end with the
// sweep publishing the plan PR. The sweep settles the plan run succeeded
// WITHOUT publishing, and a regular delivery_pending run in the same pass is
// still delivered: exactly one PR is created, and only for the non-stacking
// run.
func TestSessionReconcileParkedRunsSkipsStackingPlanRunNotOptedIn(t *testing.T) {
	root, storePath, configPath, prRecorder := newDeliveryFixture(t)
	regularRunID := runFixtureToDeliveryPending(t, root, configPath)
	repo := openDeliveryStore(t, storePath)
	seedWorktreeChange(t, root, regularRunID, repo)
	planRunID := seedParkedStackingPlanRun(t, root, storePath, repo)
	completeParkedStackDrive(t, storePath, repo, planRunID)

	e := NewSessionWorkflowEngine(root, configPath)
	e.ReconcileParkedRuns(context.Background(), false)

	ctx := context.Background()
	planRun, err := repo.GetRun(ctx, planRunID)
	if err != nil {
		t.Fatal(err)
	}
	if planRun.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("plan run status = %q, want succeeded (skipped plan run settled without publication)", planRun.Status)
	}
	regularRun, err := repo.GetRun(ctx, regularRunID)
	if err != nil {
		t.Fatal(err)
	}
	if regularRun.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("regular run status = %q, want succeeded (non-stacking delivery_pending run still delivered)", regularRun.Status)
	}
	if creates, finds := prRecorder.calls(); creates != 1 || finds != 1 {
		t.Fatalf("PR client calls: creates=%d finds=%d, want exactly one create and one find (plan run must not publish)", creates, finds)
	}
}

// TestReconcileParkedDeliveryLeavesSeededIncompletePlanParked proves the
// recovery sweep does NOT settle a delivery_pending stacking plan run whose
// task ledger carries the seeded stack plan but whose stack never drove to
// completion (the drive aborted after seeding, before any chunk merged): the
// completion gate requires every chunk task merged - the same durable state
// the driver checks - so the run stays delivery_pending for the operator to
// finish with 'mivia stack drive', and no delivery is attempted (which would
// publish the plan PR over an incomplete stack, the deliver-before-drive bug
// the skip path exists to prevent).
func TestReconcileParkedDeliveryLeavesSeededIncompletePlanParked(t *testing.T) {
	root, storePath, configPath, prRecorder := newDeliveryFixture(t)
	repo := openDeliveryStore(t, storePath)
	planRunID := seedParkedStackingPlanRun(t, root, storePath, repo)

	e := NewSessionWorkflowEngine(root, configPath)
	e.ReconcileParkedRuns(context.Background(), false)

	ctx := context.Background()
	run, err := repo.GetRun(ctx, planRunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("plan run status = %q, want delivery_pending (seeded-but-incomplete stack must stay parked)", run.Status)
	}
	if creates, finds := prRecorder.calls(); creates != 0 || finds != 0 {
		t.Fatalf("PR client calls: creates=%d finds=%d, want zero (no delivery over an incomplete stack)", creates, finds)
	}
	if _, err := repo.GetDeliveryByIdempotencyKey(ctx, delivery.DeliveryKey(planRunID, run.WorkflowDigest)); err == nil {
		t.Fatal("plan run has a delivery record, want none (DeliverRunWithStore must not run)")
	}
}

// TestReconcileParkedDeliverySettlesCompletedPlanRun proves the recovery sweep
// settles a delivery_pending stacking plan run succeeded WITHOUT publishing
// when its stack actually drove to completion: the succeeded decompose output
// is recorded and every chunk task is merged - the exact durable state the
// driver leaves and the completion gate checks.
func TestReconcileParkedDeliverySettlesCompletedPlanRun(t *testing.T) {
	root, storePath, configPath, prRecorder := newDeliveryFixture(t)
	repo := openDeliveryStore(t, storePath)
	planRunID := seedParkedStackingPlanRun(t, root, storePath, repo)
	completeParkedStackDrive(t, storePath, repo, planRunID)

	e := NewSessionWorkflowEngine(root, configPath)
	e.ReconcileParkedRuns(context.Background(), false)

	ctx := context.Background()
	run, err := repo.GetRun(ctx, planRunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("plan run status = %q, want succeeded (completed stack settled without publication)", run.Status)
	}
	if creates, finds := prRecorder.calls(); creates != 0 || finds != 0 {
		t.Fatalf("PR client calls: creates=%d finds=%d, want zero (completed plan run settles via the skip path)", creates, finds)
	}
}

// TestReconcileParkedDeliveryLeavesAutoPolicyIntegrationPendingParked proves
// the recovery sweep does NOT settle a delivery_pending stacking plan run whose
// chunk stack is fully merged with the final integration run admitted at
// delivery_pending under merge_policy=auto: the driver still auto-merges the
// integration PR and reports the stack complete only after the merge actually
// lands (waitIntegrationRunSettled), so the plan run must stay delivery_pending
// - settling it succeeded now would break the policy-auto merge contract (a
// later drive sees the integration run terminal and skips autoMergeOne).
func TestReconcileParkedDeliveryLeavesAutoPolicyIntegrationPendingParked(t *testing.T) {
	root, storePath, configPath, prRecorder := newDeliveryFixture(t)
	repo := openDeliveryStore(t, storePath)
	planRunID := seedParkedStackingPlanRun(t, root, storePath, repo)
	mergeParkedStackChunks(t, storePath, repo, planRunID)
	seedStackIntegrationRun(t, repo, planRunID, workflowledger.RunStatusDeliveryPending)

	e := NewSessionWorkflowEngine(root, configPath)
	e.ReconcileParkedRuns(context.Background(), false)

	ctx := context.Background()
	run, err := repo.GetRun(ctx, planRunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("plan run status = %q, want delivery_pending (auto-policy integration run at delivery_pending must keep the plan run parked)", run.Status)
	}
	if creates, finds := prRecorder.calls(); creates != 0 || finds != 0 {
		t.Fatalf("PR client calls: creates=%d finds=%d, want zero", creates, finds)
	}
	if _, err := repo.GetDeliveryByIdempotencyKey(ctx, delivery.DeliveryKey(planRunID, run.WorkflowDigest)); err == nil {
		t.Fatal("plan run has a delivery record, want none (DeliverRunWithStore must not run)")
	}
}

// TestReconcileParkedDeliverySettlesGrantPolicyIntegrationPending pins the
// policy-A acceptance: a delivery_pending stacking plan run whose chunk stack
// is fully merged with the final integration run admitted at delivery_pending
// under the DEFAULT grant merge policy (merge_policy unset = "approve") IS
// complete - the driver reports the stack complete at delivery_pending when it
// is not auto-merging (waitIntegrationRunSettled returns nil awaiting the
// publish grant). The sweep settles the plan run succeeded WITHOUT publishing.
func TestReconcileParkedDeliverySettlesGrantPolicyIntegrationPending(t *testing.T) {
	root, storePath, configPath, prRecorder := newDeliveryFixture(t)
	repo := openDeliveryStore(t, storePath)
	planRunID := seedGrantPolicyParkedStackingPlanRun(t, root, storePath, repo)
	mergeParkedStackChunks(t, storePath, repo, planRunID)
	seedStackIntegrationRun(t, repo, planRunID, workflowledger.RunStatusDeliveryPending)

	e := NewSessionWorkflowEngine(root, configPath)
	e.ReconcileParkedRuns(context.Background(), false)

	ctx := context.Background()
	run, err := repo.GetRun(ctx, planRunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("plan run status = %q, want succeeded (grant-policy delivery_pending integration run settles the stack)", run.Status)
	}
	if creates, finds := prRecorder.calls(); creates != 0 || finds != 0 {
		t.Fatalf("PR client calls: creates=%d finds=%d, want zero (completed plan run settles via the skip path)", creates, finds)
	}
}

// TestReconcileParkedDeliveryLeavesChunksMergedButIntegrationAbsentParked
// proves the recovery sweep does NOT settle a delivery_pending stacking plan
// run whose chunk stack is fully merged but whose final integration run was
// never admitted: the bounded drive can expire (or the process die) after
// every chunk merged but before the integration run is admitted and settled,
// and the sweep must not draw a different conclusion than the driver - which
// refuses to call the stack complete when the integration run is missing
// (waitIntegrationRunSettled errors). The run stays delivery_pending and no
// delivery is attempted.
func TestReconcileParkedDeliveryLeavesChunksMergedButIntegrationAbsentParked(t *testing.T) {
	root, storePath, configPath, prRecorder := newDeliveryFixture(t)
	repo := openDeliveryStore(t, storePath)
	planRunID := seedParkedStackingPlanRun(t, root, storePath, repo)
	mergeParkedStackChunks(t, storePath, repo, planRunID)

	e := NewSessionWorkflowEngine(root, configPath)
	e.ReconcileParkedRuns(context.Background(), false)

	ctx := context.Background()
	run, err := repo.GetRun(ctx, planRunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("plan run status = %q, want delivery_pending (merged chunks without an admitted integration run must stay parked)", run.Status)
	}
	if creates, finds := prRecorder.calls(); creates != 0 || finds != 0 {
		t.Fatalf("PR client calls: creates=%d finds=%d, want zero (no delivery over an un-integrated stack)", creates, finds)
	}
	if _, err := repo.GetDeliveryByIdempotencyKey(ctx, delivery.DeliveryKey(planRunID, run.WorkflowDigest)); err == nil {
		t.Fatal("plan run has a delivery record, want none (DeliverRunWithStore must not run)")
	}
}

// TestReconcileParkedDeliveryLeavesIntegrationInProgressParked proves the
// recovery sweep does NOT settle a delivery_pending stacking plan run whose
// chunk stack is fully merged while the final integration run is still in
// flight (pending/running/waiting_approval): the integration run was admitted
// but not yet settled, and the driver does not call the stack complete until
// it is. The plan run stays delivery_pending.
func TestReconcileParkedDeliveryLeavesIntegrationInProgressParked(t *testing.T) {
	root, storePath, configPath, prRecorder := newDeliveryFixture(t)
	repo := openDeliveryStore(t, storePath)
	planRunID := seedParkedStackingPlanRun(t, root, storePath, repo)
	mergeParkedStackChunks(t, storePath, repo, planRunID)
	seedStackIntegrationRun(t, repo, planRunID, workflowledger.RunStatusRunning)

	e := NewSessionWorkflowEngine(root, configPath)
	e.ReconcileParkedRuns(context.Background(), false)

	ctx := context.Background()
	run, err := repo.GetRun(ctx, planRunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("plan run status = %q, want delivery_pending (in-flight integration run must keep the plan run parked)", run.Status)
	}
	if creates, finds := prRecorder.calls(); creates != 0 || finds != 0 {
		t.Fatalf("PR client calls: creates=%d finds=%d, want zero", creates, finds)
	}
}

// TestReconcileParkedDeliverySettlesFailedStack proves the recovery sweep
// fail-settles a delivery_pending stacking plan run whose stack is terminally
// failed (a chunk task reached delivery.StatusFailed) instead of re-driving the
// dead stack forever. The sweep must not call driveParkedStack at all; the
// plan run ends at RunStatusFailed and no publication is attempted.
func TestReconcileParkedDeliverySettlesFailedStack(t *testing.T) {
	root, storePath, configPath, prRecorder := newDeliveryFixture(t)
	repo := openDeliveryStore(t, storePath)
	planRunID := seedParkedStackingPlanRun(t, root, storePath, repo)
	seedSucceededDecomposeAttempt(t, repo, planRunID, []byte(multiChunkPlanOutput))

	store, err := OpenContextStorePath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := workflowledger.NewStore(store).TransitionTask(planRunID, "c2", delivery.StatusFailed); err != nil {
		t.Fatalf("transition chunk c2 to failed: %v", err)
	}

	originalDrive := driveParkedStackImpl
	t.Cleanup(func() { driveParkedStackImpl = originalDrive })
	var driveCalls atomic.Int32
	driveParkedStackImpl = func(_ *sessionWorkflowEngine, _ context.Context, _ string, _ *config.Resolved, _ *storage.SQLite, _ workflowledger.Repository, _ string) (bool, error) {
		driveCalls.Add(1)
		return false, nil
	}

	e := NewSessionWorkflowEngine(root, configPath)
	e.ReconcileParkedRuns(context.Background(), false)

	ctx := context.Background()
	run, err := repo.GetRun(ctx, planRunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusFailed {
		t.Fatalf("plan run status = %q, want failed (terminally failed stack must be fail-settled)", run.Status)
	}
	if driveCalls.Load() != 0 {
		t.Fatalf("driveParkedStack calls = %d, want 0 (failed stack must not be re-driven)", driveCalls.Load())
	}
	if creates, finds := prRecorder.calls(); creates != 0 || finds != 0 {
		t.Fatalf("PR client calls: creates=%d finds=%d, want zero (failed plan run must not publish)", creates, finds)
	}
	if _, err := repo.GetDeliveryByIdempotencyKey(ctx, delivery.DeliveryKey(planRunID, run.WorkflowDigest)); err == nil {
		t.Fatal("plan run has a delivery record, want none (DeliverRunWithStore must not run)")
	}
}

// wireParkedResumeStubs replaces the resume machinery like
// wireExecuteResumeDeliveryStubs, but the build carries a controller-bearing
// build: the claim handoff (claimWorkflowResumeHandoff) only gates when the
// controller is non-nil, so the parked-run sweep exercises the real claim
// fence. The advance stub replaces WorkflowResumeRun, so no real controller
// runs.
func wireParkedResumeStubs(t *testing.T, repo workflowledger.Repository, advance func(context.Context, WorkflowControllerBuild) (workflowledger.RunSnapshot, error)) {
	t.Helper()
	originalOpen := WorkflowResumeOpenStore
	originalHooks := WorkflowResumeInstallHooks
	originalBuild := workflowResumeBuild
	originalAdmission := WorkflowResumeSetAdmission
	originalForce := WorkflowResumeSetForce
	originalRun := WorkflowResumeRun
	t.Cleanup(func() {
		WorkflowResumeOpenStore = originalOpen
		WorkflowResumeInstallHooks = originalHooks
		workflowResumeBuild = originalBuild
		WorkflowResumeSetAdmission = originalAdmission
		WorkflowResumeSetForce = originalForce
		WorkflowResumeRun = originalRun
	})
	WorkflowResumeOpenStore = func(string, config.SubagentConfig) (*storage.SQLite, workflowledger.Repository, func(), error) {
		return nil, repo, func() {}, nil
	}
	WorkflowResumeInstallHooks = func(string, bool, bool) (func(), error) { return func() {}, nil }
	workflowResumeBuild = func(_ string, _ *config.Resolved, _ *storage.SQLite, _ workflowledger.Repository, _ *definition.CompiledWorkflow, _ string, _ map[string]any, _ map[string]string, _ []byte, _ string, _ *workflowledger.Snapshot, _ []byte, recorded *workflowledger.RunSnapshot, _ map[string]bool, _ *skills.Registry, _ string, _ ledger.LedgerRepository) (WorkflowControllerBuild, error) {
		ctrl := &controller.LinearController{Holder: parkedSweepHolder}
		if recorded != nil {
			ctrl.RunID = recorded.RunID
		}
		return WorkflowControllerBuild{Dispatcher: workflowTestDispatcher{}, Controller: ctrl}, nil
	}
	WorkflowResumeSetAdmission = func(WorkflowControllerBuild) error { return nil }
	WorkflowResumeSetForce = func(WorkflowControllerBuild) error { return nil }
	WorkflowResumeRun = advance
}

// TestSessionReconcileParkedRunsResumesRunningRun proves a run left at
// running by an earlier session (its claim was released on graceful shutdown)
// is auto-resumed by the parked-run sweep: the sweep claims it fresh and
// re-advances the controller. The advance stub settles the run to succeeded;
// the run must end succeeded with exactly one controller advance.
func TestSessionReconcileParkedRunsResumesRunningRun(t *testing.T) {
	root, configPath, repo, runID := newExecuteResumeDeliveryFixture(t)
	var advanceCalls atomic.Int32
	wireParkedResumeStubs(t, repo, func(ctx context.Context, _ WorkflowControllerBuild) (workflowledger.RunSnapshot, error) {
		advanceCalls.Add(1)
		stored, err := repo.GetRun(ctx, runID)
		if err != nil {
			return workflowledger.RunSnapshot{}, err
		}
		// Writes are fenced to the claim holder the sweep claimed with; bind
		// the holder context like the real controller does.
		claimCtx := workflowledger.ContextWithClaimHolder(ctx, parkedSweepHolder)
		if err := repo.CompareAndSetRunStatus(claimCtx, runID, stored.Version, workflowledger.RunStatusSucceeded, nil); err != nil {
			return workflowledger.RunSnapshot{}, err
		}
		return repo.GetRun(ctx, runID)
	})

	res, err := config.Load(config.LoadOptions{ConfigPath: configPath, AllowMissingConfig: true})
	if err != nil {
		t.Fatal(err)
	}
	ApplyWorkflowStoreRoot(res, root)
	e := NewSessionWorkflowEngine(root, configPath)

	e.ReconcileParkedRuns(context.Background(), false)
	waitForSessionEngineIdle(t, e, runID)

	run, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run status = %q, want succeeded (sweep resumed the interrupted run)", run.Status)
	}
	if got := advanceCalls.Load(); got != 1 {
		t.Fatalf("controller advances = %d, want 1", got)
	}
}

// TestSessionReconcileParkedRunsSkipsLiveClaim proves the sweep never
// double-executes a run another session is still running: a fresh claim
// within its lease belongs to a live holder, the claim handoff refuses the
// takeover, prepareResume fails synchronously, and the sweep moves on. The
// advance stub must never be called and the run must stay running.
func TestSessionReconcileParkedRunsSkipsLiveClaim(t *testing.T) {
	root, configPath, repo, runID := newExecuteResumeDeliveryFixture(t)
	var advanceCalls atomic.Int32
	wireParkedResumeStubs(t, repo, func(ctx context.Context, _ WorkflowControllerBuild) (workflowledger.RunSnapshot, error) {
		advanceCalls.Add(1)
		return repo.GetRun(ctx, runID)
	})
	ctx := context.Background()
	if err := repo.ClaimRun(ctx, runID, "live-session"); err != nil {
		t.Fatal(err)
	}

	res, err := config.Load(config.LoadOptions{ConfigPath: configPath, AllowMissingConfig: true})
	if err != nil {
		t.Fatal(err)
	}
	ApplyWorkflowStoreRoot(res, root)
	e := NewSessionWorkflowEngine(root, configPath)

	// The sweep processes runs synchronously; the live-claim run is refused at
	// the claim handoff, so when this returns the sweep is done with it.
	e.ReconcileParkedRuns(ctx, false)

	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusRunning {
		t.Fatalf("run status = %q, want running (sweep must skip a live-claimed run)", run.Status)
	}
	if got := advanceCalls.Load(); got != 0 {
		t.Fatalf("controller advances = %d, want 0 (live-claimed run must not be resumed)", got)
	}
}
