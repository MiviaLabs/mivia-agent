package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
)

// defaultJoinWatchdog bounds a coordinator join from the controller side ONLY
// when neither the step/run context deadline nor the task timeout bounds the
// join. The coordinator's own Join (internal/coordinator/coordinator.go) waits
// on the child run's done channel with no bound of its own; a child that never
// settles with no timeout and no deadline (hung pool worker, stuck referral
// wait, dead executor) would otherwise park the controller forever: no
// wf_attempt_completed, no re-dispatch, no failure transition — the run stays
// 'running' at the current attempt until canceled. The watchdog is a
// last-resort bound for that case only: it must NOT truncate a longer task
// timeout or run deadline (workflow runs are designed to host steps up to the
// full run deadline, e.g. 24h).
const defaultJoinWatchdog = 10 * time.Minute

// joinWatchdog returns the effective controller-side join bound.
func (r *CoordinatorRunner) joinWatchdog() time.Duration {
	if r != nil && r.JoinWatchdog > 0 {
		return r.JoinWatchdog
	}
	return defaultJoinWatchdog
}

// joinBound returns the controller-side bound for one coordinator join: the
// earliest of the remaining step/run deadline (parent ctx) and the task
// timeout (spec.Timeout, when set). The parent deadline can arrive first even
// when the step has its own timeout because the run-loop ctx carries the run
// deadline (linear.go). The fixed join watchdog applies ONLY when neither
// bounds the join (no task timeout, no parent deadline): a long task timeout
// (e.g. one derived from a 24h run deadline) must be honored, never truncated
// by the watchdog.
func joinBound(ctx context.Context, taskTimeout, watchdog time.Duration) time.Duration {
	bound := time.Duration(0)
	if taskTimeout > 0 {
		bound = taskTimeout
	}
	if parent, ok := ctx.Deadline(); ok {
		remaining := time.Until(parent)
		if remaining < bound || bound <= 0 {
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
