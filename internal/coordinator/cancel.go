package coordinator

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
)

func (c *coordinator) recordCancellation(ctx context.Context, h *RunHandle, task ledger.TaskSnapshot) error {
	// Fence mailbox on cancel finalize (plan 53.03). Called after the terminal
	// CAS; also covers paths where recordRunResults already left the task
	// canceled and finalize only needs durable attempt/event bookkeeping.
	h.MarkTaskMailboxTerminal(task.TaskID)
	finished := c.nowLocked()
	attemptID := h.getAttempt(task.TaskID)
	if err := c.repo.SetTaskAttempt(ctx, h.runID, task.TaskID, attemptID, string(ledger.TaskStatusCanceled), &finished); err != nil {
		return fmt.Errorf("update canceled attempt %q: %w", task.TaskID, err)
	}
	evt := ledger.LifecycleEvent{
		ID: newEventID(), RunID: h.runID, Kind: "task_" + string(ledger.TaskStatusCanceled),
		TaskID: task.TaskID, AttemptID: attemptID,
	}
	if err := c.repo.AppendEvent(ctx, evt); err != nil {
		return fmt.Errorf("append canceled task %q event: %w", task.TaskID, err)
	}
	c.emitLifecycleEvent(evt)
	return nil
}

// Cancel records a cancel_requested state, cancels the run context, and
// commits terminal canceled only through a valid compare-and-set transition.
func (c *coordinator) Cancel(ctx context.Context, h *RunHandle) error {
	if err := c.validateHandle(h); err != nil {
		return err
	}
	if h.recovered {
		return c.cancelRecoveredWithDeadline(ctx, h)
	}
	h.cancelOnce.Do(func() { go c.reconcileCancellation(h) })
	select {
	case <-h.cancelDone:
		return h.cancelErr()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *RunHandle) setCancelErr(err error) {
	h.mu.Lock()
	h.cancellationErr = err
	h.mu.Unlock()
}

func (h *RunHandle) cancelErr() error {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.cancellationErr
}

func (c *coordinator) reconcileCancellation(h *RunHandle) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var err error
	tasks, listErr := c.repo.ListTasks(ctx, h.runID)
	err = listErr
	if err == nil {
		for _, task := range tasks {
			// awaiting_input is non-terminal (parked on a question); cancel must
			// reach it or the task is left stuck (plan 53.02).
			if task.Status == string(ledger.TaskStatusQueued) ||
				task.Status == string(ledger.TaskStatusRunning) ||
				task.Status == string(ledger.TaskStatusAwaitingInput) {
				if casErr := c.repo.CompareAndSetTaskStatus(ctx, h.runID, task.TaskID, task.Version, string(ledger.TaskStatusCancelRequested)); casErr != nil && casErr != ledger.ErrConflict {
					err = fmt.Errorf("request cancel for %q: %w", task.TaskID, casErr)
					break
				} else if casErr == nil {
					// Emit lifecycle event for cancel_requested transition.
					reqEvt := ledger.LifecycleEvent{
						ID: newEventID(), RunID: h.runID, Kind: "task_cancel_requested",
						TaskID: task.TaskID, AttemptID: h.getAttempt(task.TaskID),
					}
					_ = c.repo.AppendEvent(ctx, reqEvt) // best-effort; state IS persisted via CAS
					c.emitLifecycleEvent(reqEvt)
				}
			}
		}
	}
	h.cancel()
	if err == nil {
		select {
		case <-h.done:
		case <-ctx.Done():
			err = ctx.Err()
		}
	}
	persistCtx, persistCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer persistCancel()
	if finalTasks, listErr := c.repo.ListTasks(persistCtx, h.runID); listErr != nil {
		err = joinError(err, listErr)
	} else {
		for _, task := range finalTasks {
			if task.Status != string(ledger.TaskStatusQueued) && task.Status != string(ledger.TaskStatusCancelRequested) {
				// Still fence mailbox for tasks already terminal elsewhere.
				if IsTaskTerminal(task.Status) {
					h.MarkTaskMailboxTerminal(task.TaskID)
				}
				continue
			}
			casErr := c.repo.CompareAndSetTaskStatus(persistCtx, h.runID, task.TaskID, task.Version, string(ledger.TaskStatusCanceled))
			if casErr != nil {
				if casErr == ledger.ErrConflict {
					// Another path already terminalized; still fence the mailbox.
					h.MarkTaskMailboxTerminal(task.TaskID)
					continue
				}
				err = joinError(err, fmt.Errorf("finalize cancel for %q: %w", task.TaskID, casErr))
				continue
			}
			if cancelErr := c.recordCancellation(persistCtx, h, task); cancelErr != nil {
				err = joinError(err, cancelErr)
			}
		}
	}
	h.setCancelErr(err)
	close(h.cancelDone)
}

func (c *coordinator) cancelRecoveredWithDeadline(ctx context.Context, h *RunHandle) error {
	h.cancelOnce.Do(func() {
		go func() {
			err := c.cancelRecovered(context.Background(), h)
			h.setCancelErr(err)
			close(h.cancelDone)
		}()
	})
	select {
	case <-h.cancelDone:
		return h.cancelErr()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *coordinator) cancelRecovered(ctx context.Context, h *RunHandle) error {
	// A recovered run may still carry a claim row left by the executor that
	// created or resumed it. Every mutation below is claim-fenced
	// (AppendClaimed), so the claim must be probed FIRST with OUR holder:
	// ClaimRun succeeds only when the run is unclaimed or already ours, and
	// returns ErrClaimHeld when another executor holds it.
	//
	// A held claim is REFUSED outright - cancelRecovered never calls
	// ClearRunClaim. The task-status discriminator is unsound: a live
	// executor can hold the claim with all tasks still queued (e.g. during
	// retry backoff), so clearing a held claim would fence a LIVE owner
	// mid-flight - its next fenced append/CAS would fail with ErrClaimHeld.
	// This is deliberately fail-closed: a crashed executor's run whose claim
	// row survives (the process died without Close() releasing it) can no
	// longer be cancel-recovered by a different process until a future
	// liveness/heartbeat mechanism can distinguish a dead claim from a live
	// one.
	//
	// Only a claim that is free or already ours is taken (refreshed), and it
	// is released with our holder at the end.
	if err := c.claimRun(ctx, h.runID); err != nil {
		if errors.Is(err, ledger.ErrClaimHeld) {
			return fmt.Errorf("cannot cancel recovered run %q: execution claim is held by another executor; refusing to clear a possibly live claim", h.runID)
		}
		return fmt.Errorf("claim recovered run %q: %w", h.runID, err)
	}
	defer func() { _ = c.repo.ReleaseRun(context.Background(), h.runID, c.holderID) }()
	tasks, err := c.repo.ListTasks(ctx, h.runID)
	if err != nil {
		return fmt.Errorf("inspect recovered run for cancellation: %w", err)
	}
	for _, task := range tasks {
		switch task.Status {
		case string(ledger.TaskStatusRunning), string(ledger.TaskStatusCancelRequested):
			return fmt.Errorf("cannot cancel recovered run %q: task %q is %s and has no live execution owner", h.runID, task.TaskID, task.Status)
		case string(ledger.TaskStatusQueued):
			// A persisted queued task has no claimed execution attempt and can
			// be canceled durably by this coordinator.
		case string(ledger.TaskStatusCompleted), string(ledger.TaskStatusFailed),
			string(ledger.TaskStatusTimedOut), string(ledger.TaskStatusCanceled),
			string(ledger.TaskStatusBlocked):
			// Terminal tasks require no action.
		default:
			return fmt.Errorf("cannot cancel recovered run %q: task %q has nonterminal state %q that cannot be reconciled", h.runID, task.TaskID, task.Status)
		}
	}
	for _, task := range tasks {
		if task.Status == string(ledger.TaskStatusQueued) && h.getAttempt(task.TaskID) == "" {
			return fmt.Errorf("cannot cancel recovered task %q: persisted attempt is missing", task.TaskID)
		}
	}

	persistCtx, persistCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer persistCancel()
	for _, task := range tasks {
		if task.Status != string(ledger.TaskStatusQueued) {
			continue
		}
		if err := c.repo.CompareAndSetTaskStatus(persistCtx, h.runID, task.TaskID, task.Version, string(ledger.TaskStatusCanceled)); err != nil {
			if err == ledger.ErrConflict {
				return fmt.Errorf("cannot cancel recovered task %q: state changed during reconciliation: %w", task.TaskID, err)
			}
			return fmt.Errorf("cancel recovered task %q: %w", task.TaskID, err)
		}
		if err := c.recordCancellation(persistCtx, h, task); err != nil {
			return err
		}
	}
	return nil
}
