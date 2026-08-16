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

// joinWatchdogTickInterval returns the heartbeat poll interval for the join
// watchdog: bound/8 capped at 30 seconds with a 100 ms floor. The floor
// guards tiny bounds in tests so the ticker never runs faster than 100 ms.
func joinWatchdogTickInterval(bound time.Duration) time.Duration {
	interval := bound / 8
	if interval > 30*time.Second {
		interval = 30 * time.Second
	}
	if interval < 100*time.Millisecond {
		interval = 100 * time.Millisecond
	}
	return interval
}

// watchJoinLiveness runs the liveness-gated watchdog for one coordinator
// join. The watchdog cancels the join when the child task is silent for
// longer than the bound. The join start is the reference: a recorded
// heartbeat counts only when it is newer than the join start, so a stale
// pre-join entry from an earlier execution of the same task id can never
// cancel a live re-dispatched child early. A child silent since the join
// start is canceled at the bound. Each tick reports a step heartbeat to
// the optional emitter while the join is still live. The returned stop
// function ends the ticker and BLOCKS until the watchdog goroutine has
// exited, so a heartbeat emit already in flight when stop is called can
// never land after the caller (joinWithCancellation) returns and the run
// settles — mirrors startDurableHeartbeatTicker's join-on-stop contract.
func watchJoinLiveness(joinCtx context.Context, cancel context.CancelFunc, taskID string, bound time.Duration, emit func(ProgressEvent)) (stop func()) {
	anchor := time.Now()
	ticker := time.NewTicker(joinWatchdogTickInterval(bound))
	done := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		defer ticker.Stop()
		for {
			select {
			case <-joinCtx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				if emit != nil {
					emit(ProgressEvent{Kind: ProgressStepHeartbeat, Detail: "running"})
				}
				last, ok := LastStepHeartbeat(taskID)
				reference := anchor
				if ok && last.After(anchor) {
					reference = last
				}
				if time.Since(reference) > bound {
					cancel()
					return
				}
			}
		}
	}()
	return func() {
		close(done)
		<-exited
	}
}

// joinWithCancellation joins the child run bounded by the controller-side
// watchdog: min(remaining step/run deadline, task timeout when set, fixed
// watchdog). The watchdog is liveness-gated: a live child that emits
// subagent heartbeats is never canceled by the watchdog; only a child
// silent for the full bound is canceled. Each watchdog tick reports a
// ProgressStepHeartbeat for the step to the optional emitter, so a still
// running join stays observable. On expiry the child is canceled and the
// canceled wait is given a short settle window, exactly as for parent-ctx
// expiry; when the join bound (not the parent ctx) fired, the error names
// the join timeout so the attempt settles timed_out/failed instead of the
// controller blocking indefinitely.
func (r *CoordinatorRunner) joinWithCancellation(ctx context.Context, spec AgentStepRequest, h *coordinator.RunHandle, emitters ...func(ProgressEvent)) (*coordinator.RunResult, error) {
	watchdog := r.joinWatchdog()
	bound := joinBound(ctx, spec.Timeout, watchdog)
	joinCtx, joinCancel := context.WithCancel(ctx)
	defer joinCancel()
	var emit func(ProgressEvent)
	if len(emitters) > 0 {
		capture := emitters[0]
		emit = func(e ProgressEvent) {
			if capture == nil {
				return
			}
			e.StepID = spec.StepID
			e.AttemptNo = spec.AttemptNo
			e.TaskID = spec.TaskID
			e.CoordinatorRunID = spec.CoordinatorRunID
			capture(e)
		}
	}
	stop := watchJoinLiveness(joinCtx, joinCancel, spec.TaskID, bound, emit)
	defer stop()
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
