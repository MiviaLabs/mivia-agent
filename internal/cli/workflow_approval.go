package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// workflowApprovalDefaultActor names the operator when --actor is not given.
// It is deterministic (never the ambient OS user) so operator scripts and
// tests see stable approval records.
const workflowApprovalDefaultActor = "operator"

// executeWorkflowApprove approves one pending human_gate approval and lets
// the run continue, mirroring executeWorkflowResume's preamble (file lock,
// then controller resolution under the lock).
func executeWorkflowApprove(runID, approvalID, root, configPath, actor string, stdout, stderr io.Writer) error {
	releaseExecution, repo, _, closeFn, err := openWorkflowResolutionContextBounded(context.Background(), root, configPath, runID, workflowResolutionLockWait)
	if err != nil {
		return err
	}
	defer closeFn()
	defer releaseExecution()
	ctx := context.Background()
	before, err := repo.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	ctrl, err := buildResolutionController(ctx, repo, runID)
	if err != nil {
		return err
	}
	if err := claimWorkflowOperator(ctx, repo, runID, ctrl.Holder); err != nil {
		return err
	}
	defer func() { _ = repo.ReleaseRun(context.Background(), runID, ctrl.Holder) }()
	ctx = workflowledger.ContextWithClaimHolder(ctx, ctrl.Holder)
	if err := ctrl.Approve(ctx, approvalID, actor); err != nil {
		fmt.Fprintf(stderr, "workflow approval failed: %v\n", err)
		return err
	}
	settled, err := repo.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	emitCLIRunTerminalProgress(runID, before.Status, settled.Status, stderr)
	fmt.Fprintf(stdout, "run_id=%s status=%s\n", runID, settled.Status)
	return nil
}

// executeWorkflowReject rejects one pending human_gate approval and fails the
// run, mirroring executeWorkflowApprove's lock and controller flow. Like
// approve, it reads the before-snapshot BEFORE building the controller, so an
// unknown run fails fast at the before-read instead of inside the controller.
func executeWorkflowReject(runID, approvalID, root, configPath, actor, reason string, stdout, stderr io.Writer) error {
	releaseExecution, repo, _, closeFn, err := openWorkflowResolutionContextBounded(context.Background(), root, configPath, runID, workflowResolutionLockWait)
	if err != nil {
		return err
	}
	defer closeFn()
	defer releaseExecution()
	ctx := context.Background()
	before, err := repo.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	ctrl, err := buildResolutionController(ctx, repo, runID)
	if err != nil {
		return err
	}
	if err := claimWorkflowOperator(ctx, repo, runID, ctrl.Holder); err != nil {
		return err
	}
	defer func() { _ = repo.ReleaseRun(context.Background(), runID, ctrl.Holder) }()
	ctx = workflowledger.ContextWithClaimHolder(ctx, ctrl.Holder)
	if err := ctrl.Reject(ctx, approvalID, actor, reason); err != nil {
		fmt.Fprintf(stderr, "workflow rejection failed: %v\n", err)
		return err
	}
	settled, err := repo.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	emitCLIRunTerminalProgress(runID, before.Status, settled.Status, stderr)
	fmt.Fprintf(stdout, "run_id=%s status=%s\n", runID, settled.Status)
	return nil
}

// emitCLIRunTerminalProgress writes one run_finished JSON line to stderr when
// an operator command TRANSITIONS the run from a non-terminal status to a
// terminal status. The workflow controller emits the same event on its own
// terminal paths; operator-driven settlements (cancel, approve, reject) emit
// here so the non-interactive stream stays consistent. An idempotent command
// on an already-terminal run (before == settled) never emits: the settlement
// happened elsewhere and another run_finished would be a duplicate.
func emitCLIRunTerminalProgress(runID string, before, settled workflowledger.RunStatus, stderr io.Writer) {
	if stderr == nil || workflowledger.IsTerminalRunStatus(before) || !workflowledger.IsTerminalRunStatus(settled) {
		return
	}
	(&workflowProgressWriter{w: stderr}).Emit(controller.ProgressEvent{
		Kind: controller.ProgressRunFinished, RunID: runID, Detail: string(settled),
	})
}

// executeWorkflowCancel cancels a non-terminal run (or no-ops on a terminal
// run), mirroring controller.CancelRun's contract: the caller holds the
// workflow execution file lock and a live execution claim.
func executeWorkflowCancel(runID, root, configPath string, stdout, stderr io.Writer) error {
	releaseExecution, repo, store, closeFn, err := openWorkflowResolutionContextBounded(context.Background(), root, configPath, runID, workflowResolutionLockWait)
	if err != nil {
		return err
	}
	defer closeFn()
	defer releaseExecution()
	ctx := context.Background()
	holder := newWorkflowCancelHolder()
	if err := claimWorkflowOperator(ctx, repo, runID, holder); err != nil {
		return err
	}
	defer func() { _ = repo.ReleaseRun(context.Background(), runID, holder) }()
	ctx = workflowledger.ContextWithClaimHolder(ctx, holder)
	before, err := repo.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	// This one-shot process never has a live in-process controller, so the
	// coordinator is always freshly built over store's own ledger (D15,
	// Wave 7): see cliPanelCancelCoordinator.
	attempts, err := controller.CancelRunWithAttemptsWithClaim(ctx, repo, cliPanelCancelCoordinator(nil, store), runID, holder)
	if err != nil {
		fmt.Fprintf(stderr, "workflow cancel failed: %v\n", err)
		return err
	}
	// Terminal progress: one step_completed(canceled) JSON line per attempt
	// the cancel settled, plus one run_finished line when the run reaches a
	// terminal status, so consumers of the non-interactive stream see the
	// operator cancel like any other run terminal event.
	publishCanceledAttemptsCLI(runID, attempts, stderr)
	settled, err := repo.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	emitCLIRunTerminalProgress(runID, before.Status, settled.Status, stderr)
	fmt.Fprintf(stdout, "run_id=%s status=%s\n", runID, settled.Status)
	return nil
}

func claimWorkflowOperator(ctx context.Context, repo workflowledger.Repository, runID, holder string) error {
	err := repo.ClaimRun(ctx, runID, holder)
	if err == nil {
		return nil
	}
	if !errors.Is(err, workflowledger.ErrClaimHeld) {
		return err
	}
	err = repo.TakeoverExpiredRunClaim(ctx, runID, holder, workflowledger.DefaultClaimLease)
	if errors.Is(err, workflowledger.ErrClaimNotHeld) {
		return repo.ClaimRun(ctx, runID, holder)
	}
	if errors.Is(err, workflowledger.ErrClaimHeld) {
		return fmt.Errorf("workflow run %q is claimed by another executor", runID)
	}
	return err
}

// publishCanceledAttemptsCLI writes one step_completed(canceled) JSON line to
// stderr per attempt an operator cancel settled, reusing the non-interactive
// progress writer.
func publishCanceledAttemptsCLI(runID string, attempts []workflowledger.StepAttempt, stderr io.Writer) {
	if stderr == nil {
		return
	}
	writer := &workflowProgressWriter{w: stderr}
	for _, attempt := range attempts {
		writer.Emit(controller.ProgressEvent{
			Kind:             controller.ProgressStepCompleted,
			RunID:            runID,
			StepID:           attempt.StepID,
			AttemptNo:        attempt.AttemptNo,
			TaskID:           attempt.TaskID,
			CoordinatorRunID: attempt.CoordinatorRunID,
			Detail:           "canceled",
			Timestamp:        time.Now(),
		})
	}
}

// openWorkflowResolutionContext opens the workspace, config, store, and the
// workflow execution file lock for the mutating operator commands (approve,
// reject, cancel). The returned release must be called after closeFn.
func openWorkflowResolutionContext(root, configPath, runID string) (func(), workflowledger.Repository, *storage.SQLite, func(), error) {
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	work, err := workspace.Open(root)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	configPath = workflowConfigPath(work.Abs, configPath)
	res, err := config.Load(config.LoadOptions{ConfigPath: configPath, WorkspaceRoot: work.Abs, AllowMissingConfig: true})
	if err != nil {
		return nil, nil, nil, nil, err
	}
	applyPrivacyPolicy(res)
	applyWorkflowStoreRoot(res, work.Abs)
	store, repo, closeFn, err := openWorkflowStore(work.Abs, res.Subagents)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	releaseExecution, err := acquireWorkflowExecutionLock(contextStorePath(work.Abs, res.Subagents), runID)
	if err != nil {
		closeFn()
		return nil, nil, nil, nil, err
	}
	return releaseExecution, repo, store, closeFn, nil
}

// openWorkflowResolutionContextBounded is openWorkflowResolutionContext with a
// bounded wait for the execution lock: cancel and deliver call it so a still-
// settling controller does not surface as an opaque lock error.
func openWorkflowResolutionContextBounded(ctx context.Context, root, configPath, runID string, lockWait time.Duration) (func(), workflowledger.Repository, *storage.SQLite, func(), error) {
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	work, err := workspace.Open(root)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	configPath = workflowConfigPath(work.Abs, configPath)
	res, err := config.Load(config.LoadOptions{ConfigPath: configPath, WorkspaceRoot: work.Abs, AllowMissingConfig: true})
	if err != nil {
		return nil, nil, nil, nil, err
	}
	applyPrivacyPolicy(res)
	applyWorkflowStoreRoot(res, work.Abs)
	store, repo, closeFn, err := openWorkflowStore(work.Abs, res.Subagents)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	releaseExecution, err := acquireWorkflowExecutionLockBounded(ctx, contextStorePath(work.Abs, res.Subagents), runID, lockWait)
	if err != nil {
		closeFn()
		return nil, nil, nil, nil, err
	}
	return releaseExecution, repo, store, closeFn, nil
}

// buildResolutionController loads and validates the run snapshot, then builds
// a controller that can only route human decisions (never execute steps).
func buildResolutionController(ctx context.Context, repo workflowledger.Repository, runID string) (*controller.LinearController, error) {
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		if errors.Is(err, workflowledger.ErrNotFound) {
			return nil, fmt.Errorf("workflow run %q not found", runID)
		}
		return nil, err
	}
	raw, err := repo.GetRunSnapshot(ctx, runID)
	if err != nil {
		return nil, err
	}
	_, compiled, inputs, err := validateWorkflowResumeSnapshot(run, raw)
	if err != nil {
		return nil, err
	}
	return controller.NewResolutionController(repo, compiled, runID, raw, inputs)
}

// resolveWorkflowDialogApproval resolves one pending gate approval through the
// same bounded-lock controller path as the workflow_approval CLI surface. The
// actor is fixed to workflowApprovalDefaultActor so operator scripts and tests
// see deterministic approval records.
func resolveWorkflowDialogApproval(runID, approvalID, root, configPath, actor string, reject bool) error {
	releaseExecution, repo, _, closeFn, err := openWorkflowResolutionContextBounded(context.Background(), root, configPath, runID, workflowResolutionLockWait)
	if err != nil {
		return err
	}
	defer closeFn()
	defer releaseExecution()
	ctx := context.Background()
	ctrl, err := buildResolutionController(ctx, repo, runID)
	if err != nil {
		return err
	}
	if err := claimWorkflowOperator(ctx, repo, runID, ctrl.Holder); err != nil {
		return err
	}
	defer func() { _ = repo.ReleaseRun(context.Background(), runID, ctrl.Holder) }()
	ctx = workflowledger.ContextWithClaimHolder(ctx, ctrl.Holder)
	if reject {
		return ctrl.Reject(ctx, approvalID, actor, "")
	}
	return ctrl.Approve(ctx, approvalID, actor)
}
