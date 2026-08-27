package cliworkflow

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

var (
	// workflowResumeClaimRefreshTimeout bounds one handoff-claim refresh so a
	// wedged store call cannot deadlock the heartbeat's stop path.
	workflowResumeClaimRefreshTimeout = 5 * time.Second
	// workflowResumeClaimHeartbeatInterval refreshes the pre-Run handoff claim
	// while joinInFlightAttempts runs. The join can outlast DefaultClaimLease,
	// so the claim must be kept alive until the controller's own Advance
	// heartbeat takes over. Matches the controller heartbeat cadence
	// (DefaultClaimLease / 3). Injectable for tests.
	workflowResumeClaimHeartbeatInterval = workflowledger.DefaultClaimLease / 3
	// workflowResumeClaimHeartbeatHook is called after each successful handoff
	// claim heartbeat refresh. It exists only for deterministic tests.
	workflowResumeClaimHeartbeatHook func()
)

// errResumeHandoffClaimLost is the cancellation cause the handoff heartbeat
// sets when another executor takes the run's claim mid-join. joinInFlightAttempts
// surfaces it instead of the generic join-bound message so the operator can
// tell a claim loss from a slow child.
var errResumeHandoffClaimLost = errors.New("resume handoff claim lost")

// prepareWorkflowResumeExecution handles the shared resume handoff. It claims
// the run with the final controller holder before it joins recorded children,
// and heartbeats that handoff claim during the join so the claim lease never
// expires before the controller's own Advance heartbeat takes over.
func prepareWorkflowResumeExecution(ctx context.Context, built WorkflowControllerBuild, repo workflowledger.Repository, runID string, force bool, stdout io.Writer) error {
	if built.Controller == nil {
		return joinInFlightAttempts(ctx, built, repo, runID, stdout)
	}
	if err := claimWorkflowResumeHandoff(ctx, repo, runID, built.Controller.Holder, force); err != nil {
		return fmt.Errorf("claim workflow resume handoff: %w", err)
	}
	joinCtx, stopHeartbeat := startWorkflowResumeHandoffHeartbeat(ctx, repo, runID, built.Controller.Holder)
	err := joinInFlightAttempts(joinCtx, built, repo, runID, stdout)
	stopHeartbeat()
	if err != nil {
		_ = repo.ReleaseRun(context.Background(), runID, built.Controller.Holder)
		return err
	}
	return nil
}

// claimWorkflowResumeHandoff acquires the final controller claim without an
// unowned clear-and-claim window. --force is the operator override for crash
// recovery: it replaces the claim atomically (TakeoverRunClaim), so a fresh
// claim left by a killed executor does not brick resume until lease expiry. A
// forced takeover is still fenced: the displaced holder's fenced writes fail
// with ErrClaimHeld after the takeover. Without force, only an expired claim
// is taken over; a fresh claim within its lease belongs to a live holder and
// is refused.
func claimWorkflowResumeHandoff(ctx context.Context, repo workflowledger.Repository, runID, holder string, force bool) error {
	if force {
		return repo.TakeoverRunClaim(ctx, runID, holder)
	}
	err := repo.TakeoverExpiredRunClaim(ctx, runID, holder, workflowledger.DefaultClaimLease)
	if errors.Is(err, workflowledger.ErrClaimNotHeld) {
		err = repo.ClaimRun(ctx, runID, holder)
	}
	if errors.Is(err, workflowledger.ErrClaimHeld) {
		return fmt.Errorf("workflow run %q is still active; retry after the claim lease expires or pass --force after the prior executor stopped", runID)
	}
	return err
}

// startWorkflowResumeHandoffHeartbeat keeps the pre-Run handoff claim alive
// while joinInFlightAttempts runs. The join can outlast DefaultClaimLease, so
// the claim must be refreshed until the controller's own Advance heartbeat
// takes over. It returns a derived context that is cancelled with
// errResumeHandoffClaimLost as the cause when the claim is lost (another
// holder takes it), and a stop function that must be called when the join
// finishes.
func startWorkflowResumeHandoffHeartbeat(parent context.Context, repo workflowledger.Repository, runID, holder string) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(parent)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(workflowResumeClaimHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				// Bound the best-effort refresh so a wedged store call cannot
				// deadlock the stop function waiting on this goroutine.
				refreshCtx, release := context.WithTimeout(context.Background(), workflowResumeClaimRefreshTimeout)
				err := repo.RefreshRunClaim(refreshCtx, runID, holder)
				release()
				if err != nil {
					if errors.Is(err, workflowledger.ErrClaimHeld) || errors.Is(err, workflowledger.ErrClaimNotHeld) {
						cancel(fmt.Errorf("%w: run %s is now owned by another executor; retry resume after it stops", errResumeHandoffClaimLost, runID))
						return
					}
					log.Printf("workflow: resume handoff heartbeat for run %s failed (continuing): %v", runID, err)
				} else if workflowResumeClaimHeartbeatHook != nil {
					workflowResumeClaimHeartbeatHook()
				}
			case <-stop:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return ctx, func() {
		close(stop)
		<-done
		cancel(nil)
	}
}

// releaseWorkflowResumeHandoff clears a preflight claim when controller startup
// or execution returns before Advance releases its own claim.
func releaseWorkflowResumeHandoff(repo workflowledger.Repository, runID string, controller *controller.LinearController) {
	if controller != nil {
		_ = repo.ReleaseRun(context.Background(), runID, controller.Holder)
	}
}

// joinInFlightAttempts consumes PlanResume.AttemptsInFlight: it joins each
// recorded in-flight attempt's coordinator run through the controller BEFORE
// the Run loop starts, so a completed (or failed) child settles the attempt
// with its outcome and route instead of being orphaned by a fresh re-dispatch
// (recovery.go: a recorded attempt is joined, never re-dispatched). Attempts
// whose child never ran are left in-flight for the controller's Advance to
// interrupt and re-dispatch under the run claim. The join is idempotent:
// attempts already terminal (or superseded) are no-ops. On failure the run's
// settled status is reported to stdout before the error is returned.
func joinInFlightAttempts(ctx context.Context, built WorkflowControllerBuild, repo workflowledger.Repository, runID string, stdout io.Writer) (err error) {
	defer func() {
		if err != nil {
			if settled, getErr := repo.GetRun(ctx, runID); getErr == nil {
				fmt.Fprintf(stdout, "run_id=%s status=%s\n", runID, settled.Status)
			}
		}
	}()
	plan, err := workflowledger.PlanResume(ctx, repo, runID)
	if err != nil {
		return err
	}
	if len(plan.AttemptsInFlight) == 0 {
		return nil
	}
	if built.Controller == nil {
		return fmt.Errorf("workflow controller is nil; cannot join %d in-flight attempt(s)", len(plan.AttemptsInFlight))
	}
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	// The join is bounded to the run's own deadline when present (time.Until
	// it) so a child whose coordinator run never settles cannot park resume
	// past the run's own configured limit. A run with NO configured deadline
	// (max_duration_seconds=0, "unlimited" by explicit operator choice) gets
	// an unbounded join context here: joinWithCancellation's own
	// controller-side watchdog (defaultJoinWatchdog, liveness-gated - it
	// cancels only when the child goes SILENT past the bound, never merely
	// because wall-clock time has passed) already protects against a
	// genuinely stuck/orphaned child, so this context does not need its own
	// redundant wall-clock cap. Before this fix, a fixed workflowResumeJoinBound
	// substituted here for "no deadline" and was passed all the way into the
	// live join: it fired at a fixed wall-clock mark even while the child was
	// actively heartbeating, and joinWithCancellation (agent_step_join.go)
	// canceled the still-progressing coordinator task and let the canceled
	// outcome settle as a genuine result - not "leave it in-flight for
	// reconciliation" as documented, but a permanent run_status=timed_out on
	// work that was never stalled (reproduced live on wfr-HHR2BWDUAK2PETK7:
	// steady 30s heartbeats for ~9.5m, then run_failed "context deadline
	// exceeded", status=timed_out).
	joinCtx, cancelJoin := workflowResumeJoinCtx(ctx, run)
	defer cancelJoin()
	for _, inFlight := range plan.AttemptsInFlight {
		if err := built.Controller.JoinInFlightAttempt(joinCtx, inFlight); err != nil {
			if cause := context.Cause(joinCtx); errors.Is(cause, errResumeHandoffClaimLost) {
				return fmt.Errorf("join in-flight attempt %s for step %q: %w", inFlight.AttemptID, inFlight.StepID, cause)
			}
			return fmt.Errorf("join in-flight attempt %s for step %q: %w", inFlight.AttemptID, inFlight.StepID, err)
		}
		if joinCtx.Err() != nil {
			if cause := context.Cause(joinCtx); errors.Is(cause, errResumeHandoffClaimLost) {
				return fmt.Errorf("join in-flight attempt %s for step %q: %w", inFlight.AttemptID, inFlight.StepID, cause)
			}
			return fmt.Errorf("join in-flight attempt %s for step %q did not settle within the resume join bound; leaving it in-flight for controller reconciliation: %w", inFlight.AttemptID, inFlight.StepID, joinCtx.Err())
		}
	}
	return nil
}

// workflowResumeJoinCtx derives the context for the pre-Run in-flight attempt
// join: the run's own deadline when present (time.Until it), so the join
// never outlives a deadline the operator actually configured. An
// already-expired deadline yields an immediately-expired context so the join
// fails fast instead of hanging. A run with no configured deadline
// (max_duration_seconds=0, unlimited) gets an unbounded context - see the
// call site's comment for why that is safe (the controller's own
// liveness-gated join watchdog still catches a genuinely stuck child).
func workflowResumeJoinCtx(parent context.Context, run workflowledger.RunSnapshot) (context.Context, context.CancelFunc) {
	if run.DeadlineAt != nil {
		return context.WithDeadline(parent, *run.DeadlineAt)
	}
	return context.WithCancel(parent)
}
