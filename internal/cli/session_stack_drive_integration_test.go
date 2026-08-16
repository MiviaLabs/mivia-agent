package cli

// Session stack-drive integration tests: real end-to-end drives through the
// session engine and its recovery sweep. The shared fixture (real
// LinearControllers, real git worktrees/fetch/push against a real bare origin,
// real delivery, scripted agent and PR seams) lives in
// session_stack_drive_integration_fixture_test.go.

import (
	"context"
	"log"
	"testing"
	"time"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/tasks"
)

// TestSessionSweepDrivesParkedStackAfterAbortedDrive is THE parked-stack wedge
// regression: a session whose bounded in-session drive aborts after publishing
// the first chunk PR (here: the merge queue never merges it within the bound)
// used to leave the plan run parked at delivery_pending FOREVER - the recovery
// sweep only checked stackDriveCompleted and then said "leaving parked" with
// no chunk runs, no PRs, and no drive. Now the sweep DRIVES the parked stack
// itself. The test proves both halves: the wedge state (plan run parked, c1
// published, c2 NOT admitted, NOT settled) and the recovery (a fresh session's
// sweep drives every remaining chunk through delivery + merge, admits the
// integration run, and settles the plan run succeeded).
func TestSessionSweepDrivesParkedStackAfterAbortedDrive(t *testing.T) {
	it := newStackDriveIT(t, "", "")
	it.merges.enabled.Store(false) // the in-session drive can never merge: it must abort on its bound
	planRunID := it.startPlanRun(8 * time.Second)

	// --- The wedge, exactly as reported: ---
	if got := it.runStatus(planRunID); got != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("plan run status = %q, want delivery_pending after the aborted drive", got)
	}
	statuses := it.taskStatuses(planRunID)
	if statuses["c1"] != stackStatusPublished {
		t.Fatalf("c1 task = %q, want published (its PR was created before the drive aborted)", statuses["c1"])
	}
	// c2 is SEEDED (planned - the durable plan ledger records every chunk
	// decompose declared) but must NOT have been admitted: no run under its
	// stable key, and its task still at a pre-admission status. The wedge is
	// "c2 was never driven", not "c2 is absent from the ledger" (live
	// finding: this asserted on task-key presence, which is always true for
	// a seeded chunk, and failed the drive-abort case before the sweep even
	// ran).
	c2Run, c2Found, c2Err := stackRunRef(it.repo, planRunID, "c2")
	if c2Err != nil || c2Found {
		t.Fatalf("c2 was admitted while its dependency c1 is unmerged: found=%v err=%v (task map: %v)", c2Found, c2Err, statuses)
	}
	_ = c2Run
	if got := statuses["c2"]; got != stackStatusPlanned {
		t.Fatalf("c2 task = %q, want %q (seeded but not admitted)", got, stackStatusPlanned)
	}
	if creates, _ := it.prs.callCounts(); creates != 1 {
		t.Fatalf("PR creates = %d, want exactly 1 (c1) before the abort", creates)
	}
	if got := it.runStatus(planRunID); got == workflowledger.RunStatusSucceeded {
		t.Fatal("plan run settled succeeded over an incomplete stack - the wedge settle bug")
	}
	if heads := it.originHeads(); len(heads) != 1 {
		t.Fatalf("origin wf/* branches = %v, want exactly the c1 branch (c1 pushed, not merged)", heads)
	}

	// --- The fix: a fresh session's recovery sweep drives the parked stack. ---
	it.merges.enabled.Store(true)
	it.runSweep()

	if got := it.runStatus(planRunID); got != workflowledger.RunStatusSucceeded {
		t.Fatalf("plan run status after the sweep = %q, want succeeded (the parked stack was driven to completion and settled)", got)
	}
	statuses = it.taskStatuses(planRunID)
	for _, id := range []string{"c1", "c2"} {
		if statuses[id] != stackStatusMerged {
			t.Fatalf("chunk %s task = %q, want merged after the recovery sweep", id, statuses[id])
		}
	}
	intRun, found, err := stackRunRef(it.repo, planRunID, stackIntegrationChunkID)
	if err != nil || !found {
		t.Fatalf("integration run ref: found=%v err=%v", found, err)
	}
	if got := it.runStatus(intRun.RunID); got != workflowledger.RunStatusSucceeded {
		t.Fatalf("integration run status = %q, want succeeded", got)
	}
	if recs, rerr := it.repo.ListDeliveries(context.Background(), intRun.RunID); rerr == nil {
		for _, r := range recs {
			log.Printf("DBG integration delivery rec: status=%q head=%q sha=%q deferred=%d mode=%q", r.Status, r.HeadRef, r.CommitSHA, r.StackRemainingCommits, r.Mode)
		}
	} else {
		log.Printf("DBG integration deliveries err: %v", rerr)
	}
	if creates, finds := it.prs.callCounts(); creates != 3 || finds < 3 {
		t.Fatalf("PR client: creates=%d finds=%d, want 3 creates (c1, c2, integration) with finds, all real", creates, finds)
	}
	if heads := it.originHeads(); len(heads) != 0 {
		t.Fatalf("origin wf/* branches after completion = %v, want none (every PR branch was merged/deleted)", heads)
	}
}

// TestSessionSweepDrivesChunkRetryAfterTransientFailure proves the durable
// chunk-retry path end to end: the chunk's implement step fails once (the run
// fails), the drive reopens the chunk with a bounded attempt count and stops
// (the in-session attempt is bounded by design - the durable recovery sweep
// is the retry engine), then the sweep re-admits the chunk under the SAME
// stable key, and the retried run implements, delivers, and merges - then the
// rest of the stack completes and the plan run settles.
func TestSessionSweepDrivesChunkRetryAfterTransientFailure(t *testing.T) {
	it := newStackDriveIT(t, "", "")
	it.runner.failFirstImplement = 1
	planRunID := it.startPlanRun(60 * time.Second)

	// The injected failure fails the first c1 run; the in-session drive
	// reopens the chunk (attempt 2 of 3) and stops, leaving the plan run
	// parked at delivery_pending - the durable wedge the sweep must break.
	if got := it.runStatus(planRunID); got != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("plan run status = %q, want delivery_pending (the drive reopens the failed chunk and stops, it does not settle)", got)
	}
	statuses := it.taskStatuses(planRunID)
	if statuses["c1"] != stackStatusReopened {
		t.Fatalf("c1 task = %q, want reopened (bounded attempt 2) after the injected failure", statuses["c1"])
	}
	runsBefore, err := it.repo.ListRuns(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	failedFirst := 0
	for _, r := range runsBefore {
		if r.InvocationKey == planRunID+":c1" && r.Status == workflowledger.RunStatusFailed {
			failedFirst++
		}
	}
	if failedFirst != 1 {
		t.Fatalf("failed c1 runs with the stable key = %d, want exactly 1 (the injected failure); the drive must never settle the plan run over it", failedFirst)
	}

	// The durable backstop: the recovery sweep re-drives the parked stack.
	// The retried chunk runs under the SAME stable key (a NEW run id - the
	// failed run stays recorded), implements, delivers, merges, then the rest
	// of the stack completes and the plan run settles succeeded.
	it.runSweep()

	if got := it.runStatus(planRunID); got != workflowledger.RunStatusSucceeded {
		t.Fatalf("plan run status = %q, want succeeded (the retried sweep completed the stack)", got)
	}
	statuses = it.taskStatuses(planRunID)
	for _, id := range []string{"c1", "c2"} {
		if statuses[id] != stackStatusMerged {
			t.Fatalf("chunk %s task = %q, want merged after the retry", id, statuses[id])
		}
	}
	if creates, _ := it.prs.callCounts(); creates != 3 {
		t.Fatalf("PR creates = %d, want 3 (one per chunk plus the final integration PR; the failed run never published)", creates)
	}
	c1Run, found, err := stackRunRef(it.repo, planRunID, "c1")
	if err != nil || !found {
		t.Fatalf("c1 run ref: found=%v err=%v", found, err)
	}
	if got := it.runStatus(c1Run.RunID); got != workflowledger.RunStatusSucceeded {
		t.Fatalf("retried c1 run status = %q, want succeeded (the retry re-admitted under the same key)", got)
	}
	runs, err := it.repo.ListRuns(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	failed := 0
	for _, r := range runs {
		if r.InvocationKey == planRunID+":c1" && r.Status == workflowledger.RunStatusFailed {
			failed++
		}
	}
	if failed != 1 {
		t.Fatalf("failed c1 runs with the stable key = %d, want 1 (the injected failure stays recorded); c1 final run = %s", failed, c1Run.RunID)
	}
}

// TestSessionSweepDrivesIncrementalDecomposeWaves proves the has_more
// continuation path end to end: wave 0 declares more scope, so after wave 0
// merges the drive admits a decompose-continuation run (same workflow, started
// at decompose with stack_mode=decompose_continue), seeds wave 1's chunks, and
// drives them through delivery + merge before the integration run.
func TestSessionSweepDrivesIncrementalDecomposeWaves(t *testing.T) {
	it := newStackDriveIT(t, "", hasMorePlanOutput)
	planRunID := it.startPlanRun(60 * time.Second)

	if got := it.runStatus(planRunID); got != workflowledger.RunStatusSucceeded {
		t.Fatalf("plan run status = %q, want succeeded after both waves drove", got)
	}
	statuses := it.taskStatuses(planRunID)
	for _, id := range []string{"c1", "c2", "c3", "c4"} {
		if statuses[id] != stackStatusMerged {
			t.Fatalf("chunk %s task = %q, want merged (wave 0 + continuation wave)", id, statuses[id])
		}
	}
	if creates, _ := it.prs.callCounts(); creates != 5 {
		t.Fatalf("PR creates = %d, want 5 (c1..c4 + integration)", creates)
	}
	// The continuation run must exist in the run ledger (wave 1 admission).
	contRun, found, err := stackDecomposeContinueRunRef(it.repo, planRunID, 1)
	if err != nil || !found {
		t.Fatalf("decompose continuation wave 1 run: found=%v err=%v", found, err)
	}
	if got := it.runStatus(contRun.RunID); got != workflowledger.RunStatusDeliveryPending && got != workflowledger.RunStatusSucceeded {
		t.Fatalf("continuation run status = %q, want delivery_pending/succeeded", got)
	}
}

// TestSessionSweepGrantPolicyPausesAndResumes proves merge_policy=approve end
// to end: the drive admits each chunk, marks it reviewed, and PAUSES (a
// durable errStackAwaitsGrant - never an error, never a settle). The human
// grant (`workflow deliver --allow-publish`) plus a manual merge advances the
// stack across sweep ticks; once every chunk merged and the integration run
// was admitted, the plan run settles succeeded under the grant default.
func TestSessionSweepGrantPolicyPausesAndResumes(t *testing.T) {
	it := newStackDriveIT(t, "approve", "")
	planRunID := it.startPlanRun(3 * time.Second)

	// Tick 1: c1 admitted and reviewed; the stack waits for the grant.
	if got := it.runStatus(planRunID); got != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("plan run status = %q, want delivery_pending (grant pause is a durable park, not a settle)", got)
	}
	statuses := it.taskStatuses(planRunID)
	if statuses["c1"] != stackStatusReviewed {
		t.Fatalf("c1 task = %q, want reviewed (no auto-merge under approve; the human must publish)", statuses["c1"])
	}
	if creates, _ := it.prs.callCounts(); creates != 0 {
		t.Fatalf("PR creates = %d, want 0 before the publish grant", creates)
	}
	// A sweep under the pause must NOT settle the plan run (drove=true is not
	// completion) and must not error: it re-parks, ready to resume.
	it.runSweep()
	if got := it.runStatus(planRunID); got != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("plan run status after the paused sweep = %q, want delivery_pending (grant pause survives the sweep)", got)
	}

	// Grant c1 (real delivery -> PR) and merge it (human), then sweep: c2 is
	// admitted and reviewed; the pause fires again on the FIRST poll pass.
	c1Run, found, err := stackRunRef(it.repo, planRunID, "c1")
	if err != nil || !found {
		t.Fatalf("c1 run ref: found=%v err=%v", found, err)
	}
	it.deliverRun(c1Run.RunID)
	it.mergeBranch(c1Run.RunID)
	it.runSweep()
	statuses = it.taskStatuses(planRunID)
	if statuses["c1"] != stackStatusMerged {
		t.Fatalf("c1 task after grant+merge = %q, want merged", statuses["c1"])
	}
	if statuses["c2"] != stackStatusReviewed {
		t.Fatalf("c2 task = %q, want reviewed (grant checkpoint for wave 2)", statuses["c2"])
	}
	if got := it.runStatus(planRunID); got != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("plan run status = %q, want delivery_pending while c2 waits for its grant", got)
	}

	// Grant c2, merge it, sweep: the stack completes; the integration run is
	// admitted and waits for ITS grant; under approve that is completion, so
	// the plan run settles succeeded.
	c2Run, found, err := stackRunRef(it.repo, planRunID, "c2")
	if err != nil || !found {
		t.Fatalf("c2 run ref: found=%v err=%v", found, err)
	}
	it.deliverRun(c2Run.RunID)
	it.mergeBranch(c2Run.RunID)
	it.runSweep()

	if got := it.runStatus(planRunID); got != workflowledger.RunStatusSucceeded {
		t.Fatalf("plan run status = %q, want succeeded after the final grant + merge", got)
	}
	statuses = it.taskStatuses(planRunID)
	for _, id := range []string{"c1", "c2"} {
		if statuses[id] != stackStatusMerged {
			t.Fatalf("chunk %s task = %q, want merged at completion", id, statuses[id])
		}
	}
	intRun, found, err := stackRunRef(it.repo, planRunID, stackIntegrationChunkID)
	if err != nil || !found {
		t.Fatalf("integration run ref: found=%v err=%v", found, err)
	}
	if got := it.runStatus(intRun.RunID); got != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("integration run status = %q, want delivery_pending (it awaits its own publish grant under approve)", got)
	}
	if creates, _ := it.prs.callCounts(); creates != 2 {
		t.Fatalf("PR creates = %d, want 2 (one per granted chunk; the integration run was never granted)", creates)
	}
}

// TestSessionSingleModePlanPublishesOwnPR proves the no-stack edge end to end:
// a plan run whose decompose settles single-mode has nothing to stack, so the
// session delivers the PLAN run's own PR (real delivery) and settles it
// succeeded - with zero chunk tasks and zero chunk PRs.
func TestSessionSingleModePlanPublishesOwnPR(t *testing.T) {
	it := newStackDriveIT(t, "", singlePlanOutput)
	planRunID := it.startPlanRun(60 * time.Second)

	if got := it.runStatus(planRunID); got != workflowledger.RunStatusSucceeded {
		t.Fatalf("plan run status = %q, want succeeded (its own PR was delivered)", got)
	}
	if creates, _ := it.prs.callCounts(); creates != 1 {
		t.Fatalf("PR creates = %d, want 1 (the plan run's own PR)", creates)
	}
	byID, err := stackTaskMap(tasks.NewStore(it.store), planRunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(byID) != 0 {
		t.Fatalf("chunk tasks = %d, want 0 (single-mode plans have nothing to stack)", len(byID))
	}
	// The plan run's PR must be recorded as delivered with a pushed branch.
	records, err := it.repo.ListDeliveries(context.Background(), planRunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Status != "succeeded" {
		t.Fatalf("plan run delivery records = %+v, want one succeeded record", records)
	}
}
