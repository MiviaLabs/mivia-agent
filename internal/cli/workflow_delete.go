package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// executeWorkflowDelete removes a settled run's durable ledger record. It
// mirrors executeWorkflowCleanup's preamble (execution file lock, run lookup)
// and refuses active runs unless the operator passes force — the crash
// recovery override, exactly as resume --force and deliver --force are. force
// unlocks only the STATUS gate (pending/running/waiting_approval become
// deletable so a run stranded by a dead executor can be purged); it never
// clears a live claim: claimWorkflowOperator still refuses a fresh claim held
// by a live executor and takes over only an expired lease. A delivery_pending
// run may be mid-publish under a live claim, so deletion claims the run and
// never blind-clears a held claim.
func executeWorkflowDelete(runID, root, configPath string, force bool, stdout, stderr io.Writer) error {
	releaseExecution, repo, _, closeFn, err := openWorkflowResolutionContextBounded(root, configPath, runID, workflowResolutionLockWait)
	if err != nil {
		return err
	}
	defer closeFn()
	defer releaseExecution()
	ctx := context.Background()
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		if errors.Is(err, workflowledger.ErrNotFound) {
			return fmt.Errorf("workflow run %q not found", runID)
		}
		return err
	}
	if !workflowledger.IsDeletableRunStatus(run.Status) && !force {
		return fmt.Errorf("workflow run %q is %q; cancel it before delete, or pass --force only after the prior executor stopped (a live claim is still refused)", runID, run.Status)
	}
	holder := newWorkflowDeleteHolder()
	if err := claimWorkflowOperator(ctx, repo, runID, holder); err != nil {
		return fmt.Errorf("workflow run %q is claimed by another executor; delete refused", runID)
	}
	ctx = workflowledger.ContextWithClaimHolder(ctx, holder)
	if err := repo.DeleteRun(ctx, runID); err != nil {
		fmt.Fprintf(stderr, "workflow delete failed: %v\n", err)
		return err
	}
	fmt.Fprintf(stdout, "run_id=%s status=%s deleted=true\n", runID, run.Status)
	return nil
}
