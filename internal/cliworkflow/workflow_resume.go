package cliworkflow

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

var (
	WorkflowResumeOpenStore    = OpenWorkflowStore
	workflowResumeBuild        = buildWorkflowController
	WorkflowResumeSetAdmission = func(b WorkflowControllerBuild) error {
		return b.Controller.SetAdmission(b.Admission)
	}
	WorkflowResumeSetForce = func(b WorkflowControllerBuild) error {
		return b.Controller.SetForceResume(true)
	}
	WorkflowResumeRun = func(ctx context.Context, b WorkflowControllerBuild) (workflowledger.RunSnapshot, error) {
		return b.Controller.Run(ctx)
	}
)

func ExecuteWorkflowResume(runID, root, configPath string, force, allowPublish, acceptVerifierChange, acceptSkillChange bool, stdout, stderr io.Writer) error {
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	work, err := workspace.Open(root)
	if err != nil {
		return err
	}
	configPath = WorkflowConfigPath(work.Abs, configPath)
	res, err := config.Load(config.LoadOptions{ConfigPath: configPath, WorkspaceRoot: work.Abs, AllowMissingConfig: true})
	if err != nil {
		return err
	}
	LogMCPWarningsFunc(stderr, res)
	ApplyPrivacyPolicyFunc(res)
	ApplyWorkflowStoreRoot(res, work.Abs)
	store, repo, closeFn, err := WorkflowResumeOpenStore(work.Abs, res.Subagents)
	if err != nil {
		return err
	}
	defer closeFn()
	rawReleaseExecution, err := acquireWorkflowExecutionLockBounded(context.Background(), ContextStorePath(work.Abs, res.Subagents), runID, WorkflowResolutionLockWait)
	if err != nil {
		return err
	}
	// Wrapped so a repairable delivery rejection can release the execution
	// lock early (see reenterRepairedRun) without this deferred call
	// double-releasing when the function returns.
	releaseExecution := sync.OnceFunc(rawReleaseExecution)
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
		return finishWorkflowResumeTerminal(ctx, work.Abs, configPath, res, store, repo, runID, run.WorkflowName, compiled, allowPublish, releaseExecution, stdout, stderr)
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
	uninstallHooks, err := WorkflowResumeInstallHooks(work.Abs, false, false)
	if err != nil {
		return err
	}
	defer uninstallHooks()
	built, err := workflowResumeBuild(work.Abs, res, store, repo, compiled, "", inputs, snapshot.Inputs, snapshot.DefinitionTOML, runID, &snapshot, priorRaw, &run, remaining, skillsReg)
	if err != nil {
		return err
	}
	defer built.Dispatcher.Close()
	if err := WorkflowResumeSetAdmission(built); err != nil {
		return err
	}
	if err := WorkflowResumeSetForce(built); err != nil {
		return err
	}
	WireCLIWorkflowProgress(&built, stderr)
	if err := prepareWorkflowResumeExecution(ctx, built, repo, runID, force, stdout); err != nil {
		return err
	}
	// Safety net: releases the preflight handoff claim if the inline release
	// below was not reached. Releasing with a stale holder is harmless:
	// ReleaseRun is a no-op when the caller is not the current holder.
	defer releaseWorkflowResumeHandoff(repo, runID, built.Controller)
	return runWorkflowResumeAndSettle(ctx, built, repo, runID, work.Abs, configPath, res, store, run.WorkflowName, compiled, allowPublish, releaseExecution, stdout, stderr)
}

// prepareWorkflowResumeAdmission loads the skill registry once (R5) so the
// acceptance rewrite and the build verify against the same bytes, applies
// --accept-skill-change, and derives the set of steps still to run (R3).
func prepareWorkflowResumeAdmission(ctx context.Context, repo workflowledger.Repository, root string, compiled *definition.CompiledWorkflow, runID string, acceptSkillChange bool, snapshot *workflowledger.Snapshot, stderr io.Writer) (map[string]bool, *skills.Registry, error) {
	registry, err := WorkflowBuildLoadSkills(root)
	if err != nil {
		return nil, nil, fmt.Errorf("load skills: %w", err)
	}
	if acceptSkillChange {
		if err := ApplyAcceptedSkillChanges(snapshot, compiled, registry, stderr); err != nil {
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
	remaining, err := WorkflowRemainingSteps(ctx, repo, runID, compiled)
	if err != nil {
		return nil, nil, err
	}
	return remaining, registry, nil
}

// runWorkflowResumeAndSettle runs the resumed controller and settles the run
// exactly as ExecuteWorkflowResume's inline tail did: the preflight handoff
// claim is released BEFORE settling (settle claims the run with its own
// holder, so a still-held handoff makes the settle a no-op), the settled
// status is printed, a genuine controller fault is settled with
// SettleCLIRunFailure (DC-9), and a delivery_pending settlement routes to
// finishWorkflowResumeSettled (drive-before-delivery, mirroring ExecuteWorkflowRun). ExecuteWorkflowResume's deferred
// releaseWorkflowResumeHandoff stays as the safety net for the pre-Run path.
func runWorkflowResumeAndSettle(ctx context.Context, built WorkflowControllerBuild, repo workflowledger.Repository, runID, workRoot, configPath string, res *config.Resolved, store *storage.SQLite, workflowName string, compiled *definition.CompiledWorkflow, allowPublish bool, release func(), stdout, stderr io.Writer) error {
	snap, err := WorkflowResumeRun(ctx, built)
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
		SettleCLIRunFailure(repo, runID, err)
		return err
	}
	if snap.Status == workflowledger.RunStatusDeliveryPending {
		return finishWorkflowResumeSettled(ctx, workRoot, configPath, res, store, repo, runID, workflowName, compiled, allowPublish, release, stdout, stderr)
	}
	return nil
}

// finishWorkflowResumeTerminal completes a run reconcileWorkflowTerminal
// already settled, when that settlement landed on delivery_pending; the
// settled stack drives before delivery exactly as the normal resume path.
func finishWorkflowResumeTerminal(ctx context.Context, workRoot, configPath string, res *config.Resolved, store *storage.SQLite, repo workflowledger.Repository, runID, workflowName string, compiled *definition.CompiledWorkflow, allowPublish bool, release func(), stdout, stderr io.Writer) error {
	settled, err := repo.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	if settled.Status != workflowledger.RunStatusDeliveryPending {
		return nil
	}
	return finishWorkflowResumeSettled(ctx, workRoot, configPath, res, store, repo, runID, workflowName, compiled, allowPublish, release, stdout, stderr)
}

// finishWorkflowResumeSettled completes a resume that settled at
// delivery_pending, mirroring ExecuteWorkflowRun's drive-before-delivery
// ordering: a multi-chunk stacking plan run drives its stack BEFORE the plan
// run is published, and a driven plan run whose own publication is disabled
// (delivery.deliver_plan_run=false) settles succeeded with the 'plan PR not
// created' notice instead of delivering. The PreparedWorkflowRun is rebuilt
// from the run snapshot: both resume settle points reach here without it.
func finishWorkflowResumeSettled(ctx context.Context, root, configPath string, res *config.Resolved, store *storage.SQLite, repo workflowledger.Repository, runID, workflowName string, compiled *definition.CompiledWorkflow, allowPublish bool, release func(), stdout, stderr io.Writer) error {
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
	drove, err := maybeDriveSettledStack(context.Background(), &PreparedWorkflowRun{
		Root: root, Res: res, Store: store, Repo: repo,
		Compiled: compiled, InputSnapshot: snapshot.Inputs,
		RefBase: "", Raw: raw,
	}, runID, allowPublish, stdout, stderr)
	if err != nil {
		if errors.Is(err, ErrStackAwaitsGrant) {
			// A durable pause, not a failure: see workflow_run.go's mirror
			// of this check for the full rationale.
			return nil
		}
		return err
	}
	if drove && !compiledDeliverPlanRun(compiled) {
		if err := SettlePlanRunSkippedDelivery(ctx, repo, runID); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "run_id=%s status=%s plan PR not created (delivery.deliver_plan_run=false); plan and artifacts recorded in the ledger\n", runID, workflowledger.RunStatusSucceeded)
		return nil
	}
	return finishWorkflowRunDelivery(ctx, root, configPath, res, store, repo, runID, workflowName, workflowResumeDeliveryMode(compiled), allowPublish, release, stdout, stderr)
}

// workflowResumeDeliveryMode returns the compiled workflow's delivery mode,
// or "" when no delivery policy is compiled. Mirrors the inline nil check
// ExecuteWorkflowRun uses; extracted here because ExecuteWorkflowResume needs
// it at both the crash-recovery settle point and the normal resume-settle
// point.
func workflowResumeDeliveryMode(compiled *definition.CompiledWorkflow) string {
	if compiled != nil && compiled.Delivery != nil {
		return compiled.Delivery.Mode
	}
	return ""
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

// formatWorkflowResumeError formats a resume-path drift error so an agent or
// operator sees the run ID, what changed, which remaining steps reference it,
// and the recovery options. steps may be nil when the information is unknown
// or not applicable.
func formatWorkflowResumeError(runID, what string, steps []string, options string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "workflow run %q: %s", runID, what)
	if len(steps) > 0 {
		fmt.Fprintf(&sb, " (used by step(s): %s)", formatStepList(steps))
	}
	if options != "" {
		fmt.Fprintf(&sb, "; recover with: %s", options)
	}
	return sb.String()
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
		if err := ClaimWorkflowOperator(ctx, repo, runID, holder); err != nil {
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
