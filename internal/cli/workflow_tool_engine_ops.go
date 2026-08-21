package cli

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// Cancel implements workflowledger.Engine.
// Refusal gates first (a lock-free status read): a terminal run is an
// idempotent no-op and a delivery_pending run is refused, both without
// stopping the in-process controller (F13). Only then does it stop the
// in-process controller and settle via the same execution-lock + CancelRun
// path as `mivia workflow cancel` - the stop must precede the lock, because
// the session's own run goroutine holds the per-run execution flock for its
// whole lifetime and can only release it once stopped.
func (e *sessionWorkflowEngine) Cancel(ctx context.Context, runID string) (workflowledger.CancelResult, error) {
	if e == nil {
		return workflowledger.CancelResult{}, fmt.Errorf("workflow engine is nil")
	}
	if strings.TrimSpace(runID) == "" {
		return workflowledger.CancelResult{}, fmt.Errorf("run_id is required")
	}
	e.mu.Lock()
	active := e.active[runID]
	e.mu.Unlock()
	// Gate refusals on a LOCK-FREE status read, before the execution lock
	// and before stopActive: a delivery_pending run may be mid-publish
	// under a live claim (this or another host), still being driven by this
	// session's own goroutine, and a refused or idempotent cancel must leave
	// both the claims and that live drive untouched (F13). The read cannot
	// sit behind the execution lock: while this session is driving the run,
	// that lock is held by the run goroutine itself.
	if status, ok := e.readRunStatusForCancel(ctx, runID); ok {
		if result, err, resolved := resolveSessionCancelRefusal(runID, status); resolved {
			return result, err
		}
	}
	// Stop the in-process controller FIRST: the run is neither terminal nor
	// delivery_pending, so this cancel is going to proceed, and the bounded
	// lock acquisition below can only succeed once the run goroutine has
	// exited and released the per-run execution flock it holds.
	e.stopActive(ctx, runID)
	releaseExecution, repo, store, closeFn, err := openWorkflowResolutionContextBounded(ctx, e.root, e.configPath, runID, workflowResolutionLockWait)
	if err != nil {
		return workflowledger.CancelResult{}, err
	}
	defer closeFn()
	defer releaseExecution()
	// Re-resolve under the lock: the run may have settled, or parked at
	// delivery_pending, between the gate read above and the stop. A
	// delivery_pending park here is a refused cancel exactly as at the gate
	// (the periodic sweep re-drives the stopped delivery on its next tick).
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		return workflowledger.CancelResult{}, err
	}
	if result, err, resolved := resolveSessionCancelRefusal(runID, run.Status); resolved {
		return result, err
	}
	return e.settleSessionCancel(ctx, active, repo, store, runID)
}

// settleSessionCancel performs the gated cancel's ledger work: claim (never
// clear) the run, settle it canceled through the guarded coordinator, and
// publish the terminal progress events. The caller holds the execution lock
// and has already stopped the in-process controller.
func (e *sessionWorkflowEngine) settleSessionCancel(ctx context.Context, active *sessionActiveRun, repo workflowledger.Repository, store *storage.SQLite, runID string) (workflowledger.CancelResult, error) {
	// Never clear a held claim: cancel accepts any run_id, and a blind clear
	// would strip a live delivery claim (held by this or another host mid-
	// publish) and enable double-publish. Claim instead; an expired lease may
	// be taken over with the exclusion fence still armed, but a fresh foreign
	// claim is refused outright.
	holder := newWorkflowCancelHolder()
	if err := claimForCancel(ctx, repo, runID, holder); err != nil {
		return workflowledger.CancelResult{}, err
	}
	defer func() { _ = repo.ReleaseRun(context.Background(), runID, holder) }()
	attempts, err := cancelRunWithGuardedCoordinator(ctx, active, repo, store, runID, holder)
	if err != nil {
		// Context cancel or a prior settle may already leave the run terminal.
		run, getErr := repo.GetRun(ctx, runID)
		if getErr == nil && workflowledger.IsTerminalRunStatus(run.Status) {
			return workflowledger.CancelResult{RunID: runID, Status: string(run.Status)}, nil
		}
		return workflowledger.CancelResult{}, err
	}
	// Terminal progress: one step_completed(canceled) per attempt the cancel
	// settled, so TUI and metrics observe the operator cancel like any other
	// run terminal event.
	e.publishCanceledAttempts(runID, attempts)
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		return workflowledger.CancelResult{}, err
	}
	// Run-level terminal event: the operator cancel settled the run, so bus
	// consumers see the same run_finished signal a controller-driven terminal
	// would emit.
	if workflowledger.IsTerminalRunStatus(run.Status) {
		if sink := e.workflowProgressSink(); sink != nil {
			sink.Emit(controller.ProgressEvent{
				Kind: controller.ProgressRunFinished, RunID: runID, Detail: string(run.Status),
				Timestamp: time.Now(),
			})
		}
	}
	return workflowledger.CancelResult{RunID: runID, Status: string(run.Status)}, nil
}

// readRunStatusForCancel is Cancel's lock-free gate read. A read failure
// (unopenable workspace, missing run) reports ok=false: the gated path then
// falls through to the lock-protected read, which surfaces the same failure
// as the command's own error.
func (e *sessionWorkflowEngine) readRunStatusForCancel(ctx context.Context, runID string) (workflowledger.RunStatus, bool) {
	repo, closeFn, err := openWorkflowReportContext(e.root, e.configPath)
	if err != nil {
		return "", false
	}
	defer closeFn()
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		return "", false
	}
	return run.Status, true
}

// resolveSessionCancelRefusal resolves a cancel verdict up front from the
// run's status, shared by the lock-free gate read and the lock-protected
// re-read so the two can never drift: a terminal run is an idempotent
// success (resolved with its settled result), a delivery_pending run is an
// error (it waits on publication, not cancellation), and anything else
// falls through to the actual cancel (resolved=false).
func resolveSessionCancelRefusal(runID string, status workflowledger.RunStatus) (workflowledger.CancelResult, error, bool) {
	if workflowledger.IsTerminalRunStatus(status) {
		return workflowledger.CancelResult{RunID: runID, Status: string(status)}, nil, true
	}
	if status == workflowledger.RunStatusDeliveryPending {
		return workflowledger.CancelResult{}, fmt.Errorf("run %q is waiting for delivery; deliver it or leave it for cleanup before cancel", runID), true
	}
	return workflowledger.CancelResult{}, nil, false
}

// newWorkflowCancelHolder mints the run-claim holder for a session-engine
// cancel attempt.
func newWorkflowCancelHolder() string {
	var value [10]byte
	_, _ = rand.Read(value[:])
	return "wfcancel-" + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(value[:])
}

// newWorkflowDeleteHolder mints the run-claim holder for a session-engine
// delete attempt.
func newWorkflowDeleteHolder() string {
	var value [10]byte
	_, _ = rand.Read(value[:])
	return "wfdelete-" + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(value[:])
}

// Delete implements workflowledger.Engine.
// It removes a run from the durable ledger via the same execution-lock +
// claim path as `mivia workflow delete`. Settled runs (terminal or
// delivery_pending) are always deletable; force also permits a non-terminal
// run (pending/running/waiting_approval) — the crash-recovery override for a
// run stranded by a dead executor. The status gate resolves BEFORE any claim
// mutation: a delivery_pending run may be mid-publish under a live claim (this
// or another host), and a refused delete must leave claims untouched. A fresh
// claim held by a live executor is refused even with force; only an expired
// lease is taken over, so an actively executing run can never be deleted.
func (e *sessionWorkflowEngine) Delete(ctx context.Context, runID string, force bool) (workflowledger.DeleteResult, error) {
	if e == nil {
		return workflowledger.DeleteResult{}, fmt.Errorf("workflow engine is nil")
	}
	if strings.TrimSpace(runID) == "" {
		return workflowledger.DeleteResult{}, fmt.Errorf("run_id is required")
	}
	releaseExecution, repo, _, closeFn, err := openWorkflowResolutionContextBounded(ctx, e.root, e.configPath, runID, workflowResolutionLockWait)
	if err != nil {
		return workflowledger.DeleteResult{}, err
	}
	defer closeFn()
	defer releaseExecution()
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		return workflowledger.DeleteResult{}, err
	}
	if !workflowledger.IsDeletableRunStatus(run.Status) && !force {
		return workflowledger.DeleteResult{}, fmt.Errorf("run %q is %q; cancel it before delete, or pass force only after the prior executor stopped (a live claim is still refused)", runID, run.Status)
	}
	// Never clear a held claim: delete accepts any run_id, and a blind clear
	// would strip a live delivery claim (held by this or another host mid-
	// publish) and enable double-publish. Claim instead; an expired lease may
	// be taken over, a fresh foreign claim is refused outright.
	holder := newWorkflowDeleteHolder()
	if err := claimWorkflowOperator(ctx, repo, runID, holder); err != nil {
		return workflowledger.DeleteResult{}, fmt.Errorf("workflow run %q is claimed by another executor; delete refused", runID)
	}
	ctx = workflowledger.ContextWithClaimHolder(ctx, holder)
	if err := repo.DeleteRun(ctx, runID); err != nil {
		return workflowledger.DeleteResult{}, err
	}
	return workflowledger.DeleteResult{RunID: runID, Status: string(run.Status), Deleted: true}, nil
}

// Deliver implements workflowledger.Engine.
// Publication uses the CLI deliver path (run-owned worktree + execution lock).
// It never delivers from the caller workspace root via localengine.
func (e *sessionWorkflowEngine) Deliver(ctx context.Context, runID string, allowPublish bool) (workflowledger.DeliverResult, error) {
	if e == nil {
		return workflowledger.DeliverResult{}, fmt.Errorf("workflow engine is nil")
	}
	if strings.TrimSpace(runID) == "" {
		return workflowledger.DeliverResult{}, fmt.Errorf("run_id is required")
	}
	if !allowPublish {
		return workflowledger.DeliverResult{RunID: runID, Refused: true, Reason: "delivery requires allow_publish=true"}, nil
	}
	var stdout, stderr strings.Builder
	// Read the run status BEFORE delivery: an idempotent re-deliver of an
	// already-succeeded run must not re-publish the terminal event.
	preStatus := workflowledger.RunStatus("")
	if preRepo, preClose, preErr := openWorkflowReportContext(e.root, e.configPath); preErr == nil {
		if pre, getErr := preRepo.GetRun(ctx, runID); getErr == nil {
			preStatus = pre.Status
		}
		preClose()
	}
	// If the run is already in delivery_pending, an active session goroutine may
	// be driving its stack under the per-run execution flock. Wait for that
	// goroutine to finish (and release the flock) before contending for the lock,
	// so Deliver does not fail with "lock is busy" while the drive is still in
	// progress. Only delivery_pending is waited on; a running plan run is not
	// blocked because it is not yet at the drive-before-delivery gate.
	if preStatus == workflowledger.RunStatusDeliveryPending {
		e.mu.Lock()
		active := e.active[runID]
		e.mu.Unlock()
		if active != nil {
			select {
			case <-active.done:
			case <-time.After(workflowResolutionLockWait):
			}
		}
	}
	if err := executeWorkflowDeliver(ctx, runID, e.root, e.configPath, allowPublish, false, &stdout, &stderr); err != nil {
		// Prefer structured status when the ledger still opens after a refusal.
		if result, ok := sessionDeliverResultFromLedger(ctx, e.root, e.configPath, runID, err); ok {
			return result, nil
		}
		return workflowledger.DeliverResult{}, err
	}
	repo, closeFn, err := openWorkflowReportContext(e.root, e.configPath)
	if err != nil {
		return workflowledger.DeliverResult{RunID: runID, Status: "unknown"}, nil
	}
	defer closeFn()
	run, getErr := repo.GetRun(ctx, runID)
	if getErr != nil {
		return workflowledger.DeliverResult{RunID: runID, Status: "unknown"}, nil
	}
	result := workflowledger.DeliverResult{RunID: runID, Status: string(run.Status)}
	if rec, recErr := repo.GetDeliveryByIdempotencyKey(ctx, delivery.DeliveryKey(run.RunID, run.WorkflowDigest)); recErr == nil {
		result.URL = rec.URL
		result.Mode = rec.Mode
	}
	// The tool-deliver path settled the run outside the controller: publish
	// the terminal run_finished event the controller would have emitted, so
	// bus consumers observe the delivery completion. Gated on the run having
	// been parked at delivery_pending before this call: a replay of an already
	// delivered run is not a transition and must not re-emit.
	if preStatus == workflowledger.RunStatusDeliveryPending {
		e.publishDeliveredRunFinished(ctx, repo, runID)
	}
	return result, nil
}

// sessionDeliverResultFromLedger maps a refused tool delivery into a
// structured result when the ledger still opens and shows the run settled
// delivery_failed (a host refusal is a settled outcome, not a tool error).
func sessionDeliverResultFromLedger(ctx context.Context, root, configPath, runID string, deliverErr error) (workflowledger.DeliverResult, bool) {
	repo, closeFn, err := openWorkflowReportContext(root, configPath)
	if err != nil {
		return workflowledger.DeliverResult{}, false
	}
	defer closeFn()
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		return workflowledger.DeliverResult{}, false
	}
	// delivery_failed after a host refusal is a settled outcome, not a tool error.
	if run.Status == workflowledger.RunStatusDeliveryFailed {
		return workflowledger.DeliverResult{
			RunID: runID, Status: string(run.Status),
			Refused: true, Reason: deliverErr.Error(),
		}, true
	}
	return workflowledger.DeliverResult{}, false
}
