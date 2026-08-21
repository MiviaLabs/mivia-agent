package localengine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	workflowspace "github.com/MiviaLabs/mivia-agent/internal/workflows/workspace"
)

// Deliver implements workflowledger.Engine.
func (e *Engine) Deliver(ctx context.Context, runID string, allowPublish bool) (workflowledger.DeliverResult, error) {
	if e == nil || e.Repo == nil {
		return workflowledger.DeliverResult{}, fmt.Errorf("workflow engine is incomplete")
	}
	if !allowPublish {
		emitProgress(controller.ProgressEvent{
			Kind: controller.ProgressDeliveryRefused, RunID: runID,
			StepID:    "deliver",
			Detail:    "delivery requires allow_publish=true",
			Timestamp: time.Now(),
		})
		return workflowledger.DeliverResult{RunID: runID, Refused: true, Reason: "delivery requires allow_publish=true"}, nil
	}
	run, err := e.Repo.GetRun(ctx, runID)
	if err != nil {
		if errors.Is(err, workflowledger.ErrNotFound) {
			return workflowledger.DeliverResult{}, fmt.Errorf("workflow run %q not found", runID)
		}
		return workflowledger.DeliverResult{}, err
	}
	if run.Status == workflowledger.RunStatusSucceeded {
		return e.replayDelivery(ctx, run)
	}
	if run.Status != workflowledger.RunStatusDeliveryPending && run.Status != workflowledger.RunStatusDeliveryFailed {
		return workflowledger.DeliverResult{}, fmt.Errorf("run is not waiting for delivery (status %q)", run.Status)
	}
	return e.deliverPending(ctx, run)
}

func (e *Engine) replayDelivery(ctx context.Context, run workflowledger.RunSnapshot) (workflowledger.DeliverResult, error) {
	rec, err := e.Repo.GetDeliveryByIdempotencyKey(ctx, delivery.DeliveryKey(run.RunID, run.WorkflowDigest))
	if err != nil {
		// A succeeded run without a readable delivery record must surface the
		// loss, not silently report success with empty URL/Mode: the CLI
		// replay path propagates this error, and the engine must not diverge.
		return workflowledger.DeliverResult{}, fmt.Errorf("replay delivery for %q: %w", run.RunID, err)
	}
	return workflowledger.DeliverResult{RunID: run.RunID, Status: string(run.Status), URL: rec.URL, Mode: rec.Mode}, nil
}

func (e *Engine) deliverPending(ctx context.Context, run workflowledger.RunSnapshot) (workflowledger.DeliverResult, error) {
	return e.deliverPendingWithStackGate(ctx, run, true)
}

// deliverPendingDirect delivers a delivery_pending run through the engine's
// own publish machinery WITHOUT the drive-before-delivery gate. It mirrors
// the CLI's workflowStackDeliverRun = deliverRunWithStore (stack_merge.go):
// the stack drive calls it for the final integration run, whose delivery
// must not be refused as "the plan run of an undriven stack". The drive
// itself already verified every chunk merged before admitting the
// integration run, and the integration run's own decompose output can
// legitimately re-plan the merged suite as mode=multi (the run re-runs the
// workflow's decompose inline in single mode) - the operator gate would
// misread that as a fresh undriven stack and refuse the integration run
// forever, leaving it delivery_pending for the auto-drive to cascade on.
func (e *Engine) deliverPendingDirect(ctx context.Context, run workflowledger.RunSnapshot) (workflowledger.DeliverResult, error) {
	return e.deliverPendingWithStackGate(ctx, run, false)
}

// clearDeliveryAbandon clears the delivery fence's abandon residue for runID
// so this delivery's fenced writes pass (see deliverPendingWithStackGate for
// why a poisoned fence would otherwise fail every delivery write).
func (e *Engine) clearDeliveryAbandon(runID string) {
	_ = e.ctrlRepo()
	e.mu.Lock()
	if e.fence != nil {
		e.fence.clearAbandon(runID)
	}
	e.mu.Unlock()
}

func (e *Engine) deliverPendingWithStackGate(ctx context.Context, run workflowledger.RunSnapshot, enforceStackGate bool) (workflowledger.DeliverResult, error) {
	runID := run.RunID
	// A delivery_pending/delivery_failed run has no live controller (the
	// controller parked and exited), so no dying goroutine can settle it:
	// clear any abandon residue the fence carries for this run - e.g. an
	// Interrupt that landed just as the controller parked at delivery_pending
	// - so this delivery's fenced writes pass. Mirrors clearAbandon in
	// buildResumeController for resumed runs. Without it a poisoned fence
	// fails every delivery write with ErrConflict and the run stays
	// delivery_pending forever (resume refuses delivery_pending before
	// buildResumeController can clear the fence).
	e.clearDeliveryAbandon(runID)
	raw, err := e.Repo.GetRunSnapshot(ctx, runID)
	if err != nil {
		return workflowledger.DeliverResult{}, err
	}
	snapshot, err := workflowledger.UnmarshalSnapshot(raw)
	if err != nil {
		return workflowledger.DeliverResult{}, err
	}
	wf, _, err := definition.ParseWorkflowTOML(snapshot.DefinitionTOML, run.WorkflowName+".toml")
	if err != nil {
		return workflowledger.DeliverResult{}, err
	}
	compiled, err := definition.CompileForResume(&wf)
	if err != nil {
		return workflowledger.DeliverResult{}, err
	}
	// Drive-before-delivery gate (mirrors the CLI's
	// classifyStackPlanRunDelivery): the plan run of a multi-chunk stack must
	// not be published while its stack is undriven or incomplete - publishing
	// it would abandon the confirmed chunk work while reporting the plan run
	// succeeded. The run stays delivery_pending (resumable via the drive or
	// `mivia stack drive`). The drive's own integration-run delivery
	// (deliverPendingDirect) skips this gate: the drive verified completion.
	if enforceStackGate {
		if reason := e.undrivenPlanRunReason(ctx, e.Repo, runID, compiled); reason != "" {
			e.emitDeliveryRefused(runID, reason)
			return workflowledger.DeliverResult{}, fmt.Errorf("workflow run %q: %s", runID, reason)
		}
	}
	policy, ok := delivery.FromCompiled(compiled)
	if !ok {
		e.emitDeliveryRefused(runID, fmt.Sprintf("workflow delivery policy is not active for run %q", runID))
		return workflowledger.DeliverResult{}, fmt.Errorf("workflow delivery policy is not active for run %q", runID)
	}
	// Serialize in-process deliveries per run: two concurrent tool calls must
	// not both publish to the shared workspace branch. The claim probe below
	// still guards cross-host contention, but a sibling call in THIS engine
	// would otherwise clear our live claim mid-publish.
	e.mu.Lock()
	if e.delivering == nil {
		e.delivering = make(map[string]string)
	}
	if holder, busy := e.delivering[runID]; busy {
		e.mu.Unlock()
		return workflowledger.DeliverResult{}, fmt.Errorf("workflow run %q delivery already in progress (holder %s)", runID, holder)
	}
	e.delivering[runID] = "in-flight"
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.delivering, runID)
		e.mu.Unlock()
	}()
	holder, release, err := e.claimDelivery(ctx, runID)
	if err != nil {
		return workflowledger.DeliverResult{}, err
	}
	defer release()
	ctx = workflowledger.ContextWithClaimHolder(ctx, holder)
	// Refresh the durable on-disk trace after delivery settles (success,
	// failure, or refusal), so .mivia/runs carries the delivery hint.
	defer e.writeRunTrace(runID)
	return e.publishDelivery(ctx, run, snapshot, policy)
}

func (e *Engine) claimDelivery(ctx context.Context, runID string) (string, func(), error) {
	holder := "wfdel-" + randomToken(5)
	// Never clear a held claim: the holder may be another host mid-publish
	// (clearing would let both hosts publish to the same branch) or a
	// crashed deliverer. In-process deliveries are already serialized by the
	// delivering map, so a held claim here is cross-host: refuse and let the
	// operator settle it (the CLI's workflow deliver takes over an EXPIRED
	// claim via lease; use --force to bypass the lease explicitly).
	if err := e.Repo.ClaimRun(ctx, runID, holder); err != nil {
		if errors.Is(err, workflowledger.ErrClaimHeld) {
			return "", nil, fmt.Errorf("workflow run %q is being delivered by another host or has a fresh delivery claim; retry after it settles (mivia workflow deliver --force takes over an expired claim)", runID)
		}
		return "", nil, err
	}
	return holder, func() { _ = e.Repo.ReleaseRun(context.Background(), runID, holder) }, nil
}

func (e *Engine) publishDelivery(ctx context.Context, run workflowledger.RunSnapshot, snapshot workflowledger.Snapshot, policy delivery.Policy) (workflowledger.DeliverResult, error) {
	runID := run.RunID
	ctx = workflowledger.ContextWithRunID(ctx, runID)
	repo := e.ctrlRepo()
	git, pr := e.Git, e.PR
	if git == nil {
		git = delivery.RealGit{}
	}
	if pr == nil {
		pr = delivery.GitHubCLI{}
	}
	timeout := e.DeliveryTimeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	deliveryCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// Resolve the run's delivery workspace and verify its real git directory.
	// The CLI path does the same (workflow_deliver.go: Resolve + VerifyGitDir);
	// an empty GitDir would make pinnedEnv emit GIT_DIR= and every git command
	// would fail against an invalid empty path.
	gitCtx, err := e.deliveryGitCtx(ctx, run)
	if err != nil {
		// A deliveryGitCtx refusal is permanent (same contract as delivery.Deliver
		// refusals): settle the run to delivery_failed so it does not wedge in
		// delivery_pending forever.
		if delivery.IsRefusal(err) {
			e.settleDeliveryFailed(ctx, repo, runID)
			e.emitDeliveryRefused(runID, err.Error())
			return workflowledger.DeliverResult{RunID: runID, Status: string(workflowledger.RunStatusDeliveryFailed), Refused: true, Reason: err.Error()}, nil
		}
		return workflowledger.DeliverResult{}, err
	}
	// Every numbered delivery stage is published through the package progress
	// sink as one workflow_delivery_stage event (nil sink no-ops), so the
	// session surface observes the delivery attempt the same way the CLI stage
	// printer does. The engine's stage sink is wired by the same
	// SetProgressSink/NewBusProgressSink hook the terminal progress events use.
	inputs := delivery.CloneInputs(snapshot.Inputs)
	dreq := delivery.Request{
		RunID: runID, WorkflowDigest: run.WorkflowDigest, Policy: policy,
		Inputs: inputs, BaseCommit: run.BaseCommit,
		Branch: "wf/" + run.WorktreeName, GitCtx: gitCtx,
		OriginURL: run.RemoteURL,
		Stage:     e.deliveryStageEmitter(runID),
	}
	result, err := delivery.Deliver(deliveryCtx, repo, git, pr, dreq)
	if err != nil {
		res, handled, rerr := e.settleDeliveryAttemptError(ctx, deliveryCtx, repo, runID, policy, err)
		if handled {
			return res, nil
		}
		if rerr != nil {
			return workflowledger.DeliverResult{}, rerr
		}
		return workflowledger.DeliverResult{}, err
	}
	if err := e.settleDeliverySucceeded(ctx, repo, runID); err != nil {
		return workflowledger.DeliverResult{}, err
	}
	// A split (checkChunkDiffSize) can leave a deferred branch nobody ever
	// pushes unless something publishes it - see delivery.EnsureFollowUpPublished's
	// doc comment. This session-engine path needs the same call the CLI's
	// deliverRunWithStore makes; EnsureFollowUpPublished is idempotent, so a
	// stack driver's separate call for the same run stays safe. Best-effort:
	// a failure here does not undo the just-settled delivery.
	if _, _, _, _, ferr := delivery.EnsureFollowUpPublished(ctx, git, pr, gitCtx.Dir, repo, run, runID, nil); ferr != nil {
		log.Printf("workflow %s delivered but its follow-up PR could not be published: %v", runID, ferr)
	}
	return workflowledger.DeliverResult{RunID: runID, Status: string(workflowledger.RunStatusSucceeded), URL: result.URL, Mode: result.Mode}, nil
}

// settleDeliveryAttemptError routes one failed delivery attempt: a refusal
// settles delivery_failed (handled), an expired attempt bound is returned
// unhandled so the run stays delivery_pending (retryable), and anything
// repairable reopens the policy's repair step.
func (e *Engine) settleDeliveryAttemptError(ctx, deliveryCtx context.Context, repo workflowledger.Repository, runID string, policy delivery.Policy, err error) (workflowledger.DeliverResult, bool, error) {
	if delivery.IsRefusal(err) {
		e.settleDeliveryFailed(ctx, repo, runID)
		return workflowledger.DeliverResult{RunID: runID, Status: string(workflowledger.RunStatusDeliveryFailed), Refused: true, Reason: err.Error()}, true, nil
	}
	// The attempt's own bound fired (a hung git push or gh call hit
	// DeliveryTimeout) or the caller cancelled the attempt: a transport
	// fault, not a condition in the change - no agent can repair it -
	// so the run stays delivery_pending (retryable), mirroring the CLI's
	// settleDeliveryError guard. provider.IsTransient returns false for a
	// bare context deadline, so without this check the error routes to
	// the repair step and burns a repair cycle per timeout.
	if deliveryCtx.Err() != nil {
		return workflowledger.DeliverResult{}, false, err
	}
	return routeDeliveryRepair(ctx, repo, runID, policy, err)
}

// settleDeliveryFailed CASes runID to delivery_failed (best-effort) and drops
// its recorded worktree identity, so a permanently refused delivery does not
// leave e.worktrees carrying an entry no future call will ever look up again.
func (e *Engine) settleDeliveryFailed(ctx context.Context, repo workflowledger.Repository, runID string) {
	if fresh, getErr := repo.GetRun(ctx, runID); getErr == nil {
		_ = repo.CompareAndSetRunStatus(ctx, runID, fresh.Version, workflowledger.RunStatusDeliveryFailed, nil)
	}
	e.forgetWorktree(runID)
}

// settleDeliverySucceeded CASes a still-delivery_pending run to succeeded,
// publishes the terminal run_finished event, and drops the run's recorded
// worktree identity now that the run has reached a terminal status.
func (e *Engine) settleDeliverySucceeded(ctx context.Context, repo workflowledger.Repository, runID string) error {
	fresh, err := repo.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	if fresh.Status == workflowledger.RunStatusDeliveryPending {
		now := time.Now()
		if err := repo.CompareAndSetRunStatus(ctx, runID, fresh.Version, workflowledger.RunStatusSucceeded, &now); err != nil {
			return err
		}
		// Delivery completion settles outside the controller (which parked at
		// delivery_pending and emitted no run_finished), so publish the
		// terminal event here.
		emitDeliveredRunFinished(runID)
	}
	e.forgetWorktree(runID)
	return nil
}

// routeDeliveryRepair routes a repairable, in-change delivery failure back
// into the workflow when the policy names a repair step. Any rejection of
// the work itself - a PR-metadata defect, an over-limit delivered diff, a
// commit hook that refuses the change, or any other condition in the change
// - routes the same way. delivery.RepairTarget is the single classifier that
// maps each class to the step the workflow names (on_pr_metadata_failure,
// on_diff_size_failure, then on_failure), shared with the CLI so a rejection
// routes to the same step on both paths (mirrors the CLI's
// settleDeliveryError in internal/cli/workflow_deliver.go). A transport
// fault - marked transient by the provider layer - is never routed: no agent
// can repair it, so the run stays delivery_pending for a retry, exactly as
// before. The boolean reports whether the failure was routed (handled); a
// non-nil error means the routing itself failed and the caller must surface
// it.
func routeDeliveryRepair(ctx context.Context, repo workflowledger.Repository, runID string, policy delivery.Policy, err error) (workflowledger.DeliverResult, bool, error) {
	step := delivery.RepairTarget(err, policy)
	// A git/gh transport fault is no more repairable than a provider one:
	// provider.IsTransient does not know git's texts, so both classifiers
	// gate the dispatch (mirrors the CLI's deliveryFaultTransient).
	if step == "" || provider.IsTransient(err) || delivery.IsTransportFault(err) {
		return workflowledger.DeliverResult{}, false, nil
	}
	if rerr := delivery.ReopenForRepair(ctx, repo, runID, step, policy.MaxRepairs, err, io.Discard); rerr != nil {
		return workflowledger.DeliverResult{}, false, rerr
	}
	fresh, gerr := repo.GetRun(ctx, runID)
	if gerr != nil {
		return workflowledger.DeliverResult{}, false, gerr
	}
	return workflowledger.DeliverResult{RunID: runID, Status: string(fresh.Status)}, true, nil
}

// deliveryGitCtx resolves the run's delivery workspace and verifies its real
// git directory, mirroring the CLI's workflow deliver path (workflowspace.
// Resolve + delivery.VerifyGitDir). The engine records the worktree identity
// at start/resume; runs admitted by another engine (or before the identity was
// recorded) are resolved from the durable run record. A run without a recorded
// worktree cannot publish and is refused permanently.
func (e *Engine) deliveryGitCtx(ctx context.Context, run workflowledger.RunSnapshot) (delivery.GitContext, error) {
	if run.WorktreeName == "" {
		return delivery.GitContext{}, &delivery.RefusalError{Reason: fmt.Sprintf("workflow run %q has no recorded worktree; delivery requires a run worktree", run.RunID)}
	}
	identity, ok := e.worktreeIdentity(run.RunID)
	if !ok || identity.Root == "" || identity.MainRoot == "" {
		var err error
		identity, err = workflowspace.Resolve(ctx, e.WorkspaceRoot, workflowspace.Identity{
			BaseRef: run.BaseRef, BaseCommit: run.BaseCommit,
			WorktreeName: run.WorktreeName, Branch: "wf/" + run.WorktreeName,
		})
		if err != nil {
			return delivery.GitContext{}, &delivery.RefusalError{Reason: "resolve delivery workspace: " + err.Error()}
		}
	}
	gitDir, err := delivery.VerifyGitDir(ctx, identity.MainRoot, run.WorktreeName, identity.Root)
	if err != nil {
		return delivery.GitContext{}, err
	}
	return delivery.GitContext{Dir: identity.Root, GitDir: gitDir}, nil
}

// settleRunFailure best-effort settles a run whose execution stopped with a
// non-cancel error. If another holder owns the run (claim contention), it is
// left alone: that holder is the live executor and will settle or continue it.
// An abandoned run is also left non-terminal: Interrupt owns that outcome and
// the run must stay resumable.
func (e *Engine) settleRunFailure(runID string, runErr error) {
	log.Printf("workflow engine: run %s stopped with error: %v", runID, runErr)
	e.mu.Lock()
	abandoned := e.fence != nil && e.fence.isAbandoned(runID)
	e.mu.Unlock()
	if abandoned {
		return // Interrupt owns this run's outcome; keep it non-terminal for resume.
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	holder := "wfsettle-" + randomToken(5)
	if err := e.claimOrTakeoverExpired(ctx, runID, holder); err != nil {
		return // another holder owns the run
	}
	defer func() { _ = e.Repo.ReleaseRun(context.Background(), runID, holder) }()
	fresh, err := e.Repo.GetRun(ctx, runID)
	if err != nil || workflowledger.IsTerminalRunStatus(fresh.Status) {
		return
	}
	if err := e.Repo.CompareAndSetRunStatus(ctx, runID, fresh.Version, workflowledger.RunStatusFailed, nil); err == nil {
		e.forgetWorktree(runID)
	}
}
