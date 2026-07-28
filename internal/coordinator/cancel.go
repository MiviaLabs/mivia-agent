package coordinator

import (
	"context"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
)

func (c *Coordinator) recordCancellation(ctx context.Context, h *RunHandle, task ledger.TaskSnapshot) error {
	finished := c.now()
	attemptID := h.attempts[task.TaskID]
	if err := c.repo.SetTaskAttempt(ctx, h.runID, task.TaskID, attemptID, string(ledger.TaskStatusCanceled), &finished); err != nil {
		return fmt.Errorf("update canceled attempt %q: %w", task.TaskID, err)
	}
	if err := c.repo.AppendEvent(ctx, ledger.LifecycleEvent{
		ID: newEventID(), RunID: h.runID, Kind: "task_" + string(ledger.TaskStatusCanceled),
		TaskID: task.TaskID, AttemptID: attemptID,
	}); err != nil {
		return fmt.Errorf("append canceled task %q event: %w", task.TaskID, err)
	}
	return nil
}

// Cancel records a cancel_requested state, cancels the run context, and
// commits terminal canceled only through a valid compare-and-set transition.
func (c *Coordinator) Cancel(ctx context.Context, h *RunHandle) error {
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

func (c *Coordinator) reconcileCancellation(h *RunHandle) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var err error
	tasks, listErr := c.repo.ListTasks(ctx, h.runID)
	err = listErr
	if err == nil {
		for _, task := range tasks {
			if task.Status == string(ledger.TaskStatusQueued) || task.Status == string(ledger.TaskStatusRunning) {
				if casErr := c.repo.CompareAndSetTaskStatus(ctx, h.runID, task.TaskID, task.Version, string(ledger.TaskStatusCancelRequested)); casErr != nil && casErr != ledger.ErrConflict {
					err = fmt.Errorf("request cancel for %q: %w", task.TaskID, casErr)
					break
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
				continue
			}
			casErr := c.repo.CompareAndSetTaskStatus(persistCtx, h.runID, task.TaskID, task.Version, string(ledger.TaskStatusCanceled))
			if casErr != nil {
				if casErr == ledger.ErrConflict {
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

func (c *Coordinator) cancelRecoveredWithDeadline(ctx context.Context, h *RunHandle) error {
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

func (c *Coordinator) cancelRecovered(ctx context.Context, h *RunHandle) error {
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
		if task.Status == string(ledger.TaskStatusQueued) && h.attempts[task.TaskID] == "" {
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
				return fmt.Errorf("cannot cancel recovered task %q: state changed during reconciliation", task.TaskID)
			}
			return fmt.Errorf("cancel recovered task %q: %w", task.TaskID, err)
		}
		if err := c.recordCancellation(persistCtx, h, task); err != nil {
			return err
		}
	}
	return nil
}
