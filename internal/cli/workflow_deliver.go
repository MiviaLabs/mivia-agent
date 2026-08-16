package cli

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
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
	// workflowDeliveryClaimHeartbeat is how often a running delivery attempt
	// re-claims its run with the same holder. The attempt can run for
	// workflowDeliveryTimeout (10m) while the claim lease lasts only
	// workflowledger.DefaultClaimLease: without a refresh, a second host
	// could take the claim over mid-publish (DC-2 double publish). One third
	// of the lease keeps the claim comfortably inside it. A package var so
	// tests can shorten it.
	workflowDeliveryClaimHeartbeat = workflowledger.DefaultClaimLease / 3
)

// executeWorkflowDeliver publishes the result of one delivery_pending run via
// the `workflow deliver` subcommand. It mirrors executeWorkflowRun's preamble:
// resolve the workspace and config, open the workflow store, then acquire the
// workflow execution file lock (beginWorkflowExecution also installs hooks)
// before handing off to deliverRunWithStore, which must NOT re-acquire the
// lock. On success it prints the settled run status like executeWorkflowRun.
func executeWorkflowDeliver(ctx context.Context, runID, root, configPath string, allowPublish, force bool, stdout, stderr io.Writer) error {
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
	applyPrivacyPolicy(res)
	applyWorkflowStoreRoot(res, work.Abs)
	store, repo, closeFn, err := openWorkflowStore(work.Abs, res.Subagents)
	if err != nil {
		return err
	}
	defer closeFn()
	finishExecution, err := beginWorkflowExecutionBounded(work.Abs, contextStorePath(work.Abs, res.Subagents), runID, workflowResolutionLockWait)
	if err != nil {
		return err
	}
	defer finishExecution()
	// Drive-before-delivery gate: an incomplete multi-chunk stack's plan run
	// must be refused (checked with the execution lock held, so the verdict
	// cannot race a concurrent drive); a COMPLETE stack's plan run settles
	// below via the same skip/deliver branches the session recovery sweep
	// uses, instead of being refused forever (F11).
	switch classifyStackPlanRunDelivery(ctx, work.Abs, store, repo, runID, true) {
	case stackPlanRunIncomplete:
		return errUndrivenStackPlanRun(runID)
	case stackPlanRunComplete:
		if skipParkedPlanRunPublication(ctx, store, repo, runID) {
			if err := settlePlanRunSkippedDelivery(ctx, repo, runID); err != nil {
				return err
			}
			settled, err := repo.GetRun(ctx, runID)
			if err != nil {
				return err
			}
			fmt.Fprintf(stdout, "run_id=%s status=%s plan PR not created (delivery.deliver_plan_run=false); plan and artifacts recorded in the ledger\n", runID, settled.Status)
			return nil
		}
		// deliver_plan_run=true and the stack is complete: fall through to
		// deliverRunWithStore below, which publishes the plan run's own PR.
	}
	if err := deliverRunWithStore(ctx, work.Abs, res, store, repo, runID, allowPublish, force, stdout, stderr); err != nil {
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
func deliverRunWithStore(ctx context.Context, root string, res *config.Resolved, store *storage.SQLite, repo workflowledger.Repository, runID string, allowPublish, force bool, stdout, stderr io.Writer) error {
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
	if run.Status != workflowledger.RunStatusDeliveryPending && run.Status != workflowledger.RunStatusDeliveryFailed {
		return fmt.Errorf("run is not waiting for delivery (status %q)", run.Status)
	}
	if !allowPublish {
		return fmt.Errorf("delivery requires --allow-publish")
	}
	release, err := claimWorkflowDeliveryRun(ctx, repo, runID, force)
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
		Inputs: delivery.CloneInputs(snapshot.Inputs), BaseCommit: run.BaseCommit, Branch: "wf/" + run.WorktreeName,
		GitCtx:    delivery.GitContext{Dir: identity.Root, GitDir: gitDir},
		OriginURL: run.RemoteURL,
		Stage:     workflowDeliveryStagePrinter(stderr),
	}
	// Bound one delivery attempt: a hung git push or gh call must not block
	// the CLI forever. The claim and status CAS stay on the caller's context.
	deliveryCtx, cancel := context.WithTimeout(ctx, workflowDeliveryTimeout)
	defer cancel()
	result, err := delivery.Deliver(deliveryCtx, repo, workflowDeliverGit, workflowDeliverNewPR(), req)
	// The settle CAS runs on a context that cannot expire mid-transition (S4):
	// a publish that consumed the full attempt bound must still settle the run
	// (succeeded, or routed for repair) instead of stranding it delivery_pending
	// on a deadline-exceeded CAS. settleDeliveryError still classifies the
	// attempt with deliveryCtx, whose deadline it must observe.
	settleCtx := context.WithoutCancel(ctx)
	if err != nil {
		return settleDeliveryError(settleCtx, repo, runID, policy, stdout, deliveryCtx, result, err)
	}
	if serr := settleDeliverySuccess(settleCtx, repo, runID, result, stdout, stderr); serr != nil {
		return serr
	}
	publishDeliveryFollowUp(settleCtx, repo, identity.Root, run, runID, stdout, stderr)
	return nil
}

// publishDeliveryFollowUp pushes a split's deferred branch and opens its
// follow-up PR, if delivery left one pending. A split (checkChunkDiffSize)
// can happen on ANY successful delivery, not only a multi-chunk stack chunk
// the stack driver later visits - a run deliverRunWithStore delivers
// directly (a plain `workflow run`, `workflow deliver`, or a stack chunk
// admitted before the driver's own follow-up pass runs) must not leave a
// deferred branch it created unpublished. delivery.EnsureFollowUpPublished
// is idempotent (FindByHead before Create), so the stack driver's later,
// separate call for the same run is safe. Best-effort: a failure here does
// not undo the already-settled delivery.
func publishDeliveryFollowUp(ctx context.Context, repo workflowledger.Repository, worktreeRoot string, run workflowledger.RunSnapshot, runID string, stdout, stderr io.Writer) {
	stdoutFn := func(s string) { fmt.Fprint(stdout, s) }
	if _, _, _, _, err := delivery.EnsureFollowUpPublished(ctx, workflowDeliverGit, workflowDeliverNewPR(), worktreeRoot, repo, run, runID, stdoutFn); err != nil {
		fmt.Fprintf(stderr, "warning: run %s delivered but its follow-up PR could not be published: %v\n", runID, err)
	}
}

// workflowDeliveryStagePrinter returns the delivery stage observer for the
// CLI deliver path: one `delivery stage=<name> detail=<detail>` line per
// stage on stderr. It returns nil for io.Discard so silent CLI runs write
// nothing. The deliver path is single-threaded, so no mutex guards the writer.
func workflowDeliveryStagePrinter(stderr io.Writer) func(stage, detail string) {
	if stderr == nil || stderr == io.Discard {
		return nil
	}
	return func(stage, detail string) {
		fmt.Fprintf(stderr, "delivery stage=%s detail=%s\n", stage, detail)
	}
}

// settleDeliveryError handles a failed delivery attempt: it prints the
// outcome, then routes the failure to refusal settlement, the attempt-bound
// pass-through (timeout or caller cancellation), or repair dispatch, and
// returns the error deliverRunWithStore should surface so the exit behavior
// is unchanged.
func settleDeliveryError(ctx context.Context, repo workflowledger.Repository, runID string, policy delivery.Policy, stdout io.Writer, deliveryCtx context.Context, result delivery.Result, err error) error {
	fmt.Fprintln(stdout, delivery.FormatOutcome(result, err))
	if delivery.IsRefusal(err) {
		return settleDeliveryRefusal(ctx, repo, runID, err, stdout)
	}
	// The attempt's own bound fired: a hung git push or gh call hit the
	// workflowDeliveryTimeout (or the caller cancelled the attempt). That
	// is a transport fault, not a condition in the change - no agent can
	// repair it - so the run stays delivery_pending (retryable), exactly
	// what delivery.Deliver already contracts (see
	// TestDeliverFetchFailureIsTransient), and no auto-failure record or
	// repair dispatch is written. A later deliver succeeds once the
	// network is back.
	if deliveryCtx.Err() != nil {
		return err
	}
	// A transport fault is not a condition in the change. An unreachable
	// origin, a gh outage, a reset push: no agent can repair any of them,
	// and sending one to try burns model budget and the run deadline
	// before delivery fails again the same way. Such a run stays at
	// delivery_pending, which is what delivery.Deliver already contracts
	// (see TestDeliverFetchFailureIsTransient), so a later deliver
	// succeeds once the network is back.
	//
	// What DOES reach a repair step is a rejection of the work itself: a
	// commit hook that refuses the change, a gate that finds a violation.
	//
	// A PR-metadata failure is such a condition in the change: the agent's
	// title or summary violates the workspace pr-title policy. An over-limit
	// delivered diff is another: the chunk exceeds the stacking hard limit,
	// and only a worktree edit can shrink it. delivery.RepairTarget is the
	// single classifier that maps each rejection class to the step the
	// workflow names (on_pr_metadata_failure, on_diff_size_failure, then
	// on_failure); the CLI and the local engine share it, so a rejection
	// routes to the same step on both paths.
	if step := delivery.RepairTarget(err, policy); step != "" && !deliveryFaultTransient(err) {
		recordAutoDeliveryFailure(ctx, repo, runID, err)
		return delivery.ReopenForRepair(ctx, repo, runID, step, policy.MaxRepairs, err, stdout)
	}
	// No repair step resolves for this failure (or it is transient): the run
	// stays delivery_pending. A durable, non-transient failure still needs a
	// recorded cause - pre-commit rejections (PR metadata, commit subject)
	// deliberately write no in-flight record, so without this the run would
	// sit at delivery_pending with nothing `workflow status` can explain.
	// Transient transport faults stay unrecorded: they say nothing about the
	// change and a later deliver succeeds.
	if !deliveryFaultTransient(err) {
		recordAutoDeliveryFailure(ctx, repo, runID, err)
	}
	return err
}

// deliveryFaultTransient reports whether a delivery failure is a transport
// fault no agent can repair: a provider-side transient (provider.IsTransient)
// or a git/gh network death (delivery.IsTransportFault). provider's phrase
// list is provider-domain and does not know git's texts ("Could not resolve
// host", "Connection timed out"), which previously misrouted a fetch failure
// to the repair step and wrote a failed record for it.
func deliveryFaultTransient(err error) bool {
	return provider.IsTransient(err) || delivery.IsTransportFault(err)
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
// execution file lock, a claim held by another holder is either a live
// cross-host deliverer or an expired leftover: mirroring the resume handoff,
// the claim is taken over only when its lease has EXPIRED (or immediately with
// --force). A held, unexpired claim is a live publisher - refusing beats
// force-releasing and double-publishing the same run.
func claimWorkflowDeliveryRun(ctx context.Context, repo workflowledger.Repository, runID string, force bool) (func(), error) {
	holder := newWorkflowDeliveryHolder()
	if force {
		if err := repo.TakeoverRunClaim(ctx, runID, holder); err != nil {
			return nil, err
		}
	} else {
		err := repo.TakeoverExpiredRunClaim(ctx, runID, holder, workflowledger.DefaultClaimLease)
		if errors.Is(err, workflowledger.ErrClaimNotHeld) {
			err = repo.ClaimRun(ctx, runID, holder)
		}
		if errors.Is(err, workflowledger.ErrClaimHeld) {
			return nil, fmt.Errorf("workflow run %q is being delivered by another host or has a fresh claim; retry after the lease expires or pass --force after the prior deliverer stopped", runID)
		}
		if err != nil {
			return nil, err
		}
	}
	// The attempt can run for workflowDeliveryTimeout (10m) while the claim
	// lease lasts only DefaultClaimLease, so the claim is refreshed while
	// the attempt runs. The release func stops and joins the heartbeat BEFORE
	// releasing the claim (LIFO, mirroring the controller): a tick that landed
	// after ReleaseRun would re-create the claim row (sqlite ClaimRun INSERTs
	// when no row exists) and re-arm a dead lease that blocks takeover for
	// another lease window.
	stopHeartbeat := startWorkflowDeliveryClaimHeartbeat(ctx, repo, runID, holder)
	return func() {
		stopHeartbeat()
		_ = repo.ReleaseRun(context.Background(), runID, holder)
	}, nil
}

// startWorkflowDeliveryClaimHeartbeat refreshes the delivery run claim with
// the SAME holder while the attempt runs, so a publish that outlives the claim
// lease cannot be taken over by a second host mid-publish (DC-2). A failed
// refresh is terminal for the heartbeat: it stops instead of retry-spinning
// (the claim was taken or the store failed; hammering it cannot help). The
// returned stop func closes the stop channel and WAITS for the goroutine to
// exit, so the caller releases the claim only after the last possible refresh
// has run.
func startWorkflowDeliveryClaimHeartbeat(ctx context.Context, repo workflowledger.Repository, runID, holder string) (stop func()) {
	stopCh := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() { _ = recover() }()
		ticker := time.NewTicker(workflowDeliveryClaimHeartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := repo.ClaimRun(ctx, runID, holder); err != nil {
					return
				}
			case <-stopCh:
				return
			}
		}
	}()
	return func() {
		close(stopCh)
		wg.Wait()
	}
}

// settleDeliveryRefusal CASes a permanently refused run to delivery_failed,
// durably records the refusal reason (so `workflow status` explains the
// failure), and prints the settled status line, then returns the refusal for
// a non-zero exit. A refused run is recoverable: a later workflow deliver
// re-runs eligibility and re-opens it when the refusal condition clears.
func settleDeliveryRefusal(ctx context.Context, repo workflowledger.Repository, runID string, refusal error, stdout io.Writer) error {
	// Durably record the refusal reason FIRST (contract with
	// recordDeliveryRefusal, defined in workflow_delivery_record.go): a
	// refusal must never settle delivery_failed without its recorded cause.
	if recErr := recordDeliveryRefusal(ctx, repo, runID, refusal); recErr != nil {
		return fmt.Errorf("delivery refused: %v; record refusal reason: %w", refusal, recErr)
	}
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
// waiting) and prints the outcome. A transition to succeeded emits one
// run_finished(succeeded) progress line on stderr so non-interactive
// consumers see the terminal event.
func settleDeliverySuccess(ctx context.Context, repo workflowledger.Repository, runID string, result delivery.Result, stdout, stderr io.Writer) error {
	fresh, err := repo.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	if fresh.Status == workflowledger.RunStatusDeliveryPending {
		now := workflowDeliverNow()
		if err := repo.CompareAndSetRunStatus(ctx, runID, fresh.Version, workflowledger.RunStatusSucceeded, &now); err != nil {
			return err
		}
		(&workflowProgressWriter{w: stderr}).Emit(controller.ProgressEvent{
			Kind: controller.ProgressRunFinished, RunID: runID, Detail: "succeeded",
			Timestamp: time.Now(),
		})
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
