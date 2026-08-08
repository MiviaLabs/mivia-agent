package cli

import (
	"context"
	"errors"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// A controller error must reach the run row. The session engine read the
// controller's cause only to decide whether to deliver, then dropped it: the
// run stayed `running` with no explanation anywhere, so it looked alive and
// was not. The local engine already settled such a run; the two engines
// disagreed.
func TestSessionRunFailureSettlesTheRun(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	run := workflowledger.RunSnapshot{RunID: "wfr-session-settle", Status: workflowledger.RunStatusPending, ActiveStepID: "one"}
	if err := repo.CreateRun(ctx, run, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, run.RunID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}

	settleSessionRunFailure(repo, run.RunID, errors.New("ledger read: database is locked"))

	after, err := repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run status = %q, want failed: a run whose controller stopped must not look alive", after.Status)
	}
}

// A cancelled run is left alone. Cancel settles the run itself, and a failed
// status written here would race it and win.
func TestSessionRunFailureLeavesACancelledRunAlone(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	run := workflowledger.RunSnapshot{RunID: "wfr-session-cancel", Status: workflowledger.RunStatusPending, ActiveStepID: "one"}
	if err := repo.CreateRun(ctx, run, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	stored, _ := repo.GetRun(ctx, run.RunID)
	if err := repo.CompareAndSetRunStatus(ctx, run.RunID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}

	settleSessionRunFailure(repo, run.RunID, context.Canceled)

	after, _ := repo.GetRun(ctx, run.RunID)
	if after.Status != workflowledger.RunStatusRunning {
		t.Fatalf("run status = %q, want running: cancel owns this run's outcome", after.Status)
	}
}

// A run that already settled is never overwritten.
func TestSessionRunFailureDoesNotOverwriteATerminalRun(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	run := workflowledger.RunSnapshot{RunID: "wfr-session-terminal", Status: workflowledger.RunStatusPending, ActiveStepID: "one"}
	if err := repo.CreateRun(ctx, run, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	stored, _ := repo.GetRun(ctx, run.RunID)
	if err := repo.CompareAndSetRunStatus(ctx, run.RunID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	running, _ := repo.GetRun(ctx, run.RunID)
	if err := repo.CompareAndSetRunStatus(ctx, run.RunID, running.Version, workflowledger.RunStatusSucceeded, nil); err != nil {
		t.Fatal(err)
	}

	settleSessionRunFailure(repo, run.RunID, errors.New("late error"))

	after, _ := repo.GetRun(ctx, run.RunID)
	if after.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run status = %q, want succeeded: a settled run must not be overwritten", after.Status)
	}
}
