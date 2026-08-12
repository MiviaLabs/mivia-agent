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
// and refuses active runs. A delivery_pending run may be mid-publish under a
// live claim, so deletion claims the run (taking over only an expired lease)
// and never blind-clears a held claim.
func executeWorkflowDelete(runID, root, configPath string, stdout, stderr io.Writer) error {
	releaseExecution, repo, _, closeFn, err := openWorkflowResolutionContext(root, configPath, runID)
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
	if !workflowledger.IsDeletableRunStatus(run.Status) {
		return fmt.Errorf("workflow run %q is %q; cancel it before delete", runID, run.Status)
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
