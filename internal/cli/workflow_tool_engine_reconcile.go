package cli

import (
	"context"
	"io"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// reconcileParkedRuns recovers every run an earlier session left unfinished -
// the restart/crash cases the session engine never re-enters on its own:
//
//   - delivery_pending runs are published. Delivery authorization comes from
//     the workflow itself: a run settles at delivery_pending only when its
//     workflow declares a [delivery] policy, and that policy is the
//     publication grant - the harness must publish a delivery-defined
//     workflow always, without flags or manual overrides, so a parked run is
//     never stranded.
//
//   - pending, running, and waiting_approval runs are resumed through the same
//     durable preflight as the resume tool (prepareResume -> launchResume).
//     The claim handoff makes this safe: an unclaimed run (graceful shutdown
//     released it) is claimed fresh, a run whose claim is older than the lease
//     (a crashed executor stopped heartbeating) is taken over, and a run with
//     a fresh claim (a live session is executing it) is refused - the sweep
//     skips it and a later session start retries. Runs the sweep cannot
//     resume (invalid snapshot, config drift, live claim) stay as they are;
//     the operator can still resume them explicitly.
//
// deliverRunWithStore independently refuses a run whose workflow has no
// active delivery policy, and beginWorkflowExecution takes the workflow
// execution file lock per run, so a run being published by another live
// executor simply skips this sweep. The resume path takes the same lock.
// Runs that route to a repair step (delivery.on_failure) settle back to
// running here; the next start/resume re-advances them, and a transient
// failure leaves the run delivery_pending for the next reconcile.
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
	for _, run := range runs {
		if ctx.Err() != nil {
			return
		}
		switch run.Status {
		case workflowledger.RunStatusDeliveryPending:
			finish, err := beginWorkflowExecution(work.Abs, storePath, run.RunID)
			if err != nil {
				// Another executor holds this run; leave it parked.
				continue
			}
			// allowPublish=true is the harness's standing grant: the
			// workflow's delivery policy is the authorization, so no flag is
			// consulted here.
			_ = deliverRunWithStore(ctx, work.Abs, res, store, repo, run.RunID, true, false, io.Discard, io.Discard)
			finish()
		case workflowledger.RunStatusPending, workflowledger.RunStatusRunning, workflowledger.RunStatusWaitingApproval:
			// Resume asynchronously through the same path as the resume tool;
			// launchResume registers the run in the engine's active set. A
			// fresh claim (live session) or an unresumable snapshot makes
			// prepareResume fail synchronously and the sweep moves on.
			if _, err := e.resumeCLI(ctx, agenttools.StartRequest{Resume: true, RunID: run.RunID, Force: false}); err != nil {
				continue
			}
		}
	}
}
