package cli

import (
	"context"
	"errors"
	"io"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/tasks"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// workflowReconcileInterval is how often the session engine re-scans the
// ledger for runs to recover while the session is up, so a run whose claim
// expires mid-session (a hard kill, a transient failure) resumes on its own
// without a restart. A package var so tests can shorten it. Atomic because
// the periodic re-scan goroutine (started by workflowToolServiceWithBus on a
// background context) can outlive the test that armed the sweep, so a later
// test shortening the interval must not race the goroutine's ticker creation.
var workflowReconcileInterval atomic.Int64

func init() {
	workflowReconcileInterval.Store(int64(30 * time.Second))
}

// reconcileParkedRuns recovers every run an earlier session left unfinished -
// the restart/crash cases the session engine never re-enters on its own.
// quiet suppresses the expected per-run skip/failure logs (a fresh claim
// belongs to a live holder, or a snapshot is unresumable): the periodic
// re-scan passes true so active runs do not log every interval; the
// session-start sweep passes the session's quiet flag, so --quiet also
// silences the one-shot recovery notices.
//
// See reconcileParkedDelivery and reconcileParkedResume for the per-status
// behavior.
func (e *sessionWorkflowEngine) reconcileParkedRuns(ctx context.Context, quiet bool) {
	if e == nil || e.root == "" {
		log.Printf("workflow: session recovery: no root")
		return
	}
	work, err := workspace.Open(e.root)
	if err != nil {
		return
	}
	res, err := config.Load(config.LoadOptions{
		ConfigPath:         workflowConfigPath(work.Abs, e.configPath),
		WorkspaceRoot:      work.Abs,
		AllowMissingConfig: true,
	})
	if err != nil {
		return
	}
	applyPrivacyPolicy(res)
	applyWorkflowStoreRoot(res, work.Abs)
	store, repo, closeFn, err := openWorkflowStore(work.Abs, res.Subagents)
	if err != nil {
		return
	}
	defer closeFn()

	runs, err := repo.ListRuns(ctx,
		workflowledger.RunStatusPending,
		workflowledger.RunStatusRunning,
		workflowledger.RunStatusWaitingApproval,
		workflowledger.RunStatusDeliveryPending,
	)
	if err != nil {
		log.Printf("workflow: session recovery: list parked runs: %v", err)
		return
	}
	log.Printf("workflow: session recovery: %d parked run(s)", len(runs))
	storePath := contextStorePath(work.Abs, res.Subagents)
	// Fan out every parked run in parallel. Each run owns its execution file
	// lock and run claim, so concurrent delivery/resume of different runs
	// cannot conflict: those per-run fences are the only serialization points
	// and the storage layer serializes writes per run. A sequential sweep
	// would let one slow delivery (the push runs the repo's pre-push hook,
	// often the full test suite) delay every other parked run for minutes;
	// parallel dispatch recovers them all at once. Resumed runs continue in
	// the engine's active set after the sweep returns.
	var wg sync.WaitGroup
	for _, run := range runs {
		if ctx.Err() != nil {
			return
		}
		wg.Add(1)
		go func(run workflowledger.RunSnapshot) {
			defer wg.Done()
			// Orphan follow-up run rows (wfr-followup-* prefix, empty
			// snapshot) are left by reserveFollowUpRun when the
			// subsequent EnsureFollowUpPublished fails. The next drive
			// pass heals them; the sweep must not try to resume them
			// (the empty snapshot fails digest validation, producing
			// "snapshot digest does not match" noise every tick).
			if run.SnapshotDigest == "" && strings.HasPrefix(run.RunID, "wfr-followup-") {
				return
			}
			switch run.Status {
			case workflowledger.RunStatusDeliveryPending:
				e.reconcileParkedDelivery(ctx, work.Abs, res, store, repo, storePath, run.RunID, quiet)
			case workflowledger.RunStatusPending, workflowledger.RunStatusRunning, workflowledger.RunStatusWaitingApproval:
				e.reconcileParkedResume(ctx, run.RunID, quiet)
			}
		}(run)
	}
	wg.Wait()
}

// reconcileParkedDelivery publishes one delivery_pending run. allowPublish
// is always true: the workflow's delivery policy is the authorization, so no
// flag is consulted. A stacking plan run whose multi-chunk stack DROVE TO
// COMPLETION - every chunk task in its task ledger is merged AND the final
// integration run was admitted and settled, the same durable state the driver
// checks - and whose workflow disables publishing the plan run itself
// (delivery.deliver_plan_run=false) is settled succeeded WITHOUT publication
// instead: the chunk PRs carry the work, and a sweep publish after the crash
// window between the drive and the settle's CAS would falsely publish the
// plan PR (mirrors the in-session skip branch). A seeded-but-
// incomplete stack (the drive aborted after seeding the ledger but before any
// chunk merged) stays delivery_pending for the operator to finish with 'mivia
// stack drive': settling it succeeded would report a plan run succeeded over
// an INCOMPLETE stack, and any non-skipped fall-through would publish the plan
// PR over it (the exact deliver-before-drive bug the skip path exists to
// prevent). A delivery failure with delivery.on_failure routes the run back to
// running at the repair step (ReopenForRepair); the sweep re-enters it
// through the resume path immediately so the bounded in-session repair loop
// re-advances and re-delivers NOW instead of stranding the run until the
// next session start. A transient delivery failure leaves the run
// delivery_pending for the next reconcile. launchResume (from the repair
// re-entry) and publishDeliveredRunFinished (for direct deliveries) publish
// the terminal run_finished event like the in-session loop. quiet (--quiet)
// suppresses the expected per-run failure/skip logs: the session-start sweep
// honors the flag the same way the resume path does.
func (e *sessionWorkflowEngine) reconcileParkedDelivery(ctx context.Context, root string, res *config.Resolved, store *storage.SQLite, repo workflowledger.Repository, storePath, runID string, quiet bool) {
	finish, err := beginWorkflowExecution(root, storePath, runID)
	if err != nil {
		// Another executor holds this run; leave it parked.
		log.Printf("workflow: session recovery: %s skip: execution flock held: %v", runID, err)
		return
	}
	if skipParkedPlanRunPublication(ctx, store, repo, runID) {
		// The plan run's own publication is disabled and the task ledger
		// carries the seeded stack plan. Only a stack that actually drove to
		// COMPLETION (every chunk task merged AND the final integration run
		// admitted and settled - the same durable state the driver checks) is
		// settled succeeded WITHOUT publishing: the chunk PRs carry the work,
		// and the crash window between the drive and the settle's CAS must
		// never publish the plan PR. A seeded-but-incomplete stack (the drive
		// aborted after seeding the ledger) stays delivery_pending for the
		// operator to finish with 'mivia stack drive'; settling or delivering
		// it now would report the plan run succeeded over an incomplete
		// stack, or publish the plan PR over it (deliver-before-drive).
		if abort := e.driveParkedStackIfNeeded(ctx, root, res, store, repo, runID, quiet); abort {
			finish()
			return
		}
		if settleErr := settlePlanRunSkippedDelivery(context.Background(), repo, runID); settleErr != nil {
			if !quiet {
				log.Printf("workflow: session recovery: settle skipped plan run %s failed: %v", runID, settleErr)
			}
		}
		finish()
		return
	}
	// Drive-before-delivery guard (F10): when DeliverPlanRun is true,
	// skipParkedPlanRunPublication returns false and control reaches here.
	// If the run is a multi-chunk stacking plan run whose stack has not driven
	// to completion, deliverRunWithStore would settle the plan run (no_diff →
	// succeeded) over an undriven stack — the deliver-before-drive bug.
	// classifyStackPlanRunDelivery covers the CLI path only
	// (workflow_deliver.go), not the engine sweep path. Drive the stack
	// first; then fall through to deliverRunWithStore, which publishes the
	// plan run PR as DeliverPlanRun=true requests.
	if _, ok := stackDecomposedChunks(ctx, repo, runID); ok {
		if abort := e.driveParkedStackIfNeeded(ctx, root, res, store, repo, runID, quiet); abort {
			finish()
			return
		}
	}
	// Publish authority for a stack chunk/integration run derives from the
	// stack's merge policy: the allowPublish=true just below authorizes a
	// non-stacking run (or a deliver_plan_run=true plan run), never a
	// blanket grant (reachable-bug audit finding 1; see
	// stackRunPublishWithheld's doc comment).
	if stackRunPublishWithheld(ctx, repo, runID, quiet) {
		finish()
		return
	}
	deliverErr := deliverRunWithStore(ctx, root, res, store, repo, runID, true, false, io.Discard, io.Discard)
	finish()
	if deliverErr != nil {
		if !quiet {
			log.Printf("workflow: session recovery: deliver %s failed: %v", runID, deliverErr)
		}
		return
	}
	fresh, getErr := repo.GetRun(ctx, runID)
	if getErr == nil && fresh.Status == workflowledger.RunStatusRunning {
		if _, err := e.resumeCLI(ctx, agenttools.StartRequest{Resume: true, RunID: runID, Force: false}); err != nil {
			if !quiet {
				log.Printf("workflow: session recovery: re-advance repair route for %s failed: %v", runID, err)
			}
		}
		return
	}
	e.publishDeliveredRunFinished(ctx, repo, runID)
}

// driveParkedStackImpl is the production implementation behind
// driveParkedStackIfNeeded. It is a package var so tests can stub or count
// calls to driveParkedStack without changing the public surface.
var driveParkedStackImpl = (*sessionWorkflowEngine).driveParkedStack

// driveParkedStackIfNeeded drives a parked multi-chunk plan run's incomplete
// stack from the recovery sweep. Returns true (abort) when the stack is
// still incomplete after the drive attempt and the caller must leave the run
// parked; returns false when the stack is complete (or not a stacking run)
// and the caller may proceed with delivery or settlement. Used by both the
// skip-publication path and the deliver-before-delivery guard (F10).
func (e *sessionWorkflowEngine) driveParkedStackIfNeeded(ctx context.Context, root string, res *config.Resolved, store *storage.SQLite, repo workflowledger.Repository, runID string, quiet bool) bool {
	policy := stackPlanMergePolicy(ctx, repo, runID)
	if stackDriveCompleted(ctx, root, store, repo, runID, policy, true) {
		return false
	}
	// A terminally failed stack cannot complete, so fail-settle the plan
	// run once instead of re-driving it forever on every sweep tick.
	if gate := classifyStackPlanRunDelivery(ctx, root, store, repo, runID, true); gate == stackPlanRunFailed {
		_, reason := stackPlanRunFailureReasonFn(ctx, root, store, repo, runID)
		if reason == "" {
			reason = "stack terminally failed"
		}
		if settleErr := settleStackPlanRunFailed(context.Background(), repo, runID, reason); settleErr != nil {
			if !quiet {
				log.Printf("workflow: session recovery: settle failed stack plan run %s failed: %v", runID, settleErr)
			}
		}
		return true
	}
	// The stack is seeded but incomplete: DRIVE it from the recovery
	// sweep (the durable backstop). The in-session drive can abort for
	// many reasons (its 30-minute attempt bound, a transient admission
	// or delivery fault, the session ending), and before this fix
	// NOTHING else ever advanced a seeded-but-incomplete stack: the
	// plan run sat parked at delivery_pending forever with zero chunk
	// runs and zero PRs (the parked-stack wedge). The drive is
	// idempotent (stable admission keys + task CAS + durable
	// reconcile), so re-driving from the sweep on every tick is safe;
	// the per-run execution flock serializes it against the in-session
	// drive. A drive that outlives its bound returns and the next tick
	// resumes it.
	driveCtx, cancelDrive := context.WithTimeout(ctx, workflowAutoDeliveryAttemptTimeout)
	drove, driveErr := driveParkedStackImpl(e, driveCtx, root, res, store, repo, runID)
	cancelDrive()
	if driveErr != nil && !errors.Is(driveErr, errStackAwaitsGrant) {
		// A drive error may have left the stack terminally failed (for
		// example, a chunk task reached stackStatusFailed). Fail-settle
		// once instead of leaving the run delivery_pending so the next
		// sweep tick re-drives the dead stack.
		if gate := classifyStackPlanRunDelivery(ctx, root, store, repo, runID, true); gate == stackPlanRunFailed {
			reason := driveErr.Error()
			if _, r := stackPlanRunFailureReason(ctx, root, store, repo, runID); r != "" {
				reason = r
			}
			if settleErr := settleStackPlanRunFailed(context.Background(), repo, runID, reason); settleErr != nil {
				if !quiet {
					log.Printf("workflow: session recovery: settle failed stack plan run %s failed: %v", runID, settleErr)
				}
			}
			return true
		}
		if !quiet {
			log.Printf("workflow: session recovery: drive parked stack %s failed: %v", runID, driveErr)
		}
		return true
	}
	if !drove || !stackDriveCompleted(ctx, root, store, repo, runID, policy, true) {
		if !quiet {
			log.Printf("workflow: session recovery: plan run %s stack incomplete; leaving parked", runID)
		}
		return true
	}
	return false
}

// skipParkedPlanRunPublication reports whether a delivery_pending run has the
// SKIP SHAPE the sweep may settle WITHOUT publishing: the compiled workflow
// disables the plan run's own publication (delivery.deliver_plan_run false -
// the default) and the run's task ledger carries the seeded stack plan the
// chunk drive wrote. The shape predicate alone does NOT authorize the settle:
// reconcileParkedDelivery settles succeeded only when the stack actually drove
// to completion (stackDriveCompleted); a seeded-but-incomplete stack stays
// delivery_pending for the operator to finish with 'mivia stack drive'. Any
// resolution failure (missing run, corrupt snapshot) returns false, so the run
// falls through to deliverRunWithStore, which reports the error exactly as
// before.
func skipParkedPlanRunPublication(ctx context.Context, store *storage.SQLite, repo workflowledger.Repository, runID string) bool {
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		return false
	}
	raw, err := repo.GetRunSnapshot(ctx, runID)
	if err != nil {
		return false
	}
	_, compiled, _, err := validateWorkflowResumeSnapshot(run, raw)
	if err != nil {
		return false
	}
	if compiled == nil || compiled.Delivery == nil || compiled.Delivery.DeliverPlanRun {
		return false
	}
	_, err = tasks.NewStore(store).ReadBackPlan(runID)
	return err == nil
}

// stackPlanMergePolicy resolves the stacking merge_policy of a plan run from
// its admitted snapshot (the same snapshot validateWorkflowResumeSnapshot
// validates on the resume path), so stackDriveCompleted can apply the
// policy-aware delivery_pending rule. Any resolution failure (missing run,
// corrupt snapshot) returns "" - the grant default: a delivery_pending
// integration run then counts as complete (admitted, awaiting the publish
// grant).
func stackPlanMergePolicy(ctx context.Context, repo workflowledger.Repository, runID string) string {
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		return ""
	}
	raw, err := repo.GetRunSnapshot(ctx, runID)
	if err != nil {
		return ""
	}
	_, compiled, _, err := validateWorkflowResumeSnapshot(run, raw)
	if err != nil {
		return ""
	}
	if compiled == nil || compiled.Stacking == nil {
		return ""
	}
	return compiled.Stacking.MergePolicy
}

// driveParkedStack continues a parked multi-chunk plan run's stack drive from
// the recovery sweep (reconcileParkedDelivery). The in-session drive can abort
// on its attempt bound or any fault; this is the durable re-entry point that
// keeps the stack advancing until every chunk and the integration run settle,
// after which the sweep settles the plan run itself. It reconstructs the
// prepared run (the same prepareWorkflowRun path the CLI uses) from the plan
// run's durable snapshot, so the drive replays the plan run's declared inputs
// into every chunk run (D3). Returns whether it drove a stack; the caller
// re-checks stackDriveCompleted to decide the settle. errStackAwaitsGrant
// propagates as-is (a durable pause, not a failure).
func (e *sessionWorkflowEngine) driveParkedStack(ctx context.Context, root string, res *config.Resolved, store *storage.SQLite, repo workflowledger.Repository, runID string) (bool, error) {
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		return false, err
	}
	raw, err := repo.GetRunSnapshot(ctx, runID)
	if err != nil {
		return false, err
	}
	_, compiled, _, err := validateWorkflowResumeSnapshot(run, raw)
	if err != nil {
		return false, err
	}
	if compiled == nil || compiled.Stacking == nil || !compiled.Stacking.Enabled || !compiled.DeliveryActive() {
		return false, nil // nothing to drive (not a stacking delivery plan run)
	}
	snap, err := workflowledger.UnmarshalSnapshot(raw)
	if err != nil {
		return false, err
	}
	rawInputs := make([]string, 0, len(snap.Inputs))
	for k, v := range snap.Inputs {
		rawInputs = append(rawInputs, k+"="+v)
	}
	prepared, err := prepareWorkflowRun(run.WorkflowName, root, workflowConfigPath(root, e.configPath), rawInputs)
	if err != nil {
		return false, err
	}
	defer prepared.closeFn()
	// Replay the plan run's inputs: the controller's PrepareWorkflowRun keeps
	// the inputSnapshot from the plan run's OWN admission (it compiled from
	// the same raw inputs), so chunk runs replay the plan's declared inputs
	// exactly as the in-session drive would (D3). Publish authority derives
	// from the merge policy like the session hook: an approve stack (the
	// default) must pause for the per-chunk deliver grant, never auto-publish
	// (live finding: the sweep hardcoded true and silently merged approve
	// stacks, skipping the human checkpoint).
	return maybeDriveSettledStack(ctx, prepared, runID, stackingDriveAllowPublish(prepared.compiled), io.Discard, io.Discard)
}

// stackDriveCompleted reports whether a stacking plan run's chunk stack
// actually drove to completion: the run ledger carries the succeeded decompose
// output the driver reads (loadStackPlanOutput), every chunk task is merged
// (allChunksMerged over stackTaskMap), and the final integration run - keyed
// by its stable admission key; runID IS the stack id - was admitted and
// settled. The gate is deliberately STRICTER than waitIntegrationRunSettled
// (which reports complete for an unsettled integration run), so the sweep
// never settles a plan run over a stack the driver is still advancing. A
// delivery_pending integration run counts as settled ONLY under the grant
// merge policy ("awaits the publish grant"); under merge_policy=auto the
// driver still auto-merges the integration PR, so delivery_pending is NOT
// complete there. Any resolution failure (missing run, corrupt output,
// unseeded plan) returns false: a seeded-but-incomplete stack stays
// delivery_pending for 'mivia stack drive' to finish.
//
// remoteMergeOracle: settle paths (deliver, stack drive, the sweep) MUST
// pass true - for a succeeded, pushed integration run under auto policy,
// the oracle (git merge-base, then gh IsMerged if inconclusive) must
// confirm the PR actually merged before the plan run may settle.
// Read-only display surfaces (workflow status's undriven notice, the
// run_finished event publisher) pass false: the durable pushed evidence
// settles the DISPLAY verdict, and a read-only surface runs no probes.
func stackDriveCompleted(ctx context.Context, root string, store *storage.SQLite, repo workflowledger.Repository, runID, policy string, remoteMergeOracle bool) bool {
	// loadAllStackChunks (not the plan run's own output alone) so a pending
	// decompose-continuation wave (§12.1: hasMore=true with no continuation
	// admitted yet) is never mistaken for a complete stack just because every
	// CURRENTLY KNOWN chunk merged.
	chunks, hasMore, _, err := loadAllStackChunks(repo, runID)
	if err != nil {
		return false
	}
	if hasMore {
		return false
	}
	byID, err := stackTaskMap(tasks.NewStore(store), runID)
	if err != nil {
		return false
	}
	if !allChunksMerged(chunks, stackMergedSet(byID)) {
		return false
	}
	// The final full-suite integration run must have been admitted and
	// settled - a STRICTER gate than waitIntegrationRunSettled, which
	// reports the stack complete even for an unsettled integration run
	// (conservative by design). runID IS the stack id here, so the
	// integration run resolves by its stable admission key
	// <stack-id>:integration.
	intRun, found, err := stackRunRef(repo, runID, stackIntegrationChunkID)
	if err != nil || !found {
		return false
	}
	switch intRun.Status {
	case workflowledger.RunStatusPending, workflowledger.RunStatusRunning, workflowledger.RunStatusWaitingApproval:
		return false
	case workflowledger.RunStatusDeliveryPending:
		// delivery_pending is complete only under the grant policy: the
		// driver reports the stack complete there awaiting the publish
		// grant, while under merge_policy=auto it still auto-merges the
		// integration PR (waitIntegrationRunSettled) - settling now would
		// break that contract (a later drive skips autoMergeOne once the
		// integration run is terminal).
		return policy != "auto"
	case workflowledger.RunStatusSucceeded:
		// Under merge_policy=auto with durable pushed evidence, the
		// integration PR must actually be merged before the plan run may
		// settle. Otherwise a crash between deliverRunWithStore and
		// waitForIntegrationMerge leaves the final PR open while the
		// sweep reports the stack complete.
		if policy != "auto" || !stackRunPushed(repo, intRun) {
			return true
		}
		if !remoteMergeOracle {
			// Display-only caller: the durable pushed evidence settles the
			// verdict, and a read-only surface must not run network probes.
			return true
		}
		slug, _ := delivery.ParseOwnerRepo(intRun.RemoteURL)
		checker := gitMergeChecker{
			git: workflowDeliverGit,
			pr:  workflowDeliverNewPR(),
			gc:  delivery.GitContext{Dir: root, GitDir: filepath.Join(root, ".git")},
		}
		merged, err := checker.Merged(ctx, stackHeadBranch(intRun), intRun.BaseRef, stackRunHeadCommit(repo, intRun), slug, true)
		if err != nil {
			return false
		}
		return merged
	case workflowledger.RunStatusFailed, workflowledger.RunStatusCanceled,
		workflowledger.RunStatusTimedOut, workflowledger.RunStatusDeliveryFailed:
		// Terminal failure: the integration run settled but the stack did NOT
		// complete. A failed stack must not make the plan run look complete.
		return false
	default:
		// Unknown status: fail closed. Only the explicit statuses above are
		// recognized as stack-complete.
		return false
	}
}

// reconcileParkedResume resumes one interrupted (pending/running/
// waiting_approval) run asynchronously through the same path as the resume
// tool; launchResume registers the run in the engine's active set. A fresh
// claim (live session) or an unresumable snapshot makes prepareResume fail
// synchronously and the sweep moves on. waiting_approval runs re-attach at
// the human gate and re-park (pauseHumanGate is idempotent): the resume
// never re-executes agents or auto-approves.
func (e *sessionWorkflowEngine) reconcileParkedResume(ctx context.Context, runID string, quiet bool) {
	if _, err := e.resumeCLI(ctx, agenttools.StartRequest{Resume: true, RunID: runID, Force: false}); err != nil {
		if !quiet {
			log.Printf("workflow: session recovery: resume %s skipped: %v", runID, err)
		}
	}
}

// reconcileParkedRunsPeriodic re-runs the recovery scan while the session is
// up, so a run whose claim expires mid-session (a hard kill, a transient
// failure) is picked up on its own at the first scan after expiry - no
// restart and no manual resume. The pass is quiet: expected skips (runs this
// or another live session is executing) must not log every interval. The
// per-run execution lock and claim fence make repeated scans idempotent and
// safe against concurrent executors.
func (e *sessionWorkflowEngine) reconcileParkedRunsPeriodic(ctx context.Context) {
	ticker := time.NewTicker(time.Duration(workflowReconcileInterval.Load()))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.reconcileParkedRuns(ctx, true)
		}
	}
}
