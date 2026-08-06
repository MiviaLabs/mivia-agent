package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
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
	releaseExecution, repo, closeFn, err := openWorkflowResolutionContext(root, configPath, runID)
	if err != nil {
		return err
	}
	defer closeFn()
	defer releaseExecution()
	ctx := context.Background()
	if err := repo.ClearRunClaim(ctx, runID); err != nil {
		return fmt.Errorf("clear stale workflow claim: %w", err)
	}
	ctrl, err := buildResolutionController(ctx, repo, runID)
	if err != nil {
		return err
	}
	if err := ctrl.Approve(ctx, approvalID, actor); err != nil {
		fmt.Fprintf(stderr, "workflow approval failed: %v\n", err)
		return err
	}
	settled, err := repo.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "run_id=%s status=%s\n", runID, settled.Status)
	return nil
}

// executeWorkflowReject rejects one pending human_gate approval and fails the
// run, mirroring executeWorkflowApprove's lock and controller flow.
func executeWorkflowReject(runID, approvalID, root, configPath, actor, reason string, stdout, stderr io.Writer) error {
	releaseExecution, repo, closeFn, err := openWorkflowResolutionContext(root, configPath, runID)
	if err != nil {
		return err
	}
	defer closeFn()
	defer releaseExecution()
	ctx := context.Background()
	if err := repo.ClearRunClaim(ctx, runID); err != nil {
		return fmt.Errorf("clear stale workflow claim: %w", err)
	}
	ctrl, err := buildResolutionController(ctx, repo, runID)
	if err != nil {
		return err
	}
	if err := ctrl.Reject(ctx, approvalID, actor, reason); err != nil {
		fmt.Fprintf(stderr, "workflow rejection failed: %v\n", err)
		return err
	}
	settled, err := repo.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "run_id=%s status=%s\n", runID, settled.Status)
	return nil
}

// executeWorkflowCancel cancels a non-terminal run (or no-ops on a terminal
// run), mirroring controller.CancelRun's contract: the caller holds the
// workflow execution file lock and clears a stale claim first.
func executeWorkflowCancel(runID, root, configPath string, stdout, stderr io.Writer) error {
	releaseExecution, repo, closeFn, err := openWorkflowResolutionContext(root, configPath, runID)
	if err != nil {
		return err
	}
	defer closeFn()
	defer releaseExecution()
	ctx := context.Background()
	if err := repo.ClearRunClaim(ctx, runID); err != nil {
		return fmt.Errorf("clear stale workflow claim: %w", err)
	}
	if err := controller.CancelRun(ctx, repo, runID); err != nil {
		fmt.Fprintf(stderr, "workflow cancel failed: %v\n", err)
		return err
	}
	settled, err := repo.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "run_id=%s status=%s\n", runID, settled.Status)
	return nil
}

// openWorkflowResolutionContext opens the workspace, config, store, and the
// workflow execution file lock for the mutating operator commands (approve,
// reject, cancel). The returned release must be called after closeFn.
func openWorkflowResolutionContext(root, configPath, runID string) (func(), workflowledger.Repository, func(), error) {
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	work, err := workspace.Open(root)
	if err != nil {
		return nil, nil, nil, err
	}
	configPath = workflowConfigPath(work.Abs, configPath)
	res, err := config.Load(config.LoadOptions{ConfigPath: configPath, AllowMissingConfig: true})
	if err != nil {
		return nil, nil, nil, err
	}
	applyPrivacyPolicy(res)
	applyWorkflowStoreRoot(res, work.Abs)
	_, repo, closeFn, err := openWorkflowStore(work.Abs, res.Subagents)
	if err != nil {
		return nil, nil, nil, err
	}
	releaseExecution, err := acquireWorkflowExecutionLock(contextStorePath(work.Abs, res.Subagents), runID)
	if err != nil {
		closeFn()
		return nil, nil, nil, err
	}
	return releaseExecution, repo, closeFn, nil
}

// openWorkflowResolutionContextBounded is openWorkflowResolutionContext with a
// bounded wait for the execution lock: cancel and deliver call it so a still-
// settling controller does not surface as an opaque lock error.
func openWorkflowResolutionContextBounded(root, configPath, runID string, lockWait time.Duration) (func(), workflowledger.Repository, func(), error) {
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	work, err := workspace.Open(root)
	if err != nil {
		return nil, nil, nil, err
	}
	configPath = workflowConfigPath(work.Abs, configPath)
	res, err := config.Load(config.LoadOptions{ConfigPath: configPath, AllowMissingConfig: true})
	if err != nil {
		return nil, nil, nil, err
	}
	applyPrivacyPolicy(res)
	applyWorkflowStoreRoot(res, work.Abs)
	_, repo, closeFn, err := openWorkflowStore(work.Abs, res.Subagents)
	if err != nil {
		return nil, nil, nil, err
	}
	releaseExecution, err := acquireWorkflowExecutionLockBounded(contextStorePath(work.Abs, res.Subagents), runID, lockWait)
	if err != nil {
		closeFn()
		return nil, nil, nil, err
	}
	return releaseExecution, repo, closeFn, nil
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
