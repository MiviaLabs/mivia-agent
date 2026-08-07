package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
)

// defaultJoinWatchdog bounds a coordinator join from the controller side when
// neither the step/run context deadline nor the task timeout is earlier. The
// coordinator's own Join (internal/coordinator/coordinator.go) waits on the
// child run's done channel with no bound of its own; a child that never
// settles (hung pool worker, stuck referral wait, dead executor) would
// otherwise park the controller forever: no wf_attempt_completed, no
// re-dispatch, no failure transition — the run stays 'running' at the current
// attempt until canceled.
const defaultJoinWatchdog = 10 * time.Minute

// joinWatchdog returns the effective controller-side join bound.
func (r *CoordinatorRunner) joinWatchdog() time.Duration {
	if r != nil && r.JoinWatchdog > 0 {
		return r.JoinWatchdog
	}
	return defaultJoinWatchdog
}

// joinBound returns the controller-side bound for one coordinator join: the
// earliest of the remaining step/run deadline (parent ctx), the task timeout
// (spec.Timeout, when set), and the fixed join watchdog. The parent deadline
// can arrive first even when the step has its own timeout because the run-loop
// ctx carries the run deadline (linear.go), so all three are raced together.
func joinBound(ctx context.Context, taskTimeout, watchdog time.Duration) time.Duration {
	bound := watchdog
	if taskTimeout > 0 && taskTimeout < bound {
		bound = taskTimeout
	}
	if parent, ok := ctx.Deadline(); ok {
		if remaining := time.Until(parent); remaining < bound {
			bound = remaining
		}
	}
	if bound <= 0 {
		return watchdog
	}
	return bound
}

// joinWithCancellation joins the child run bounded by the controller-side
// watchdog: min(remaining step/run deadline, task timeout when set, fixed
// watchdog). On expiry the child is canceled and the canceled wait is given a
// short settle window, exactly as for parent-ctx expiry; when the join bound
// (not the parent ctx) fired, the error names the join timeout so the attempt
// settles timed_out/failed instead of the controller blocking indefinitely.
func (r *CoordinatorRunner) joinWithCancellation(ctx context.Context, spec AgentStepRequest, h *coordinator.RunHandle) (*coordinator.RunResult, error) {
	watchdog := r.joinWatchdog()
	bound := joinBound(ctx, spec.Timeout, watchdog)
	joinCtx, joinCancel := context.WithTimeout(ctx, bound)
	defer joinCancel()
	result, err := r.Coordinator.Join(joinCtx, h)
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return result, err
	}
	if ctx.Err() == nil {
		// The join bound (task timeout or controller-side watchdog) fired
		// before the parent step/run deadline: the child never settled in
		// time. Report it clearly so the attempt settles timed_out with an
		// error naming the join bound instead of a bare parent cancel.
		err = fmt.Errorf("workflow step %q join timed out after %s (child never settled): %w", spec.StepID, bound.Round(time.Millisecond), context.DeadlineExceeded)
	}
	_ = r.Coordinator.Cancel(context.Background(), h)
	cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// The canceled wait may still settle with the child outcome; keep it so
	// the caller records output and status instead of a bare failure.
	if settled, settleErr := r.Coordinator.Join(cleanup, h); settleErr == nil && settled != nil {
		return settled, err
	}
	return result, err
}
