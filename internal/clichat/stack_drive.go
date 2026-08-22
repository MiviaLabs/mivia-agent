package clichat

// mivia stack drive (plan D2/D3/D8, §5a): the driver loop. On start it
// reconciles every chunk task against its run and git merge state (idempotent
// recovery), then admits chunk runs in topological order with stable
// admission keys, honors the merge policy (A approve / B auto), and finishes
// with one full-suite integration run. Ledger queries and state helpers live
// in stack_state.go.

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"github.com/MiviaLabs/mivia-agent/internal/cliworkflow"
	"io"
	"path/filepath"
	"sync"
	"time"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
)

// stackDriveClaimHeartbeat is how often an active `stack drive` invocation
// refreshes its own claim (mirrors workflowDeliveryClaimHeartbeat in
// workflow_deliver.go: a fraction of the lease, so a single missed tick
// under transient store contention still leaves margin before the lease
// actually expires).
const stackDriveClaimHeartbeat = workflowledger.DefaultClaimLease / 3

// loadStackDrivePlanInputs resolves the stack id and loads its plan run's
// declared inputs, so chunk runs can replay them (D3). The driver never runs
// the plan run itself, but every chunk run replays the plan run's declared
// inputs, so this must happen before prepare (required-input validation).
func loadStackDrivePlanInputs(workspaceRoot, configPath, name, stackFlag string) (map[string]string, error) {
	_, repo, closeEarly, err := OpenStackLedgerFunc(workspaceRoot, configPath)
	if err != nil {
		return nil, fmt.Errorf("stack drive: %w", err)
	}
	defer closeEarly()
	stackID, err := ResolveStackIDFunc(repo, name, stackFlag)
	if err != nil {
		return nil, err
	}
	planInputs, err := stackPlanInputs(repo, stackID)
	if err != nil {
		return nil, fmt.Errorf("stack drive: %w", err)
	}
	return planInputs, nil
}

// runStackDrive parses `stack drive <workflow> [--stack <id>]` and runs the
// driver loop.
func runStackDrive(args []string, workspaceRoot, configPath string, stdout, stderr io.Writer) error {
	name, stackFlag, rest, err := ParseStackWorkflowArgsFunc(args)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return fmt.Errorf("stack drive: unexpected argument %q", rest[0])
	}
	planInputs, err := loadStackDrivePlanInputs(workspaceRoot, configPath, name, stackFlag)
	if err != nil {
		return err
	}

	rawInputs := make([]string, 0, len(planInputs))
	for k, v := range planInputs {
		rawInputs = append(rawInputs, k+"="+v)
	}
	prepared, err := cliworkflow.PrepareWorkflowRun(name, workspaceRoot, configPath, rawInputs)
	if err != nil {
		return fmt.Errorf("stack drive: %w", err)
	}
	defer prepared.CloseFn()
	logMCPWarnings(stderr, prepared.Res)

	wf := prepared.Compiled
	if wf.Stacking == nil || !wf.Stacking.Enabled {
		return fmt.Errorf("stack drive: workflow %q is not stacking-enabled; declare a [stacking] table with plan_step and implement_step in its definition", name)
	}
	if !wf.DeliveryActive() {
		return fmt.Errorf("stack drive: workflow %q has no active delivery policy; stacking requires chunk PR delivery", name)
	}
	stackID, releaseClaim, err := resolveAndClaimStackDrive(context.Background(), prepared, name, stackFlag)
	if err != nil {
		return err
	}
	defer releaseClaim()
	return driveStackOnePass(prepared, stackID, planInputs, stdout, stderr)
}

// driveStackOnePass runs one `mivia stack drive` pass under an already-
// claimed stack: load the plan, drive its chunks, admit follow-ups and the
// next continuation wave, then settle the integration run and the plan run
// itself once the stack is complete. Split out of runStackDrive to keep both
// under the repo's per-function line budget.
func driveStackOnePass(prepared *cliworkflow.PreparedWorkflowRun, stackID string, planInputs map[string]string, stdout, stderr io.Writer) error {
	ledger := workflowledger.NewStore(prepared.Store)
	planOutput, err := loadStackPlanOutput(prepared.Repo, stackID)
	if err != nil {
		return fmt.Errorf("stack drive: %w", err)
	}
	mode, _, _, _, err := parseStackPlanOutput(planOutput)
	if err != nil {
		return fmt.Errorf("stack drive: %w", err)
	}
	if mode == "single" || mode == "no_bug" {
		fmt.Fprintf(stdout, "stack %s: %s - nothing to stack\n", stackID, mode)
		return nil
	}
	// Reconstruct the full chunk list across every already-admitted decompose
	// wave (not just the plan run's own first wave): a prior process may have
	// already admitted continuation waves before this invocation. The drive
	// loader recovers a wedged wave instead of failing on it (see
	// loadAllStackChunksForDrive); loadAllStackChunks stays strict for the
	// reconcile sweep.
	chunks, hasMore, hasUnsettledWave, remainingScope, err := loadAllStackChunksForDrive(prepared, stackID, planOutput, planInputs, stdout, stderr)
	if err != nil {
		return fmt.Errorf("stack drive: %w", err)
	}
	if len(chunks) == 0 {
		return fmt.Errorf("stack drive: stack %s has a multi plan with no chunks", stackID)
	}
	if err := seedStackLedger(ledger, stackID, chunks); err != nil {
		return fmt.Errorf("stack drive: %w", err)
	}
	if err := driveStack(context.Background(), prepared, ledger, stackID, chunks, planInputs, false, hasMore, hasUnsettledWave, stdout, stderr); err != nil {
		if settled, settleErr := cliworkflow.SettleFailedStackPlanRunIfNeeded(context.Background(), prepared, stackID, err.Error()); settleErr != nil {
			return fmt.Errorf("stack drive: settle failed plan run: %w", settleErr)
		} else if settled {
			return errFailedStackPlanRun(stackID, err.Error())
		}
		return err
	}
	byID, err := stackTaskMap(ledger, stackID)
	if err != nil {
		return err
	}
	if err := admitPendingFollowUps(context.Background(), prepared, ledger, stackID, byID, stdout, stderr); err != nil {
		return fmt.Errorf("stack drive: %w", err)
	}
	if err := admitNextWaveIfReady(prepared, ledger, stackID, chunks, hasMore && !hasUnsettledWave, remainingScope, planInputs, stdout, stderr); err != nil {
		return err
	}
	if err := settleStackIntegrationRunIfReady(context.Background(), prepared, ledger, stackID, chunks, hasMore, hasUnsettledWave, stdout, stderr); err != nil {
		return err
	}
	return settleStackPlanRunIfComplete(context.Background(), prepared, stackID, stdout)
}

// settleStackPlanRunIfComplete resolves the plan run's own delivery_pending
// status once the operator-driven `mivia stack drive` loop above has run: a
// stack that just finished driving (or was already complete on entry) must
// not stay parked forever with no CLI command able to settle it (F11) -
// `workflow deliver`/`resume`/`cancel` all refuse a delivery_pending run
// pointing back at this command or at `workflow deliver`, so this command is
// the durable path that must close the loop. It reuses the same completion
// check and skip/deliver branches as the CLI deliver path
// (classifyStackPlanRunDelivery) and the session recovery sweep
// (cliworkflow.SkipParkedPlanRunPublication + cliworkflow.SettlePlanRunSkippedDelivery): a plan run
// with deliver_plan_run=false settles succeeded without publishing; one with
// deliver_plan_run=true is left for an explicit `mivia workflow deliver
// <runID> --allow-publish` (this command has no publish flag of its own,
// and stacking's whole point is that the chunk PRs already carry the
// reviewable work). An incomplete stack (still awaiting a publish grant, or
// mid decompose-continuation-wave) is left parked with no output - this is
// the routine, non-error `stack drive` outcome under merge_policy=approve.
func settleStackPlanRunIfComplete(ctx context.Context, prepared *cliworkflow.PreparedWorkflowRun, stackID string, stdout io.Writer) error {
	// Every gate value has a case: a terminally failed stack must report the
	// failure (and settle the plan run) instead of falling out of the switch
	// as if the drive ended cleanly, and an unknown value must not do so
	// either. Unlike the deliver path this branch publishes nothing, so the
	// fall-out was a silent-success bug, not a publication bug.
	switch gate := classifyStackPlanRunDeliveryFn(ctx, prepared.Root, prepared.Store, prepared.Repo, stackID, true); gate {
	case stackPlanRunNotApplicable, stackPlanRunIncomplete:
		// Routine `stack drive` outcome: nothing to settle, no output.
	case stackPlanRunFailed:
		return cliworkflow.RefuseFailedStackPlanRunDelivery(ctx, prepared.Root, prepared.Store, prepared.Repo, stackID)
	case stackPlanRunComplete:
		if cliworkflow.SkipParkedPlanRunPublication(ctx, prepared.Store, prepared.Repo, stackID) {
			if err := cliworkflow.SettlePlanRunSkippedDelivery(ctx, prepared.Repo, stackID); err != nil {
				return fmt.Errorf("stack drive: settle plan run: %w", err)
			}
			fmt.Fprintf(stdout, "stack %s: plan run settled (plan PR not created; delivery.deliver_plan_run=false)\n", stackID)
			return nil
		}
		fmt.Fprintf(stdout, "stack %s: plan run ready for delivery: mivia workflow deliver %s --allow-publish\n", stackID, stackID)
	default:
		return fmt.Errorf("stack drive: stack %s has an unknown plan run classification (%d)", stackID, int(gate))
	}
	return nil
}

// settleStackIntegrationRunIfReady is the operator-path completion hook for
// `mivia stack drive`: when every chunk and follow-up task is merged and no
// decompose continuation wave remains, it waits out the integration run's
// delivery and auto-merge under merge_policy=auto. This closes the gap where
// the operator path never called waitIntegrationRunSettled and left the final
// PR open even under auto-merge.
func settleStackIntegrationRunIfReady(ctx context.Context, prepared *cliworkflow.PreparedWorkflowRun, ledger *workflowledger.Store, stackID string, chunks []ChunkPlan, hasMore bool, hasUnsettledWave bool, stdout, stderr io.Writer) error {
	byID, err := stackTaskMap(ledger, stackID)
	if err != nil {
		return err
	}
	if hasMore || hasUnsettledWave || !allChunksMerged(chunks, stackMergedSet(byID)) || !allTasksMerged(byID) {
		return nil
	}
	checker := gitMergeChecker{
		git: cliworkflow.WorkflowDeliverGit,
		pr:  cliworkflow.WorkflowDeliverNewPR(),
		gc:  delivery.GitContext{Dir: prepared.Root, GitDir: filepath.Join(prepared.Root, ".git")},
	}
	policy := prepared.Compiled.Stacking.MergePolicy
	return waitIntegrationRunSettledFn(ctx, prepared, ledger, checker, stackID, policy, false, stdout, stderr)
}

// driveStackToCompletion drives the stack until every chunk is merged and the
// final integration run is published, waiting out publish grants (policy A)
// and merge-queue times instead of halting for a re-invocation. It is the
// in-command stacking engine: one `workflow run` invocation owns the whole
// stack (plan -> chunks -> per-chunk PRs -> integration) and only returns when
// the stack is complete, a chunk failed terminally, or the process is
// interrupted (the stack stays resumable from durable state). The ctx is the
// drive's stop signal: a cancelled/expired ctx (the session attempt bound)
// returns the cancellation error so the caller can release the run's
// execution flock instead of polling forever. CLI foreground paths pass
// context.Background() and stay unbounded by design.
func driveStackToCompletion(ctx context.Context, prepared *cliworkflow.PreparedWorkflowRun, ledger *workflowledger.Store, stackID string, chunks []ChunkPlan, hasMore bool, hasUnsettledWave bool, remainingScope string, planInputs map[string]string, allowPublish bool, stdout, stderr io.Writer) error {
	checker := gitMergeChecker{
		git: cliworkflow.WorkflowDeliverGit,
		pr:  cliworkflow.WorkflowDeliverNewPR(),
		gc:  delivery.GitContext{Dir: prepared.Root, GitDir: filepath.Join(prepared.Root, ".git")},
	}
	policy := prepared.Compiled.Stacking.MergePolicy
	wave, err := latestDecomposeContinueWave(prepared.Repo, stackID)
	if err != nil {
		return fmt.Errorf("stack drive: %w", err)
	}
	for {
		// The drive must stop when its context is done: a stuck merge-queue
		// poll would otherwise hold the plan run's execution flock forever.
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("stack drive: %w", err)
		}
		// One drive pass admits the next ready wave and applies the merge
		// policy; it halts when a chunk needs a grant or a merge to land.
		if err := driveStack(ctx, prepared, ledger, stackID, chunks, planInputs, allowPublish, hasMore, hasUnsettledWave, stdout, stderr); err != nil {
			return err
		}
		byID, err := stackTaskMap(ledger, stackID)
		if err != nil {
			return err
		}
		// A chunk that just delivered may have left a deferred commit
		// (§5.2-5.3): admit its follow-up PR before deciding whether the
		// stack is complete, so allTasksMerged below waits for it too.
		if err := admitPendingFollowUps(ctx, prepared, ledger, stackID, byID, stdout, stderr); err != nil {
			return fmt.Errorf("stack drive: %w", err)
		}
		byID, err = stackTaskMap(ledger, stackID)
		if err != nil {
			return err
		}
		if !allChunksMerged(chunks, stackMergedSet(byID)) || !allTasksMerged(byID) {
			// The pass halted before completion: wait for the outstanding
			// chunk's delivery + merge (or a terminal failure), then drive
			// again. With merge_policy=auto the wait also merges published
			// PRs itself.
			if err := waitForChunkMerges(ctx, prepared, ledger, checker, stackID, chunks, policy, stdout, stderr); err != nil {
				// errStackAwaitsGrant propagates as-is (a durable pause, not
				// a failure - the ledger keeps the stack resumable): the
				// caller (maybeDriveSettledStack) must see it distinctly
				// from genuine completion, or it settles the plan run
				// succeeded while the stack is still incomplete (an
				// adversarial audit found exactly this after the pause was
				// first added: driveStackToCompletion swallowed the pause
				// to nil here, so drove=true reached the plan-run-succeeded
				// branch with zero chunk PRs published).
				return err
			}
			continue
		}
		if !hasMore && !hasUnsettledWave {
			return waitIntegrationRunSettledFn(ctx, prepared, ledger, checker, stackID, policy, allowPublish, stdout, stderr)
		}
		// Every currently-known chunk merged, but an earlier decompose call
		// declared more scope than it planned (§12.1). Request the next wave
		// before considering the stack complete.
		wave++
		nextChunks, nextHasMore, nextRemaining, err := admitNextDecomposeWave(prepared, ledger, stackID, wave, chunks, remainingScope, planInputs, stdout, stderr)
		if err != nil {
			return err
		}
		chunks = append(chunks, nextChunks...)
		hasMore, remainingScope = nextHasMore, nextRemaining
	}
}

// resolveAndClaimStackDrive resolves the stack id and claims it for this
// drive invocation in one step, so a caller that fails either half never
// proceeds to drive an unclaimed or ambiguous stack. On success the caller
// must defer the returned release func immediately.
func resolveAndClaimStackDrive(ctx context.Context, prepared *cliworkflow.PreparedWorkflowRun, name, stackFlag string) (stackID string, release func(), err error) {
	stackID, err = ResolveStackIDFunc(prepared.Repo, name, stackFlag)
	if err != nil {
		return "", nil, err
	}
	storePath := ContextStorePath(prepared.Root, prepared.Res.Subagents)
	release, err = claimStackDrive(ctx, prepared.Repo, prepared.Root, storePath, stackID)
	if err != nil {
		return "", nil, fmt.Errorf("stack drive: %w", err)
	}
	return stackID, release, nil
}

// claimStackDrive acquires the plan run's execution flock and the stack's
// execution claim for this drive invocation, minting a fresh claim holder
// id. The flock comes first and matches every other CLI-operator run
// mutation (run/resume/deliver/cancel/delete/sweep, see cliworkflow.BeginWorkflowExecution
// in workflow_resume_lock.go): it is scoped to this process and held for the
// whole drive, so `workflow delete --force`/cancel/resume against the plan
// run block on it even if the claim heartbeat below has died from a
// transient store error and the DB claim has since gone stale (an
// adversarial audit found this drive was the one CLI-operator path with no
// flock on the plan run at all - "workflow delete --force" could take over
// and permanently strand the stack once the heartbeat lapsed). The claim
// reuses cliworkflow.ClaimWorkflowOperator's takeover-if-expired semantics: claim, and
// on ErrClaimHeld take over only if the existing claim's lease has expired,
// so a live foreign driver is refused with a clear "claimed by another
// executor" error and a stale one is recovered automatically. On success it
// starts a heartbeat that refreshes the claim for the duration of the drive:
// a single driveStack pass can admit and run several chunks sequentially,
// plausibly longer than workflowledger.DefaultClaimLease, so without a
// heartbeat a slow pass would let a second driver take over the still-live
// claim mid-drive through this same takeover-if-expired path - reopening the
// exact race this claim exists to close (mirrors
// claimWorkflowDeliveryRun/startWorkflowDeliveryClaimHeartbeat in
// workflow_deliver.go). On refusal the returned release is nil - the caller
// must not invoke it, and any partially-acquired flock is released before
// returning.
func claimStackDrive(ctx context.Context, repo workflowledger.Repository, workspaceRoot, storePath, stackID string) (release func(), err error) {
	releaseExecution, err := cliworkflow.BeginWorkflowExecutionBounded(ctx, workspaceRoot, storePath, stackID, cliworkflow.WorkflowResolutionLockWait)
	if err != nil {
		return nil, fmt.Errorf("stack drive: plan run %q: %w", stackID, err)
	}
	holder := newStackDriveHolder()
	if err := cliworkflow.ClaimWorkflowOperator(ctx, repo, stackID, holder); err != nil {
		releaseExecution()
		return nil, err
	}
	stopHeartbeat := startStackDriveClaimHeartbeat(ctx, repo, stackID, holder)
	return func() {
		stopHeartbeat()
		_ = repo.ReleaseRun(context.Background(), stackID, holder)
		releaseExecution()
	}, nil
}

// startStackDriveClaimHeartbeat refreshes the stack's claim with the same
// holder while the drive runs, so a pass that outlives the claim lease
// cannot be taken over mid-drive (DC-2). A failed refresh is terminal for the
// heartbeat: it stops instead of retry-spinning. The returned stop func
// closes the stop channel and waits for the goroutine to exit, so the caller
// releases the claim only after the last possible refresh has run (a tick
// landing after ReleaseRun would re-create the claim row and re-arm a dead
// lease for another lease window).
func startStackDriveClaimHeartbeat(ctx context.Context, repo workflowledger.Repository, stackID, holder string) (stop func()) {
	stopCh := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() { _ = recover() }()
		ticker := time.NewTicker(stackDriveClaimHeartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := repo.ClaimRun(ctx, stackID, holder); err != nil {
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

// newStackDriveHolder mints the run-claim holder for one `stack drive`
// invocation, matching the naming/construction pattern of this codebase's
// other CLI-operator claim holders (newWorkflowDeliveryHolder,
// newWorkflowCancelHolder, newWorkflowDeleteHolder).
func newStackDriveHolder() string {
	var value [10]byte
	_, _ = rand.Read(value[:])
	return "stackdrive-" + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(value[:])
}
