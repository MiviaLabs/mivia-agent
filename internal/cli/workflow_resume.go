package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

var (
	workflowResumeOpenStore    = openWorkflowStore
	workflowResumeInstallHooks = installHookSession
	workflowResumeBuild        = buildWorkflowController
	workflowResumeSetAdmission = func(b workflowControllerBuild) error {
		return b.Controller.SetAdmission(b.Admission)
	}
	workflowResumeSetForce = func(b workflowControllerBuild) error {
		return b.Controller.SetForceResume(true)
	}
	workflowResumeRun = func(ctx context.Context, b workflowControllerBuild) (workflowledger.RunSnapshot, error) {
		return b.Controller.Run(ctx)
	}
	// workflowResumeJoinBound bounds the pre-Run in-flight attempt join for runs
	// that carry no deadline of their own, so a coordinator child whose run never
	// settles cannot park resume forever (runs WITH a deadline bound the join to
	// the time remaining before it; see workflowResumeJoinCtx). Injectable for tests.
	workflowResumeJoinBound = 10 * time.Minute
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

func executeWorkflowResume(runID, root, configPath string, force, allowPublish, acceptVerifierChange, acceptSkillChange bool, stdout, stderr io.Writer) error {
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	work, err := workspace.Open(root)
	if err != nil {
		return err
	}
	configPath = workflowConfigPath(work.Abs, configPath)
	res, err := config.Load(config.LoadOptions{ConfigPath: configPath, WorkspaceRoot: work.Abs, AllowMissingConfig: true})
	if err != nil {
		return err
	}
	logMCPWarnings(stderr, res)
	applyPrivacyPolicy(res)
	applyWorkflowStoreRoot(res, work.Abs)
	store, repo, closeFn, err := workflowResumeOpenStore(work.Abs, res.Subagents)
	if err != nil {
		return err
	}
	defer closeFn()
	releaseExecution, err := acquireWorkflowExecutionLockBounded(contextStorePath(work.Abs, res.Subagents), runID, workflowResolutionLockWait)
	if err != nil {
		return err
	}
	defer releaseExecution()

	ctx := context.Background()
	run, snapshot, priorRaw, compiled, inputs, err := loadWorkflowResumeState(ctx, repo, runID, res)
	if err != nil {
		return err
	}
	terminal, err := reconcileWorkflowTerminal(ctx, repo, runID, compiled.DeliveryActive(), stdout)
	if err != nil {
		return err
	}
	if terminal {
		return finishWorkflowResumeTerminal(ctx, work.Abs, res, store, repo, runID, run.WorkflowName, compiled, allowPublish, stdout, stderr)
	}
	if acceptVerifierChange {
		if err := applyAcceptedVerifierChanges(&snapshot, compiled, res.Verifiers, stderr); err != nil {
			return err
		}
	}
	remaining, skillsReg, err := prepareWorkflowResumeAdmission(ctx, repo, work.Abs, compiled, runID, acceptSkillChange, &snapshot, stderr)
	if err != nil {
		return err
	}
	uninstallHooks, err := workflowResumeInstallHooks(work.Abs, false, false)
	if err != nil {
		return err
	}
	defer uninstallHooks()
	built, err := workflowResumeBuild(work.Abs, res, store, repo, compiled, "", inputs, snapshot.Inputs, snapshot.DefinitionTOML, runID, &snapshot, priorRaw, &run, remaining, skillsReg)
	if err != nil {
		return err
	}
	defer built.Dispatcher.Close()
	if err := workflowResumeSetAdmission(built); err != nil {
		return err
	}
	if err := workflowResumeSetForce(built); err != nil {
		return err
	}
	wireCLIWorkflowProgress(&built, stderr)
	if err := prepareWorkflowResumeExecution(ctx, built, repo, runID, force, stdout); err != nil {
		return err
	}
	// Safety net: releases the preflight handoff claim if the inline release
	// below was not reached. Releasing with a stale holder is harmless:
	// ReleaseRun is a no-op when the caller is not the current holder.
	defer releaseWorkflowResumeHandoff(repo, runID, built.Controller)
	return runWorkflowResumeAndSettle(ctx, built, repo, runID, work.Abs, res, store, run.WorkflowName, compiled, allowPublish, stdout, stderr)
}

// prepareWorkflowResumeAdmission loads the skill registry once (R5) so the
// acceptance rewrite and the build verify against the same bytes, applies
// --accept-skill-change, and derives the set of steps still to run (R3).
func prepareWorkflowResumeAdmission(ctx context.Context, repo workflowledger.Repository, root string, compiled *compiler.CompiledWorkflow, runID string, acceptSkillChange bool, snapshot *workflowledger.Snapshot, stderr io.Writer) (map[string]bool, *skills.Registry, error) {
	registry, err := workflowBuildLoadSkills(root)
	if err != nil {
		return nil, nil, fmt.Errorf("load skills: %w", err)
	}
	if acceptSkillChange {
		if err := applyAcceptedSkillChanges(snapshot, compiled, registry, stderr); err != nil {
			return nil, nil, err
		}
	}
	// Derive the set of steps still to run from the ledger so the skill guard
	// (R3) does not hold the resume hostage to a drifted skill that only
	// completed steps used: the PlanResume-derived active step plus every step
	// reachable from it through declared transitions and on_failure routes.
	// A step outside that set can never run again. When the active step is
	// unknown to the graph the result is nil and the guard checks all steps
	// (safe default).
	remaining, err := workflowRemainingSteps(ctx, repo, runID, compiled)
	if err != nil {
		return nil, nil, err
	}
	return remaining, registry, nil
}

// runWorkflowResumeAndSettle runs the resumed controller and settles the run
// exactly as executeWorkflowResume's inline tail did: the preflight handoff
// claim is released BEFORE settling (settle claims the run with its own
// holder, so a still-held handoff makes the settle a no-op), the settled
// status is printed, a genuine controller fault is settled with
// settleCLIRunFailure (DC-9), and a delivery_pending settlement routes to
// finishWorkflowResumeSettled (drive-before-delivery, mirroring executeWorkflowRun). executeWorkflowResume's deferred
// releaseWorkflowResumeHandoff stays as the safety net for the pre-Run path.
func runWorkflowResumeAndSettle(ctx context.Context, built workflowControllerBuild, repo workflowledger.Repository, runID, workRoot string, res *config.Resolved, store *storage.SQLite, workflowName string, compiled *compiler.CompiledWorkflow, allowPublish bool, stdout, stderr io.Writer) error {
	snap, err := workflowResumeRun(ctx, built)
	// Release the preflight handoff claim BEFORE settling: settle claims the
	// run with its own holder, so a still-held handoff (the controller stopped
	// before its first Advance claimed and released the run) makes the settle
	// a no-op and the run stays running with no cause.
	releaseWorkflowResumeHandoff(repo, runID, built.Controller)
	fmt.Fprintf(stdout, "run_id=%s status=%s\n", runID, snap.Status)
	if err != nil {
		// A genuine (non-deadline) fault that stops the controller must settle
		// the run: Controller.Run self-settles deadline errors, cancel owns
		// cancelled runs, but a raw storage/claim fault would otherwise leave
		// the run row `running` with no cause (DC-9).
		settleCLIRunFailure(repo, runID, err)
		return err
	}
	if snap.Status == workflowledger.RunStatusDeliveryPending {
		return finishWorkflowResumeSettled(ctx, workRoot, res, store, repo, runID, workflowName, compiled, allowPublish, stdout, stderr)
	}
	return nil
}

// finishWorkflowResumeTerminal completes a run reconcileWorkflowTerminal
// already settled, when that settlement landed on delivery_pending; the
// settled stack drives before delivery exactly as the normal resume path.
func finishWorkflowResumeTerminal(ctx context.Context, workRoot string, res *config.Resolved, store *storage.SQLite, repo workflowledger.Repository, runID, workflowName string, compiled *compiler.CompiledWorkflow, allowPublish bool, stdout, stderr io.Writer) error {
	settled, err := repo.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	if settled.Status != workflowledger.RunStatusDeliveryPending {
		return nil
	}
	return finishWorkflowResumeSettled(ctx, workRoot, res, store, repo, runID, workflowName, compiled, allowPublish, stdout, stderr)
}

// finishWorkflowResumeSettled completes a resume that settled at
// delivery_pending, mirroring executeWorkflowRun's drive-before-delivery
// ordering: a multi-chunk stacking plan run drives its stack BEFORE the plan
// run is published, and a driven plan run whose own publication is disabled
// (delivery.deliver_plan_run=false) settles succeeded with the 'plan PR not
// created' notice instead of delivering. The preparedWorkflowRun is rebuilt
// from the run snapshot: both resume settle points reach here without it.
func finishWorkflowResumeSettled(ctx context.Context, root string, res *config.Resolved, store *storage.SQLite, repo workflowledger.Repository, runID, workflowName string, compiled *compiler.CompiledWorkflow, allowPublish bool, stdout, stderr io.Writer) error {
	raw, err := repo.GetRunSnapshot(ctx, runID)
	if err != nil {
		return err
	}
	snapshot, err := workflowledger.UnmarshalSnapshot(raw)
	if err != nil {
		return err
	}
	// CLI foreground paths are unbounded by design: the drive's ctx is the
	// session attempt bound's stop signal, and resume owns the run until the
	// stack completes (or the process is interrupted).
	drove, err := maybeDriveSettledStack(context.Background(), &preparedWorkflowRun{
		root: root, res: res, store: store, repo: repo,
		compiled: compiled, inputSnapshot: snapshot.Inputs,
		refBase: "", raw: raw,
	}, runID, allowPublish, stdout, stderr)
	if err != nil {
		if errors.Is(err, errStackAwaitsGrant) {
			// A durable pause, not a failure: see workflow_run.go's mirror
			// of this check for the full rationale.
			return nil
		}
		return err
	}
	if drove && !compiledDeliverPlanRun(compiled) {
		if err := settlePlanRunSkippedDelivery(ctx, repo, runID); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "run_id=%s status=%s plan PR not created (delivery.deliver_plan_run=false); plan and artifacts recorded in the ledger\n", runID, workflowledger.RunStatusSucceeded)
		return nil
	}
	return finishWorkflowRunDelivery(ctx, root, res, store, repo, runID, workflowName, workflowResumeDeliveryMode(compiled), allowPublish, stdout, stderr)
}

// workflowResumeDeliveryMode returns the compiled workflow's delivery mode,
// or "" when no delivery policy is compiled. Mirrors the inline nil check
// executeWorkflowRun uses; extracted here because executeWorkflowResume needs
// it at both the crash-recovery settle point and the normal resume-settle
// point.
func workflowResumeDeliveryMode(compiled *compiler.CompiledWorkflow) string {
	if compiled != nil && compiled.Delivery != nil {
		return compiled.Delivery.Mode
	}
	return ""
}

// prepareWorkflowResumeExecution handles the shared resume handoff. It claims
// the run with the final controller holder before it joins recorded children,
// and heartbeats that handoff claim during the join so the claim lease never
// expires before the controller's own Advance heartbeat takes over.
func prepareWorkflowResumeExecution(ctx context.Context, built workflowControllerBuild, repo workflowledger.Repository, runID string, force bool, stdout io.Writer) error {
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
// takes over. It returns a derived context that is cancelled when the claim is
// lost (another holder takes it), and a stop function that must be called when
// the join finishes.
func startWorkflowResumeHandoffHeartbeat(parent context.Context, repo workflowledger.Repository, runID, holder string) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(workflowResumeClaimHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := repo.RefreshRunClaim(context.Background(), runID, holder); err != nil {
					if errors.Is(err, workflowledger.ErrClaimHeld) || errors.Is(err, workflowledger.ErrClaimNotHeld) {
						cancel()
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
		cancel()
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
func joinInFlightAttempts(ctx context.Context, built workflowControllerBuild, repo workflowledger.Repository, runID string, stdout io.Writer) (err error) {
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
	// The join is bounded so a child whose coordinator run never settles cannot
	// park resume forever: bound to the run's own deadline when present (time.Until
	// it), otherwise a fixed workflowResumeJoinBound. On expiry the join is
	// abandoned with a clear error and the attempt stays in-flight for the
	// controller's normal reconciliation (Advance interrupts and re-dispatches it
	// under the run claim on a subsequent resume).
	joinCtx, cancelJoin := workflowResumeJoinCtx(ctx, run)
	defer cancelJoin()
	for _, inFlight := range plan.AttemptsInFlight {
		if err := built.Controller.JoinInFlightAttempt(joinCtx, inFlight); err != nil {
			return fmt.Errorf("join in-flight attempt %s for step %q: %w", inFlight.AttemptID, inFlight.StepID, err)
		}
		if joinCtx.Err() != nil {
			return fmt.Errorf("join in-flight attempt %s for step %q did not settle within the resume join bound; leaving it in-flight for controller reconciliation: %w", inFlight.AttemptID, inFlight.StepID, joinCtx.Err())
		}
	}
	return nil
}

// workflowResumeJoinCtx derives the bounded context for the pre-Run in-flight
// attempt join: the run's own deadline when present (time.Until it), otherwise
// the fixed workflowResumeJoinBound. An already-expired deadline yields an
// immediately-expired context so the join fails fast instead of hanging.
func workflowResumeJoinCtx(parent context.Context, run workflowledger.RunSnapshot) (context.Context, context.CancelFunc) {
	if run.DeadlineAt != nil {
		return context.WithDeadline(parent, *run.DeadlineAt)
	}
	return context.WithTimeout(parent, workflowResumeJoinBound)
}

// refuseWorkflowDeliverySettled points resume at the delivery surface for runs
// whose body is complete: delivery_pending means the result waits for
// publication, and delivery_failed means publication failed (its refusal may
// have cleared - a forward-advanced base is normal). Recovery for both is a
// delivery concern, not a body re-run: re-eligibility happens inside workflow
// deliver.
func refuseWorkflowDeliverySettled(runID string, status workflowledger.RunStatus) error {
	if status == workflowledger.RunStatusDeliveryPending {
		return fmt.Errorf("workflow run %q is waiting for delivery; deliver with: mivia workflow deliver %s --allow-publish", runID, runID)
	}
	if status == workflowledger.RunStatusDeliveryFailed {
		return fmt.Errorf("workflow run %q failed delivery; recover with: mivia workflow deliver %s --allow-publish (re-runs eligibility; add --force after a prior deliverer stopped)", runID, runID)
	}
	return nil
}

func reconcileWorkflowTerminal(ctx context.Context, repo workflowledger.Repository, runID string, deliveryActive bool, stdout io.Writer) (bool, error) {
	plan, err := workflowledger.PlanResume(ctx, repo, runID)
	if err != nil {
		return false, err
	}
	if !plan.Terminal {
		return false, nil
	}
	// A run whose derived route reached the success terminal under an active
	// delivery policy must settle at delivery_pending, never succeeded: the
	// delivery phase still has to publish. This mirrors the controller's
	// terminal-route routing for the crash window.
	if deliveryActive && plan.TerminalStatus == workflowledger.RunStatusSucceeded &&
		plan.Run.Status != workflowledger.RunStatusDeliveryPending {
		plan.TerminalStatus = workflowledger.RunStatusDeliveryPending
	}
	if !workflowledger.IsTerminalRunStatus(plan.Run.Status) && plan.TerminalStatus != plan.Run.Status {
		holder := newWorkflowCancelHolder()
		if err := claimWorkflowOperator(ctx, repo, runID, holder); err != nil {
			return false, err
		}
		defer func() { _ = repo.ReleaseRun(context.Background(), runID, holder) }()
		ctx = workflowledger.ContextWithClaimHolder(ctx, holder)
		from := plan.Run
		// waiting_approval has no direct edge to a terminal status (the edge
		// table only allows running/failed/canceled/timed_out); step through
		// running first, exactly as the controller's reconcileTerminalRoute
		// does for the approve crash window.
		if from.Status == workflowledger.RunStatusWaitingApproval {
			if err := repo.CompareAndSetRunStatus(ctx, runID, from.Version, workflowledger.RunStatusRunning, nil); err != nil {
				return false, err
			}
			fresh, err := repo.GetRun(ctx, runID)
			if err != nil {
				return false, err
			}
			from = fresh
		}
		if err := repo.CompareAndSetRunStatus(ctx, runID, from.Version, plan.TerminalStatus, nil); err != nil {
			return false, err
		}
		plan.Run, err = repo.GetRun(ctx, runID)
		if err != nil {
			return false, err
		}
	}
	fmt.Fprintf(stdout, "run_id=%s status=%s\n", runID, plan.Run.Status)
	return true, nil
}
