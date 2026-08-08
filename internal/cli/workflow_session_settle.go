package cli

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// sessionSettleTimeout bounds the settle write. The controller has already
// stopped, so this must not hold the session open.
const sessionSettleTimeout = 5 * time.Second

// settleCLIRunFailure records why a CLI-driven workflow run stopped, for
// errors the controller did not already settle. Controller.Run self-settles
// deadline errors (timed_out) and cancel owns cancelled runs, so only genuine
// non-deadline failures need this settle. It is a no-op for nil, cancelled,
// and deadline errors.
func settleCLIRunFailure(repo workflowledger.Repository, runID string, runErr error) {
	if runErr == nil || errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) || isNonTerminalWorkflowStop(runErr) {
		return
	}
	settleSessionRunFailure(repo, runID, runErr)
}

func isNonTerminalWorkflowStop(err error) bool {
	return errors.Is(err, controller.ErrPanelMembersComplete)
}

// settleSessionRunFailure records why a session-driven run stopped, and gives
// it a terminal status when nothing else will.
//
// The controller returns its cause from Run, but the session engine read that
// cause only to decide whether to deliver, then dropped it. The run row stayed
// `running` with no explanation anywhere: it looked alive and was not. The
// local engine already answers this, in settleRunFailure, and the two engines
// simply disagreed.
//
// This is that same answer, with the same carve-outs:
//   - A cancelled run is left alone. Cancel settles the run itself, and a
//     failed status written here would race it and win.
//   - A run another holder owns is left alone. That holder is the live
//     executor.
//   - A run that already reached a terminal status is left alone.
//   - A run parked at delivery_pending, pending, or waiting_approval is left
//     alone. None of them is mid-flight running: a parked approval must stay
//     approvable, and a delivery-pending run belongs to the delivery path.
//
// For a run that IS mid-flight, the settle consults the ledger's recovery plan
// instead of failing blindly. The controller persists the ROUTE (the
// completion's ToStepID) before the run-status CAS, so a transient storage
// fault between the two leaves a COMPLETED run with derived ActiveStepID
// "success"/"failure" and Status "running". PlanResume detects that derived
// terminal route, and the settle records the plan's terminal status
// (succeeded/failed) rather than failing a finished run — failing it would
// block delivery of work that is already done.
//
// A storage fault that stops the controller therefore settles as failed rather
// than stranding — unless the run already finished, in which case it settles
// as succeeded. The work is not lost: every completed step stays durable in
// the ledger, and the operator can see the cause instead of guessing why a
// `running` run stopped moving.
func settleSessionRunFailure(repo workflowledger.Repository, runID string, runErr error) {
	if runErr == nil || errors.Is(runErr, context.Canceled) || isNonTerminalWorkflowStop(runErr) {
		return
	}
	log.Printf("workflow: run %s stopped with error: %v", runID, runErr)
	ctx, cancel := context.WithTimeout(context.Background(), sessionSettleTimeout)
	defer cancel()
	holder := "wfsettle-" + newCLIWorkflowRunID()
	if err := repo.ClaimRun(ctx, runID, holder); err != nil {
		return // another holder owns the run and will settle or continue it
	}
	defer func() { _ = repo.ReleaseRun(context.Background(), runID, holder) }()
	fresh, err := repo.GetRun(ctx, runID)
	if err != nil || workflowledger.IsTerminalRunStatus(fresh.Status) {
		return
	}
	// Not mid-flight running: never fail these. A parked approval must stay
	// approvable; a delivery-pending run belongs to the delivery path (a
	// delivery_pending->delivery_pending CAS would also be an invalid edge);
	// a pending run has not started.
	switch fresh.Status {
	case workflowledger.RunStatusDeliveryPending,
		workflowledger.RunStatusPending,
		workflowledger.RunStatusWaitingApproval:
		return
	}
	plan, planErr := workflowledger.PlanResume(ctx, repo, runID)
	if planErr != nil {
		// PlanResume is a pure read; if it cannot be derived, fall back to the
		// historical behaviour: a storage fault settles as failed rather than
		// stranding a `running` run.
		log.Printf("workflow: run %s: derive recovery plan: %v; settling failed", runID, planErr)
		_ = repo.CompareAndSetRunStatus(ctx, runID, fresh.Version, workflowledger.RunStatusFailed, nil)
		return
	}
	if plan.Terminal {
		// The run already reached its terminal route (derived from the durable
		// attempt log) even though the status CAS was never recorded. Record
		// the plan's terminal status with the version from the FRESH snapshot
		// read under the claim — never a stale one. Skip the same-status no-op
		// (a delivery_pending->delivery_pending CAS is not a valid edge).
		target := plan.TerminalStatus
		if plan.TerminalStatus == workflowledger.RunStatusSucceeded {
			// PlanResume is a pure ledger read and cannot see the delivery
			// policy. A delivery-active run whose success route is durable but
			// whose delivery_pending CAS was missed must settle at
			// delivery_pending, never succeeded: succeeded only exists after
			// publication, so settling there would make `workflow deliver`
			// replay a missing record and the PR would never be published. The
			// controller settles the same route to delivery_pending when
			// delivery is required (reconcileTerminalRoute), and the resume
			// path converts plan succeeded->delivery_pending for
			// delivery-active workflows; this closes the same crash window at
			// the settle. If the snapshot/compile derivation fails (e.g. a
			// transient storage fault), fall back to the plan's terminal
			// status rather than stranding the run — PlanResume already read
			// the ledger successfully, so a snapshot read usually succeeds.
			target = settleSessionRunFailureDeliveryAwareTarget(ctx, repo, runID, fresh)
		}
		if target != fresh.Status {
			_ = repo.CompareAndSetRunStatus(ctx, runID, fresh.Version, target, nil)
		}
		return
	}
	// Genuinely mid-flight: nothing else will settle it.
	_ = repo.CompareAndSetRunStatus(ctx, runID, fresh.Version, workflowledger.RunStatusFailed, nil)
}

// settleSessionRunFailureDeliveryAwareTarget derives whether the run's
// durable admission snapshot carries an active delivery policy and returns
// the status the settle should record for a plan-succeeded terminal route:
// delivery_pending when delivery is active, succeeded otherwise. A failed
// derivation (e.g. a transient storage fault on the snapshot read) falls back
// to succeeded — the plan's terminal status — so the settle never strands a
// run whose route already finished it.
func settleSessionRunFailureDeliveryAwareTarget(ctx context.Context, repo workflowledger.Repository, runID string, run workflowledger.RunSnapshot) workflowledger.RunStatus {
	raw, err := repo.GetRunSnapshot(ctx, runID)
	if err != nil {
		log.Printf("workflow: run %s: read admission snapshot: %v; settling succeeded", runID, err)
		return workflowledger.RunStatusSucceeded
	}
	_, compiled, _, err := validateWorkflowResumeSnapshot(run, raw)
	if err != nil {
		log.Printf("workflow: run %s: derive delivery policy from snapshot: %v; settling succeeded", runID, err)
		return workflowledger.RunStatusSucceeded
	}
	if compiled.DeliveryActive() {
		return workflowledger.RunStatusDeliveryPending
	}
	return workflowledger.RunStatusSucceeded
}
