package cli

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	workflowspace "github.com/MiviaLabs/mivia-agent/internal/workflows/workspace"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

var (
	workflowDeliverGit      delivery.GitRunner = delivery.RealGit{}
	workflowDeliverNewPR                       = func() delivery.PRClient { return delivery.GitHubCLI{} }
	workflowDeliverNow                         = time.Now
	workflowDeliverRandom                      = rand.Read
	workflowDeliveryTimeout                    = 10 * time.Minute
)

// executeWorkflowDeliver publishes the result of one delivery_pending run via
// the `workflow deliver` subcommand. It mirrors executeWorkflowRun's preamble:
// resolve the workspace and config, open the workflow store, then acquire the
// workflow execution file lock (beginWorkflowExecution also installs hooks)
// before handing off to deliverRunWithStore, which must NOT re-acquire the
// lock. On success it prints the settled run status like executeWorkflowRun.
func executeWorkflowDeliver(runID, root, configPath string, allowPublish bool, stdout, stderr io.Writer) error {
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	work, err := workspace.Open(root)
	if err != nil {
		return err
	}
	configPath = workflowConfigPath(work.Abs, configPath)
	res, err := config.Load(config.LoadOptions{ConfigPath: configPath, AllowMissingConfig: true})
	if err != nil {
		return err
	}
	applyPrivacyPolicy(res)
	applyWorkflowStoreRoot(res, work.Abs)
	store, repo, closeFn, err := openWorkflowStore(work.Abs, res.Subagents)
	if err != nil {
		return err
	}
	defer closeFn()
	finishExecution, err := beginWorkflowExecution(work.Abs, contextStorePath(work.Abs, res.Subagents), runID)
	if err != nil {
		return err
	}
	defer finishExecution()
	ctx := context.Background()
	if err := deliverRunWithStore(ctx, work.Abs, res, store, repo, runID, allowPublish, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "workflow delivery failed: %v\n", err)
		return err
	}
	settled, err := repo.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "run_id=%s status=%s\n", runID, settled.Status)
	return nil
}

// deliverRunWithStore performs delivery for a delivery_pending run. The caller
// holds the workflow execution file lock (beginWorkflowExecution).
func deliverRunWithStore(ctx context.Context, root string, res *config.Resolved, store *storage.SQLite, repo workflowledger.Repository, runID string, allowPublish bool, stdout, stderr io.Writer) error {
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	raw, err := repo.GetRunSnapshot(ctx, runID)
	if err != nil {
		return err
	}
	snapshot, compiled, _, err := validateWorkflowResumeSnapshot(run, raw)
	if err != nil {
		return err
	}
	policy, ok := delivery.FromCompiled(compiled)
	if !ok {
		return fmt.Errorf("workflow delivery policy is not active for run %q", runID)
	}
	if run.Status == workflowledger.RunStatusSucceeded {
		return replayDeliveryRecord(ctx, repo, run, stdout)
	}
	if run.Status != workflowledger.RunStatusDeliveryPending {
		return fmt.Errorf("run is not waiting for delivery (status %q)", run.Status)
	}
	if !allowPublish {
		return fmt.Errorf("delivery requires --allow-publish")
	}
	release, err := claimWorkflowDeliveryRun(ctx, repo, runID)
	if err != nil {
		return err
	}
	defer release()

	identity, err := workflowspace.Resolve(ctx, root, workflowspace.Identity{
		BaseRef: run.BaseRef, BaseCommit: run.BaseCommit,
		WorktreeName: run.WorktreeName, Branch: "wf/" + run.WorktreeName,
	})
	if err != nil {
		return &delivery.RefusalError{Reason: "resolve delivery workspace: " + err.Error()}
	}
	gitDir, err := delivery.VerifyGitDir(ctx, identity.MainRoot, run.WorktreeName, identity.Root)
	if err != nil {
		return err
	}
	req := delivery.Request{
		RunID: runID, WorkflowDigest: run.WorkflowDigest, Policy: policy,
		Inputs: snapshot.Inputs, BaseCommit: run.BaseCommit, Branch: "wf/" + run.WorktreeName,
		GitCtx:    delivery.GitContext{Dir: identity.Root, GitDir: gitDir},
		OriginURL: run.RemoteURL,
	}
	// Bound one delivery attempt: a hung git push or gh call must not block
	// the CLI forever. The claim and status CAS stay on the caller's context.
	deliveryCtx, cancel := context.WithTimeout(ctx, workflowDeliveryTimeout)
	defer cancel()
	result, err := delivery.Deliver(deliveryCtx, repo, workflowDeliverGit, workflowDeliverNewPR(), req)
	if err != nil {
		fmt.Fprintln(stdout, delivery.FormatOutcome(result, err))
		if delivery.IsRefusal(err) {
			return settleDeliveryRefusal(ctx, repo, runID, err, stdout)
		}
		return err
	}
	return settleDeliverySuccess(ctx, repo, runID, result, stdout)
}

// replayDeliveryRecord prints the durable outcome of a run that already
// completed delivery.
func replayDeliveryRecord(ctx context.Context, repo workflowledger.Repository, run workflowledger.RunSnapshot, stdout io.Writer) error {
	rec, err := repo.GetDeliveryByIdempotencyKey(ctx, delivery.DeliveryKey(run.RunID, run.WorkflowDigest))
	if err != nil {
		return err
	}
	if rec.Status != "succeeded" && rec.Status != "no_diff" {
		return fmt.Errorf("run %q already succeeded but its delivery record has status %q", run.RunID, rec.Status)
	}
	fmt.Fprintln(stdout, delivery.FormatOutcome(delivery.Result{
		Mode: rec.Mode, BaseRef: rec.BaseRef, HeadRef: rec.HeadRef,
		CommitSHA: rec.CommitSHA, Provider: rec.Provider,
		RemoteID: rec.RemoteID, URL: rec.URL,
		Status: rec.Status, DiffRef: rec.DiffRef,
	}, nil))
	return nil
}

// claimWorkflowDeliveryRun acquires the run claim for delivery. Under the held
// execution file lock, a claim held by another holder is stale (an interrupted
// prior executor left it behind) and is force-released before re-claiming.
func claimWorkflowDeliveryRun(ctx context.Context, repo workflowledger.Repository, runID string) (func(), error) {
	holder := newWorkflowDeliveryHolder()
	if err := repo.ClaimRun(ctx, runID, holder); err != nil {
		if !errors.Is(err, workflowledger.ErrClaimHeld) {
			return nil, err
		}
		if err := repo.ClearRunClaim(ctx, runID); err != nil {
			return nil, err
		}
		if err := repo.ClaimRun(ctx, runID, holder); err != nil {
			return nil, err
		}
	}
	return func() { _ = repo.ReleaseRun(context.Background(), runID, holder) }, nil
}

// settleDeliveryRefusal CASes a permanently refused run to delivery_failed and
// prints the settled status line, then returns the refusal for a non-zero exit.
func settleDeliveryRefusal(ctx context.Context, repo workflowledger.Repository, runID string, refusal error, stdout io.Writer) error {
	fresh, getErr := repo.GetRun(ctx, runID)
	if getErr != nil {
		return fmt.Errorf("delivery refused: %v; read settled run: %w", refusal, getErr)
	}
	if casErr := repo.CompareAndSetRunStatus(ctx, runID, fresh.Version, workflowledger.RunStatusDeliveryFailed, nil); casErr != nil {
		return fmt.Errorf("delivery refused: %v; settle run to delivery_failed: %w", refusal, casErr)
	}
	fmt.Fprintf(stdout, "run_id=%s status=%s\n", runID, workflowledger.RunStatusDeliveryFailed)
	return refusal
}

// settleDeliverySuccess CASes a delivered run to succeeded (when it is still
// waiting) and prints the outcome.
func settleDeliverySuccess(ctx context.Context, repo workflowledger.Repository, runID string, result delivery.Result, stdout io.Writer) error {
	fresh, err := repo.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	if fresh.Status == workflowledger.RunStatusDeliveryPending {
		now := workflowDeliverNow()
		if err := repo.CompareAndSetRunStatus(ctx, runID, fresh.Version, workflowledger.RunStatusSucceeded, &now); err != nil {
			return err
		}
	}
	fmt.Fprintln(stdout, delivery.FormatOutcome(result, nil))
	return nil
}

// newWorkflowDeliveryHolder mints the run-claim holder for a CLI delivery
// attempt.
func newWorkflowDeliveryHolder() string {
	var value [10]byte
	_, _ = workflowDeliverRandom(value[:])
	return "wfdel-" + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(value[:])
}
