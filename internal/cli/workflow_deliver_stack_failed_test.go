package cli

// Pins two regressions the stackPlanRunFailed gate introduced:
//
//  1. `workflow deliver` had cases for Incomplete and Complete only, so a
//     terminally FAILED stack's plan run fell out of the switch into
//     deliverRunWithStore: the plan PR was published and the run settled
//     succeeded over a stack that lost a chunk.
//  2. stackPlanRunFailureReason counted a delivery_failed integration run as a
//     terminal stack failure. delivery_failed is the REPAIRABLE delivery state
//     (workflowledger.ValidRunTransition keeps outgoing edges for it), so the
//     plan run was CAS-settled to failed - a status with NO outgoing edges -
//     while the integration run itself was still repairable.

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// seedStackIntegrationRunTerminal seeds the stack's integration run and moves
// it to a terminal status seedStackIntegrationRun itself does not reach:
// failed/canceled/timed_out through running, delivery_failed through
// delivery_pending (the only valid route to it).
func seedStackIntegrationRunTerminal(t *testing.T, repo workflowledger.Repository, stackID string, status workflowledger.RunStatus) string {
	t.Helper()
	ctx := context.Background()
	from := workflowledger.RunStatusRunning
	if status == workflowledger.RunStatusDeliveryFailed {
		from = workflowledger.RunStatusDeliveryPending
	}
	seedStackIntegrationRun(t, repo, stackID, from)
	intRunID := "wfr-" + stackID + "-integration"
	stored, err := repo.GetRun(ctx, intRunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, intRunID, stored.Version, status, nil); err != nil {
		t.Fatalf("transition integration run to %q: %v", status, err)
	}
	return intRunID
}

// TestWorkflowDeliverRefusesFailedStackPlanRun pins R1: `workflow deliver` on
// the plan run of a terminally failed stack must refuse, settle the plan run
// failed, and publish NOTHING. Before the fix the stackPlanRunFailed gate fell
// out of the switch and the plan PR was created with the run settled
// succeeded, over a stack whose integration run had died.
func TestWorkflowDeliverRefusesFailedStackPlanRun(t *testing.T) {
	root, storePath, configPath, prRecorder := newDeliveryFixture(t)
	repo := openDeliveryStore(t, storePath)
	planRunID := seedGrantPolicyParkedStackingPlanRun(t, root, storePath, repo)
	mergeParkedStackChunks(t, storePath, repo, planRunID)
	seedStackIntegrationRunTerminal(t, repo, planRunID, workflowledger.RunStatusFailed)

	var stdout strings.Builder
	err := runWorkflowWithIO([]string{"deliver", planRunID, "--workspace", root, "--config", configPath, "--allow-publish"}, &stdout, io.Discard)
	if err == nil {
		t.Fatalf("deliver error = nil, want a refusal; stdout = %q", stdout.String())
	}
	if !strings.Contains(err.Error(), "stack that cannot complete") {
		t.Fatalf("deliver error = %v, want errFailedStackPlanRun", err)
	}

	ctx := context.Background()
	run, err2 := repo.GetRun(ctx, planRunID)
	if err2 != nil {
		t.Fatal(err2)
	}
	if run.Status != workflowledger.RunStatusFailed {
		t.Fatalf("plan run status = %q, want failed (never succeeded over a dead stack)", run.Status)
	}
	if creates, finds := prRecorder.calls(); creates != 0 || finds != 0 {
		t.Fatalf("PR client calls: creates=%d finds=%d, want zero (a failed stack must not publish)", creates, finds)
	}
	if _, err2 := repo.GetDeliveryByIdempotencyKey(ctx, delivery.DeliveryKey(planRunID, run.WorkflowDigest)); err2 == nil {
		t.Fatal("plan run has a delivery record, want none (deliverRunWithStore must not run)")
	}
}

// TestStackPlanRunFailureReasonDeliveryFailedIsRepairable pins R2 at the
// classifier: an integration run parked at delivery_failed is repairable (the
// operator fixes the refusal and re-delivers), so the plan run must classify
// INCOMPLETE. The genuinely edge-less statuses stay terminal.
func TestStackPlanRunFailureReasonDeliveryFailedIsRepairable(t *testing.T) {
	ctx := context.Background()

	root, store, repo, stackID := seedFailedIntegrationStack(t, workflowledger.RunStatusDeliveryFailed)
	failed, reason := stackPlanRunFailureReason(ctx, root, store, repo, stackID)
	if failed {
		t.Fatalf("stackPlanRunFailureReason() = true (%q) for a delivery_failed integration run; want false - delivery_failed is repairable", reason)
	}
	if got := classifyStackPlanRunDelivery(ctx, root, store, repo, stackID, true); got != stackPlanRunIncomplete {
		t.Fatalf("classifyStackPlanRunDelivery() = %v, want stackPlanRunIncomplete for a delivery_failed integration run", got)
	}

	for _, status := range []workflowledger.RunStatus{
		workflowledger.RunStatusFailed,
		workflowledger.RunStatusCanceled,
		workflowledger.RunStatusTimedOut,
	} {
		t.Run(string(status), func(t *testing.T) {
			// These statuses have no outgoing transition, so the stack really
			// cannot complete.
			if workflowledger.ValidRunTransition(status, workflowledger.RunStatusRunning) {
				t.Fatalf("status %q has a repair edge; it must not count as a terminal stack failure", status)
			}
			r, s, rp, id := seedFailedIntegrationStack(t, status)
			if got, _ := stackPlanRunFailureReason(ctx, r, s, rp, id); !got {
				t.Fatalf("stackPlanRunFailureReason() = false for integration run status %q, want true", status)
			}
		})
	}
}

// TestWorkflowDeliverKeepsPlanRunAliveWhenIntegrationDeliveryFailed pins R2
// end to end: a commit hook that rejects the integration PR leaves that run
// delivery_failed. The plan run must stay delivery_pending (recoverable, with
// outgoing edges) and be refused as merely UNDRIVEN, not CAS-settled to
// failed, which has no route back.
func TestWorkflowDeliverKeepsPlanRunAliveWhenIntegrationDeliveryFailed(t *testing.T) {
	root, storePath, configPath, prRecorder := newDeliveryFixture(t)
	repo := openDeliveryStore(t, storePath)
	planRunID := seedGrantPolicyParkedStackingPlanRun(t, root, storePath, repo)
	mergeParkedStackChunks(t, storePath, repo, planRunID)
	intRunID := seedStackIntegrationRunTerminal(t, repo, planRunID, workflowledger.RunStatusDeliveryFailed)

	var stdout strings.Builder
	err := runWorkflowWithIO([]string{"deliver", planRunID, "--workspace", root, "--config", configPath, "--allow-publish"}, &stdout, io.Discard)
	if err == nil {
		t.Fatalf("deliver error = nil, want the undriven-stack refusal; stdout = %q", stdout.String())
	}
	if !strings.Contains(err.Error(), "has not fully driven yet") {
		t.Fatalf("deliver error = %v, want errUndrivenStackPlanRun (the stack is repairable, not dead)", err)
	}

	ctx := context.Background()
	run, err2 := repo.GetRun(ctx, planRunID)
	if err2 != nil {
		t.Fatal(err2)
	}
	if run.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("plan run status = %q, want delivery_pending (a repairable integration failure must not kill the plan run)", run.Status)
	}
	// The plan run keeps its route to a settled state, and the integration run
	// keeps its route back to delivery.
	if !workflowledger.ValidRunTransition(run.Status, workflowledger.RunStatusSucceeded) {
		t.Fatalf("plan run status %q has no route to succeeded; the run is dead", run.Status)
	}
	intRun, err2 := repo.GetRun(ctx, intRunID)
	if err2 != nil {
		t.Fatal(err2)
	}
	if !workflowledger.ValidRunTransition(intRun.Status, workflowledger.RunStatusDeliveryPending) {
		t.Fatalf("integration run status %q cannot be re-delivered", intRun.Status)
	}
	if creates, finds := prRecorder.calls(); creates != 0 || finds != 0 {
		t.Fatalf("PR client calls: creates=%d finds=%d, want zero (no publish over an incomplete stack)", creates, finds)
	}
}
