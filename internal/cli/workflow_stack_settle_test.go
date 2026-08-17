package cli

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// failingCASRepository wraps a real Repository and injects a failure into
// CompareAndSetRunStatus whenever the caller tries to transition a run to
// failStatus. It lets tests reach the settleErr != nil branches of
// settleStackPlanRunFailed's callers, which a healthy repository never takes
// in a single-threaded test (a genuine CAS conflict needs real concurrency).
type failingCASRepository struct {
	workflowledger.Repository
	failStatus workflowledger.RunStatus
}

func (f *failingCASRepository) CompareAndSetRunStatus(ctx context.Context, runID string, expectedVersion uint64, status workflowledger.RunStatus, finishedAt *time.Time) error {
	if status == f.failStatus {
		return errors.New("injected CAS failure")
	}
	return f.Repository.CompareAndSetRunStatus(ctx, runID, expectedVersion, status, finishedAt)
}

// failingGetRunRepository reports a fixed error from GetRun regardless of
// runID, for tests that only need settleStackPlanRunFailed's very first call
// to fail (it never reaches any other Repository method in that path).
type failingGetRunRepository struct {
	workflowledger.Repository
	err error
}

func (f *failingGetRunRepository) GetRun(ctx context.Context, runID string) (workflowledger.RunSnapshot, error) {
	return workflowledger.RunSnapshot{}, f.err
}

// TestSettleStackPlanRunFailed pins that a delivery_pending stacking plan run
// can be CAS-settled to failed, that the version is bumped, and that a second
// call is idempotent and does not re-log the cause.
func TestSettleStackPlanRunFailed(t *testing.T) {
	root, storePath, _, _ := newDeliveryFixture(t)
	repo := openDeliveryStore(t, storePath)
	planRunID := seedGrantPolicyParkedStackingPlanRun(t, root, storePath, repo)

	ctx := context.Background()
	before, err := repo.GetRun(ctx, planRunID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("seeded status = %q, want delivery_pending", before.Status)
	}

	var buf bytes.Buffer
	prevOutput := log.Writer()
	prevFlags := log.Flags()
	t.Cleanup(func() { log.SetOutput(prevOutput); log.SetFlags(prevFlags) })
	log.SetOutput(&buf)
	log.SetFlags(0)

	cause := "terminal failure: uncompletable stack"
	if err := settleStackPlanRunFailed(ctx, repo, planRunID, cause); err != nil {
		t.Fatalf("settleStackPlanRunFailed() error = %v", err)
	}

	after, err := repo.GetRun(ctx, planRunID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != workflowledger.RunStatusFailed {
		t.Fatalf("after settle status = %q, want failed", after.Status)
	}
	if after.Version != before.Version+1 {
		t.Fatalf("after settle version = %d, want %d", after.Version, before.Version+1)
	}
	wantLog := "workflow: plan run " + planRunID + " failed: " + cause
	if !strings.Contains(buf.String(), wantLog) {
		t.Fatalf("log = %q, want %q", buf.String(), wantLog)
	}

	if err := settleStackPlanRunFailed(ctx, repo, planRunID, cause); err != nil {
		t.Fatalf("second settleStackPlanRunFailed() error = %v", err)
	}

	repeat, err := repo.GetRun(ctx, planRunID)
	if err != nil {
		t.Fatal(err)
	}
	if repeat.Status != workflowledger.RunStatusFailed {
		t.Fatalf("repeat status = %q, want failed", repeat.Status)
	}
	if repeat.Version != after.Version {
		t.Fatalf("repeat version = %d, want %d (must not bump)", repeat.Version, after.Version)
	}
	if strings.Count(buf.String(), wantLog) != 1 {
		t.Fatalf("cause logged %d times, want 1; log = %q", strings.Count(buf.String(), wantLog), buf.String())
	}
}

// TestSettleStackPlanRunFailedAbsentRunNoop pins the ErrNotFound branch: a
// runID with no durable run row is a no-op, not an error - the caller may
// race a run that was already deleted or never admitted.
func TestSettleStackPlanRunFailedAbsentRunNoop(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	if err := settleStackPlanRunFailed(context.Background(), repo, "wfr-does-not-exist", "cause"); err != nil {
		t.Fatalf("settleStackPlanRunFailed() on an absent run error = %v, want nil (no-op)", err)
	}
}

// TestSettleStackPlanRunFailedPropagatesGetRunError pins that a non-NotFound
// GetRun failure (a transient store fault) propagates as-is, instead of
// being swallowed like the absent-run case.
func TestSettleStackPlanRunFailedPropagatesGetRunError(t *testing.T) {
	sentinel := errors.New("boom")
	repo := &failingGetRunRepository{err: sentinel}
	err := settleStackPlanRunFailed(context.Background(), repo, "wfr-x", "cause")
	if !errors.Is(err, sentinel) {
		t.Fatalf("settleStackPlanRunFailed() error = %v, want it to propagate the non-NotFound GetRun error", err)
	}
}

// TestSettleStackPlanRunFailedNonDeliveryPendingNoop pins that a run present
// but parked somewhere other than delivery_pending (e.g. still running) is
// left untouched: only a run actually waiting for delivery is fail-settled.
func TestSettleStackPlanRunFailedNonDeliveryPendingNoop(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	ctx := context.Background()
	runID := "wfr-running-only"
	snap := workflowledger.RunSnapshot{RunID: runID, WorkflowName: "x", Status: workflowledger.RunStatusPending}
	if err := repo.CreateRun(ctx, snap, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}

	if err := settleStackPlanRunFailed(ctx, repo, runID, "cause"); err != nil {
		t.Fatalf("settleStackPlanRunFailed() error = %v, want nil (noop for a non-delivery_pending run)", err)
	}
	after, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != workflowledger.RunStatusRunning {
		t.Fatalf("status after noop settle = %q, want unchanged running", after.Status)
	}
}

// TestRefuseFailedStackPlanRunDeliveryDefaultReason pins the fallback reason
// text: when stackPlanRunFailureReason finds nothing failed (an empty
// reason - the caller invoked refuse without confirming the gate itself),
// the refusal still carries a non-empty cause instead of an empty one.
func TestRefuseFailedStackPlanRunDeliveryDefaultReason(t *testing.T) {
	root, _, store, repo, compiled := newWorkflowBuildFixture(t)
	const planRunID = "wfr-refuse-default-reason"
	seedPlanRunDeliveryPending(t, repo, planRunID, compiled.Digest)

	err := refuseFailedStackPlanRunDelivery(context.Background(), root, store, repo, planRunID)
	if err == nil || !strings.Contains(err.Error(), "stack terminally failed") {
		t.Fatalf("refuseFailedStackPlanRunDelivery() error = %v, want the default reason text", err)
	}
	run, getErr := repo.GetRun(context.Background(), planRunID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if run.Status != workflowledger.RunStatusFailed {
		t.Fatalf("plan run status = %q, want failed", run.Status)
	}
}

// TestRefuseFailedStackPlanRunDeliveryPropagatesSettleError pins that a
// failure to fail-settle the run returns the raw settle error, not the
// errFailedStackPlanRun refusal: a caller that cannot even durably record
// the failure must see the store fault, not a misleadingly clean refusal.
func TestRefuseFailedStackPlanRunDeliveryPropagatesSettleError(t *testing.T) {
	root, _, store, repo, compiled := newWorkflowBuildFixture(t)
	const planRunID = "wfr-refuse-settle-fails"
	seedPlanRunDeliveryPending(t, repo, planRunID, compiled.Digest)
	failing := &failingCASRepository{Repository: repo, failStatus: workflowledger.RunStatusFailed}

	err := refuseFailedStackPlanRunDelivery(context.Background(), root, store, failing, planRunID)
	if err == nil || !strings.Contains(err.Error(), "injected CAS failure") {
		t.Fatalf("refuseFailedStackPlanRunDelivery() error = %v, want the raw settle error", err)
	}
	if strings.Contains(err.Error(), "cannot complete") {
		t.Fatalf("refuseFailedStackPlanRunDelivery() error = %v, want the raw settle error, not errFailedStackPlanRun", err)
	}
}

// TestSettleFailedStackPlanRunIfNeededNotFailedGate pins the false,nil
// no-op: a stack that has not yet driven (Incomplete, not Failed) must not
// be fail-settled.
func TestSettleFailedStackPlanRunIfNeededNotFailedGate(t *testing.T) {
	prepared, planRunID := seedStackDriveIncompleteGateFixture(t)

	settled, err := settleFailedStackPlanRunIfNeeded(context.Background(), prepared, planRunID, "cause")
	if err != nil {
		t.Fatalf("settleFailedStackPlanRunIfNeeded() error = %v, want nil", err)
	}
	if settled {
		t.Fatal("settled = true, want false: the gate is Incomplete, not Failed")
	}
	run, getErr := prepared.repo.GetRun(context.Background(), planRunID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if run.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("plan run status = %q, want unchanged delivery_pending", run.Status)
	}
}

// TestSettleFailedStackPlanRunIfNeededPropagatesSettleError pins that a
// Failed-gate stack whose fail-settle attempt itself errors returns
// (false, err), not a false success.
func TestSettleFailedStackPlanRunIfNeededPropagatesSettleError(t *testing.T) {
	prepared, planRunID := seedStackDriveFailedGateFixture(t)
	prepared.repo = &failingCASRepository{Repository: prepared.repo, failStatus: workflowledger.RunStatusFailed}

	settled, err := settleFailedStackPlanRunIfNeeded(context.Background(), prepared, planRunID, "cause")
	if err == nil || !strings.Contains(err.Error(), "injected CAS failure") {
		t.Fatalf("settleFailedStackPlanRunIfNeeded() error = %v, want the injected CAS failure", err)
	}
	if settled {
		t.Fatal("settled = true, want false when settle itself fails")
	}
}
