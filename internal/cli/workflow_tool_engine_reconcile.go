package cli

import (
	"context"
	"io"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
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
		return
	}
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
		policy := stackPlanMergePolicy(ctx, repo, runID)
		if !stackDriveCompleted(ctx, store, repo, runID, policy) {
			if !quiet {
				log.Printf("workflow: session recovery: plan run %s stack incomplete; leaving parked", runID)
			}
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

// stackDriveCompleted reports whether a stacking plan run's chunk stack
// actually drove to completion: the run ledger carries the succeeded decompose
// output the driver reads (loadStackPlanOutput), every chunk task in the task
// ledger is merged - the same durable state driveStackToCompletion checks
// before it settles (allChunksMerged(chunks, stackMergedSet(byID))) - AND the
// final integration run was admitted and settled. The integration run resolves
// via its stable admission key (stackRunRef on the integration chunk id;
// runID IS the stack id here, loadStackPlanOutput keys by it). The gate is
// deliberately STRICTER than waitIntegrationRunSettled, which reports the
// stack complete even for an unsettled integration run (its pending/running/
// waiting_approval fall-through): conservative by design, the sweep never
// settles a plan run over a stack the driver is still advancing. A
// delivery_pending integration run counts as settled ONLY under the grant
// merge policy - the driver's completion state there is "awaits the publish
// grant" (waitIntegrationRunSettled returns nil); under merge_policy=auto the
// driver still auto-merges the integration PR, so delivery_pending is NOT
// complete there and the plan run stays parked until the merge lands (policy
// resolves via stackPlanMergePolicy; "" behaves as the grant default). The
// integration dimension closes the crash-window hole where the bounded drive
// expired (or the process died) after every chunk merged but before the
// integration run was admitted or settled: without it the sweep would settle
// the plan run succeeded over a stack the driver itself refuses to call
// complete. Any resolution failure (missing run, corrupt output, unseeded
// plan) returns false, so a seeded-but-incomplete stack stays delivery_pending
// for the operator to finish with 'mivia stack drive'.
func stackDriveCompleted(ctx context.Context, store *storage.SQLite, repo workflowledger.Repository, runID, policy string) bool {
	raw, err := loadStackPlanOutput(repo, runID)
	if err != nil {
		return false
	}
	_, chunks, err := parseStackPlanOutput(raw)
	if err != nil {
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
	default:
		// Any terminal status: the integration run settled.
		return true
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
