package cli

import (
	"context"
	"io"
	"log"
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// reconcileParkedRuns recovers every run an earlier session left unfinished -
// the restart/crash cases the session engine never re-enters on its own:
//
//   - delivery_pending runs are published (reconcileParkedDelivery).
//     Delivery authorization comes from the workflow itself: a run settles at
//     delivery_pending only when its workflow declares a [delivery] policy,
//     and that policy is the publication grant - the harness must publish a
//     delivery-defined workflow always, without flags or manual overrides, so
//     a parked run is never stranded.
//
//   - pending, running, and waiting_approval runs are resumed
//     (reconcileParkedResume) through the same durable preflight as the
//     resume tool (prepareResume -> launchResume). The claim handoff makes
//     this safe: an unclaimed run (graceful shutdown released it) is claimed
//     fresh, a run whose claim is older than the lease (a crashed executor
//     stopped heartbeating) is taken over, and a run with a fresh claim (a
//     live session is executing it) is refused - the sweep skips it and a
//     later session start retries. Runs the sweep cannot resume (invalid
//     snapshot, config drift, live claim) stay as they are; the operator can
//     still resume them explicitly.
//
// deliverRunWithStore independently refuses a run whose workflow has no
// active delivery policy, and beginWorkflowExecution takes the workflow
// execution file lock per run, so a run being published by another live
// executor simply skips this sweep. The resume path takes the same lock.
func (e *sessionWorkflowEngine) reconcileParkedRuns(ctx context.Context) {
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
				e.reconcileParkedDelivery(ctx, work.Abs, res, store, repo, storePath, run.RunID)
			case workflowledger.RunStatusPending, workflowledger.RunStatusRunning, workflowledger.RunStatusWaitingApproval:
				e.reconcileParkedResume(ctx, run.RunID)
			}
		}(run)
	}
	wg.Wait()
}

// reconcileParkedDelivery publishes one delivery_pending run. allowPublish
// is always true: the workflow's delivery policy is the authorization, so no
// flag is consulted. A delivery failure with delivery.on_failure routes the
// run back to running at the repair step (ReopenForRepair); the sweep
// re-enters it through the resume path immediately so the bounded in-session
// repair loop re-advances and re-delivers NOW instead of stranding the run
// until the next session start. A transient delivery failure leaves the run
// delivery_pending for the next reconcile. launchResume (from the repair
// re-entry) and publishDeliveredRunFinished (for direct deliveries) publish
// the terminal run_finished event like the in-session loop.
func (e *sessionWorkflowEngine) reconcileParkedDelivery(ctx context.Context, root string, res *config.Resolved, store *storage.SQLite, repo workflowledger.Repository, storePath, runID string) {
	finish, err := beginWorkflowExecution(root, storePath, runID)
	if err != nil {
		// Another executor holds this run; leave it parked.
		return
	}
	deliverErr := deliverRunWithStore(ctx, root, res, store, repo, runID, true, false, io.Discard, io.Discard)
	finish()
	if deliverErr != nil {
		log.Printf("workflow: session recovery: deliver %s failed: %v", runID, deliverErr)
		return
	}
	fresh, getErr := repo.GetRun(ctx, runID)
	if getErr == nil && fresh.Status == workflowledger.RunStatusRunning {
		if _, err := e.resumeCLI(ctx, agenttools.StartRequest{Resume: true, RunID: runID, Force: false}); err != nil {
			log.Printf("workflow: session recovery: re-advance repair route for %s failed: %v", runID, err)
		}
		return
	}
	e.publishDeliveredRunFinished(ctx, repo, runID)
}

// reconcileParkedResume resumes one interrupted (pending/running/
// waiting_approval) run asynchronously through the same path as the resume
// tool; launchResume registers the run in the engine's active set. A fresh
// claim (live session) or an unresumable snapshot makes prepareResume fail
// synchronously and the sweep moves on. waiting_approval runs re-attach at
// the human gate and re-park (pauseHumanGate is idempotent): the resume
// never re-executes agents or auto-approves.
func (e *sessionWorkflowEngine) reconcileParkedResume(ctx context.Context, runID string) {
	if _, err := e.resumeCLI(ctx, agenttools.StartRequest{Resume: true, RunID: runID, Force: false}); err != nil {
		log.Printf("workflow: session recovery: resume %s skipped: %v", runID, err)
	}
}
