package cli

import (
	"context"
	"errors"
	"log"
	"time"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// sessionSettleTimeout bounds the settle write. The controller has already
// stopped, so this must not hold the session open.
const sessionSettleTimeout = 5 * time.Second

// settleSessionRunFailure records why a session-driven run stopped, and gives
// it a terminal status when nothing else will.
//
// The controller returns its cause from Run, but the session engine read that
// cause only to decide whether to deliver, then dropped it. The run row stayed
// `running` with no explanation anywhere: it looked alive and was not. The
// local engine already answers this, in settleRunFailure, and the two engines
// simply disagreed.
//
// This is that same answer, with the same carve-outs:
//   - A cancelled run is left alone. Cancel settles the run itself, and a
//     failed status written here would race it and win.
//   - A run another holder owns is left alone. That holder is the live
//     executor.
//   - A run that already reached a terminal status is left alone.
//
// A storage fault that stops the controller therefore settles as failed rather
// than stranding. The work is not lost: every completed step stays durable in
// the ledger, and the operator can see the cause instead of guessing why a
// `running` run stopped moving.
func settleSessionRunFailure(repo workflowledger.Repository, runID string, runErr error) {
	if runErr == nil || errors.Is(runErr, context.Canceled) {
		return
	}
	log.Printf("workflow: run %s stopped with error: %v", runID, runErr)
	ctx, cancel := context.WithTimeout(context.Background(), sessionSettleTimeout)
	defer cancel()
	holder := "wfsettle-" + newCLIWorkflowRunID()
	if err := repo.ClaimRun(ctx, runID, holder); err != nil {
		return // another holder owns the run and will settle or continue it
	}
	defer func() { _ = repo.ReleaseRun(context.Background(), runID, holder) }()
	fresh, err := repo.GetRun(ctx, runID)
	if err != nil || workflowledger.IsTerminalRunStatus(fresh.Status) {
		return
	}
	_ = repo.CompareAndSetRunStatus(ctx, runID, fresh.Version, workflowledger.RunStatusFailed, nil)
}
