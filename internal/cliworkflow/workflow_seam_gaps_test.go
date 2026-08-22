package cliworkflow

// workflow_seam_gaps_test.go covers the fault branches behind the session
// engine seams: the Delete write fault, the Deliver post-success ledger
// reopen/read faults, and the recovery sweep's post-delivery re-read that
// routes a run back to resume.

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// runningAfterSuccessRepo wraps the ledger repository and reports a settled
// (succeeded) run as running, so the recovery sweep's post-delivery re-read
// takes the repair-resume route instead of the terminal-event route.
type runningAfterSuccessRepo struct {
	workflowledger.Repository
}

// GetRun rewrites a succeeded status to running and passes every other read
// through unchanged.
func (r runningAfterSuccessRepo) GetRun(ctx context.Context, runID string) (workflowledger.RunSnapshot, error) {
	snap, err := r.Repository.GetRun(ctx, runID)
	if err != nil {
		return snap, err
	}
	if snap.Status == workflowledger.RunStatusSucceeded {
		snap.Status = workflowledger.RunStatusRunning
	}
	return snap, nil
}

// TestSessionEngineDeleteRunWriteFault covers the Delete write-fault branch:
// a ledger that refuses the delete write after the operator claim succeeds
// must surface the write error.
func TestSessionEngineDeleteRunWriteFault(t *testing.T) {
	root, _, _, closeFn, ctx, runID := openEventsFixtureWithRun(t, "wfr-cov-delete-write-fault")
	defer closeFn()

	sentinel := errors.New("scripted delete-run failure")
	original := SessionDeleteRunFunc
	SessionDeleteRunFunc = func(context.Context, workflowledger.Repository, string) error { return sentinel }
	t.Cleanup(func() { SessionDeleteRunFunc = original })

	e := NewSessionWorkflowEngine(root, filepath.Join(root, "config.toml"))
	_, err := e.Delete(ctx, runID, true)
	if !errors.Is(err, sentinel) {
		t.Fatalf("Delete() error = %v, want the scripted delete-write failure", err)
	}
}

// TestSessionEngineDeliverReopenFault covers the Deliver post-success reopen
// fault: a delivery that succeeds but whose ledger re-open fails must report
// the unknown status, not an error.
func TestSessionEngineDeliverReopenFault(t *testing.T) {
	root, storePath, configPath, _ := newDeliveryFixture(t)
	runID := runFixtureToDeliveryPending(t, root, configPath)
	repo := openDeliveryStore(t, storePath)
	seedWorktreeChange(t, root, runID, repo)

	sentinel := errors.New("scripted reopen failure")
	original := SessionDeliverLedgerReopenFunc
	SessionDeliverLedgerReopenFunc = func(string, string) (workflowledger.Repository, func(), error) {
		return nil, nil, sentinel
	}
	t.Cleanup(func() { SessionDeliverLedgerReopenFunc = original })

	e := NewSessionWorkflowEngine(root, configPath)
	result, err := e.Deliver(context.Background(), runID, true)
	if err != nil {
		t.Fatalf("Deliver() error = %v, want the reopen fault mapped to an unknown status", err)
	}
	if result.Status != "unknown" {
		t.Fatalf("Deliver() status = %q, want unknown after the reopen fault", result.Status)
	}
}

// TestSessionEngineDeliverRereadFault covers the Deliver post-success re-read
// fault: a delivery that succeeds but whose settled status read fails must
// report the unknown status, not an error.
func TestSessionEngineDeliverRereadFault(t *testing.T) {
	root, storePath, configPath, _ := newDeliveryFixture(t)
	runID := runFixtureToDeliveryPending(t, root, configPath)
	repo := openDeliveryStore(t, storePath)
	seedWorktreeChange(t, root, runID, repo)

	original := SessionDeliverLedgerReopenFunc
	SessionDeliverLedgerReopenFunc = func(root, configPath string) (workflowledger.Repository, func(), error) {
		real, closeFn, err := OpenWorkflowReportContext(root, configPath)
		if err != nil {
			return nil, nil, err
		}
		return &faultRepo{Repository: real, getRunFailFrom: 1}, closeFn, nil
	}
	t.Cleanup(func() { SessionDeliverLedgerReopenFunc = original })

	e := NewSessionWorkflowEngine(root, configPath)
	result, err := e.Deliver(context.Background(), runID, true)
	if err != nil {
		t.Fatalf("Deliver() error = %v, want the read fault mapped to an unknown status", err)
	}
	if result.Status != "unknown" {
		t.Fatalf("Deliver() status = %q, want unknown after the re-read fault", result.Status)
	}
}

// TestReconcileParkedDeliveryRereadRunningRoutesToResume covers the sweep's
// post-delivery repair route: a delivery that succeeds while the re-read
// reports the run running again must re-enter the resume path; the resume of
// a durably terminal run refuses and only logs, and the delivery itself
// stays settled.
func TestReconcileParkedDeliveryRereadRunningRoutesToResume(t *testing.T) {
	root, storePath, configPath, prRecorder := newDeliveryFixture(t)
	runID := runFixtureToDeliveryPending(t, root, configPath)
	baseRepo := openDeliveryStore(t, storePath)
	seedWorktreeChange(t, root, runID, baseRepo)

	res, err := config.Load(config.LoadOptions{ConfigPath: configPath, WorkspaceRoot: root, AllowMissingConfig: true})
	if err != nil {
		t.Fatal(err)
	}
	ApplyWorkflowStoreRoot(res, root)
	store, _, closeStore, err := OpenWorkflowStore(root, res.Subagents)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeStore)

	e := NewSessionWorkflowEngine(root, configPath)
	e.reconcileParkedDelivery(context.Background(), root, res, store, runningAfterSuccessRepo{Repository: baseRepo}, ContextStorePath(root, res.Subagents), runID, false)

	ctx := context.Background()
	fresh, err := baseRepo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run status = %q, want succeeded (delivery completed before the running re-read)", fresh.Status)
	}
	if creates, finds := prRecorder.calls(); creates != 1 || finds != 1 {
		t.Fatalf("PR client calls: creates=%d finds=%d, want one create and one find", creates, finds)
	}
}
