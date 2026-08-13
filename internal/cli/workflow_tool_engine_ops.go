package cli

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// Cancel implements agenttools.Engine.
// It stops any in-process controller first, then settles via the same
// execution-lock + CancelRun path as `mivia workflow cancel`. A terminal or
// delivery_pending run is resolved BEFORE any claim mutation: a refused cancel
// must never strip a live delivery claim.
func (e *sessionWorkflowEngine) Cancel(ctx context.Context, runID string) (agenttools.CancelResult, error) {
	if e == nil {
		return agenttools.CancelResult{}, fmt.Errorf("workflow engine is nil")
	}
	if strings.TrimSpace(runID) == "" {
		return agenttools.CancelResult{}, fmt.Errorf("run_id is required")
	}
	e.mu.Lock()
	active := e.active[runID]
	e.mu.Unlock()
	e.stopActive(ctx, runID)
	releaseExecution, repo, store, closeFn, err := openWorkflowResolutionContextBounded(e.root, e.configPath, runID, workflowResolutionLockWait)
	if err != nil {
		return agenttools.CancelResult{}, err
	}
	defer closeFn()
	defer releaseExecution()
	// Resolve the run BEFORE any claim mutation: a delivery_pending run may be
	// mid-publish under a live claim (this or another host), and a refused or
	// idempotent cancel must leave claims untouched.
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		return agenttools.CancelResult{}, err
	}
	if workflowledger.IsTerminalRunStatus(run.Status) {
		// Idempotent operator retry: the run is already settled; no claim work.
		return agenttools.CancelResult{RunID: runID, Status: string(run.Status)}, nil
	}
	if run.Status == workflowledger.RunStatusDeliveryPending {
		return agenttools.CancelResult{}, fmt.Errorf("run %q is waiting for delivery; deliver it or leave it for cleanup before cancel", runID)
	}
	// Never clear a held claim: cancel accepts any run_id, and a blind clear
	// would strip a live delivery claim (held by this or another host mid-
	// publish) and enable double-publish. Claim instead; an expired lease may
	// be taken over with the exclusion fence still armed, but a fresh foreign
	// claim is refused outright.
	holder := newWorkflowCancelHolder()
	if err := claimForCancel(ctx, repo, runID, holder); err != nil {
		return agenttools.CancelResult{}, err
	}
	defer func() { _ = repo.ReleaseRun(context.Background(), runID, holder) }()
	attempts, err := cancelRunWithGuardedCoordinator(ctx, active, repo, store, runID, holder)
	if err != nil {
		// Context cancel or a prior settle may already leave the run terminal.
		run, getErr := repo.GetRun(ctx, runID)
		if getErr == nil && workflowledger.IsTerminalRunStatus(run.Status) {
			return agenttools.CancelResult{RunID: runID, Status: string(run.Status)}, nil
		}
		return agenttools.CancelResult{}, err
	}
	// Terminal progress: one step_completed(canceled) per attempt the cancel
	// settled, so TUI and metrics observe the operator cancel like any other
	// run terminal event.
	e.publishCanceledAttempts(runID, attempts)
	run, err = repo.GetRun(ctx, runID)
	if err != nil {
		return agenttools.CancelResult{}, err
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
	return agenttools.CancelResult{RunID: runID, Status: string(run.Status)}, nil
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

// Delete implements agenttools.Engine.
// It removes a run from the durable ledger via the same execution-lock +
// claim path as `mivia workflow delete`. Settled runs (terminal or
// delivery_pending) are always deletable; force also permits a non-terminal
// run (pending/running/waiting_approval) — the crash-recovery override for a
// run stranded by a dead executor. The status gate resolves BEFORE any claim
// mutation: a delivery_pending run may be mid-publish under a live claim (this
// or another host), and a refused delete must leave claims untouched. A fresh
// claim held by a live executor is refused even with force; only an expired
// lease is taken over, so an actively executing run can never be deleted.
func (e *sessionWorkflowEngine) Delete(ctx context.Context, runID string, force bool) (agenttools.DeleteResult, error) {
	if e == nil {
		return agenttools.DeleteResult{}, fmt.Errorf("workflow engine is nil")
	}
	if strings.TrimSpace(runID) == "" {
		return agenttools.DeleteResult{}, fmt.Errorf("run_id is required")
	}
	releaseExecution, repo, _, closeFn, err := openWorkflowResolutionContextBounded(e.root, e.configPath, runID, workflowResolutionLockWait)
	if err != nil {
		return agenttools.DeleteResult{}, err
	}
	defer closeFn()
	defer releaseExecution()
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		return agenttools.DeleteResult{}, err
	}
	if !workflowledger.IsDeletableRunStatus(run.Status) && !force {
		return agenttools.DeleteResult{}, fmt.Errorf("run %q is %q; cancel it before delete, or pass force only after the prior executor stopped (a live claim is still refused)", runID, run.Status)
	}
	// Never clear a held claim: delete accepts any run_id, and a blind clear
	// would strip a live delivery claim (held by this or another host mid-
	// publish) and enable double-publish. Claim instead; an expired lease may
	// be taken over, a fresh foreign claim is refused outright.
	holder := newWorkflowDeleteHolder()
	if err := claimWorkflowOperator(ctx, repo, runID, holder); err != nil {
		return agenttools.DeleteResult{}, fmt.Errorf("workflow run %q is claimed by another executor; delete refused", runID)
	}
	ctx = workflowledger.ContextWithClaimHolder(ctx, holder)
	if err := repo.DeleteRun(ctx, runID); err != nil {
		return agenttools.DeleteResult{}, err
	}
	return agenttools.DeleteResult{RunID: runID, Status: string(run.Status), Deleted: true}, nil
}

// Deliver implements agenttools.Engine.
// Publication uses the CLI deliver path (run-owned worktree + execution lock).
// It never delivers from the caller workspace root via localengine.
func (e *sessionWorkflowEngine) Deliver(ctx context.Context, runID string, allowPublish bool) (agenttools.DeliverResult, error) {
	if e == nil {
		return agenttools.DeliverResult{}, fmt.Errorf("workflow engine is nil")
	}
	if strings.TrimSpace(runID) == "" {
		return agenttools.DeliverResult{}, fmt.Errorf("run_id is required")
	}
	if !allowPublish {
		return agenttools.DeliverResult{RunID: runID, Refused: true, Reason: "delivery requires allow_publish=true"}, nil
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
	if err := executeWorkflowDeliver(ctx, runID, e.root, e.configPath, allowPublish, false, &stdout, &stderr); err != nil {
		// Prefer structured status when the ledger still opens after a refusal.
		if result, ok := sessionDeliverResultFromLedger(ctx, e.root, e.configPath, runID, err); ok {
			return result, nil
		}
		return agenttools.DeliverResult{}, err
	}
	repo, closeFn, err := openWorkflowReportContext(e.root, e.configPath)
	if err != nil {
		return agenttools.DeliverResult{RunID: runID, Status: "unknown"}, nil
	}
	defer closeFn()
	run, getErr := repo.GetRun(ctx, runID)
	if getErr != nil {
		return agenttools.DeliverResult{RunID: runID, Status: "unknown"}, nil
	}
	result := agenttools.DeliverResult{RunID: runID, Status: string(run.Status)}
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
func sessionDeliverResultFromLedger(ctx context.Context, root, configPath, runID string, deliverErr error) (agenttools.DeliverResult, bool) {
	repo, closeFn, err := openWorkflowReportContext(root, configPath)
	if err != nil {
		return agenttools.DeliverResult{}, false
	}
	defer closeFn()
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		return agenttools.DeliverResult{}, false
	}
	// delivery_failed after a host refusal is a settled outcome, not a tool error.
	if run.Status == workflowledger.RunStatusDeliveryFailed {
		return agenttools.DeliverResult{
			RunID: runID, Status: string(run.Status),
			Refused: true, Reason: deliverErr.Error(),
		}, true
	}
	return agenttools.DeliverResult{}, false
}
