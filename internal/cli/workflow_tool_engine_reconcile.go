package cli

import (
	"context"
	"io"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// reconcileParkedDeliveries publishes delivery_pending runs left over from an
// earlier session (a restart or a crash). Delivery authorization comes from
// the workflow itself: a run settles at delivery_pending only when its
// workflow declares a [delivery] policy, and that policy is the publication
// grant - the harness must publish a delivery-defined workflow always, without
// flags or manual overrides, so a parked run is never stranded.
//
// deliverRunWithStore independently refuses a run whose workflow has no active
// delivery policy, and beginWorkflowExecution takes the workflow execution
// file lock per run, so a run being published by another live executor simply
// skips this sweep. Runs that route to a repair step (delivery.on_failure)
// settle back to running here; the next start/resume re-advances them, and a
// transient failure leaves the run delivery_pending for the next reconcile.
func (e *sessionWorkflowEngine) reconcileParkedDeliveries(ctx context.Context) {
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

	runs, err := repo.ListRuns(ctx, workflowledger.RunStatusDeliveryPending)
	if err != nil {
		return
	}
	storePath := contextStorePath(work.Abs, res.Subagents)
	for _, run := range runs {
		if ctx.Err() != nil {
			return
		}
		finish, err := beginWorkflowExecution(work.Abs, storePath, run.RunID)
		if err != nil {
			// Another executor holds this run; leave it parked.
			continue
		}
		// allowPublish=true is the harness's standing grant: the workflow's
		// delivery policy is the authorization, so no flag is consulted here.
		_ = deliverRunWithStore(ctx, work.Abs, res, store, repo, run.RunID, true, false, io.Discard, io.Discard)
		finish()
	}
}
