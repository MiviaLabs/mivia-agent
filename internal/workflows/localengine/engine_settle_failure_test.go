package localengine

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// This file covers Engine.settleRunFailure (engine_deliver.go), the shipped
// safety net that settles a run to failed when the launch goroutine's
// controller returns a non-cancel error without settling the run terminal
// itself (e.g. a durable ledger write fault mid-step). The four direct branch
// tests exercise the settle, abandoned-skip, terminal-skip, and
// foreign-claim-skip paths; the Start-wiring test pins the launch goroutine ->
// settleRunFailure path end to end.

// settleRunFailureRun seeds one run and CASes it to running, the state a
// controller leaves behind when it errors mid-step.
func settleRunFailureRun(t *testing.T, repo workflowledger.Repository, runID string) {
	t.Helper()
	ctx := context.Background()
	snap := workflowledger.RunSnapshot{
		RunID: runID, WorkflowName: "two-step", WorkflowDigest: "digest",
		Status: workflowledger.RunStatusPending,
	}
	if err := repo.CreateRun(ctx, snap, []byte(`{}`)); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	cur, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, cur.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatalf("CAS to running: %v", err)
	}
}

// TestSettleRunFailureSettlesNonTerminalRunAndReleasesClaim is the success
// branch: a running, non-abandoned run with no competing holder settles to
// failed and the settle claim is released (the run must not be left claimed
// by a dead executor).
func TestSettleRunFailureSettlesNonTerminalRunAndReleasesClaim(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	ctx := context.Background()
	runID := "wfr-settle-ok"
	settleRunFailureRun(t, repo, runID)

	engine := &Engine{Repo: repo}
	engine.settleRunFailure(runID, errors.New("boom"))

	fresh, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run status = %q, want failed", fresh.Status)
	}
	if _, _, ok, err := repo.GetRunClaim(ctx, runID); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("settle claim was not released after the run settled")
	}
}

// TestSettleRunFailureSkipsAbandonedRun is the negative branch: an abandoned
// run (fence.abandon, set by Interrupt) must stay non-terminal so the run
// remains resumable; Interrupt owns that outcome and settleRunFailure must
// not fight it.
func TestSettleRunFailureSkipsAbandonedRun(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	ctx := context.Background()
	runID := "wfr-settle-abandon"
	settleRunFailureRun(t, repo, runID)

	engine := &Engine{Repo: repo}
	engine.ctrlRepo() // create the abandon fence
	engine.fence.abandon(runID)

	engine.settleRunFailure(runID, errors.New("boom"))

	fresh, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != workflowledger.RunStatusRunning {
		t.Fatalf("abandoned run status = %q, want running (kept non-terminal for resume)", fresh.Status)
	}
	if _, _, ok, err := repo.GetRunClaim(ctx, runID); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("abandoned run must not be claimed by settleRunFailure")
	}
}

// TestSettleRunFailureLeavesTerminalRunUnchanged is the terminal-skip branch:
// a run that is already terminal (failed) must not be touched; its status and
// version stay exactly as they were and the probe claim is released.
func TestSettleRunFailureLeavesTerminalRunUnchanged(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	ctx := context.Background()
	runID := "wfr-settle-terminal"
	settleRunFailureRun(t, repo, runID)
	cur, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, cur.Version, workflowledger.RunStatusFailed, nil); err != nil {
		t.Fatal(err)
	}
	before, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}

	engine := &Engine{Repo: repo}
	engine.settleRunFailure(runID, errors.New("boom"))

	after, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != before.Status || after.Version != before.Version {
		t.Fatalf("terminal run changed by settleRunFailure: before %+v after %+v", before, after)
	}
	if _, _, ok, err := repo.GetRunClaim(ctx, runID); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("settle probe claim was not released on the terminal-skip branch")
	}
}

// TestSettleRunFailureSkipsForeignClaimAndLeavesItIntact is the claim
// contention branch: when another holder owns the run, settleRunFailure must
// leave it alone (that holder is the live executor) and must not disturb the
// existing claim.
func TestSettleRunFailureSkipsForeignClaimAndLeavesItIntact(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	ctx := context.Background()
	runID := "wfr-settle-foreign"
	settleRunFailureRun(t, repo, runID)
	if err := repo.ClaimRun(ctx, runID, "other-holder"); err != nil {
		t.Fatal(err)
	}

	engine := &Engine{Repo: repo}
	engine.settleRunFailure(runID, errors.New("boom"))

	fresh, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != workflowledger.RunStatusRunning {
		t.Fatalf("foreign-claimed run status = %q, want running (the owner settles it)", fresh.Status)
	}
	holder, _, ok, err := repo.GetRunClaim(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || holder != "other-holder" {
		t.Fatalf("claim = (holder %q, ok %v), want the foreign holder intact", holder, ok)
	}
}

// failOnceListStepAttemptsRepo fails exactly one ListStepAttempts call (the
// call whose ordinal is failOn) and passes every other repository method
// through to the inner repository. The controller's advanceAgentStep lists
// attempts as its first action, so a fail-once list makes the controller error
// without settling the run — exactly the condition settleRunFailure exists
// for. Thread-safe: the launch goroutine runs concurrently with the caller.
type failOnceListStepAttemptsRepo struct {
	workflowledger.Repository
	mu     sync.Mutex
	calls  int
	failOn int
}

func (r *failOnceListStepAttemptsRepo) ListStepAttempts(ctx context.Context, runID string) ([]workflowledger.StepAttempt, error) {
	r.mu.Lock()
	r.calls++
	call := r.calls
	r.mu.Unlock()
	if call == r.failOn {
		return nil, errors.New("injected ListStepAttempts failure")
	}
	return r.Repository.ListStepAttempts(ctx, runID)
}

// TestStartSettlesRunFailedWhenControllerErrors pins the launch wiring end to
// end: a Start whose controller errors mid-step (here a durable ledger
// ListStepAttempts write fault on the step's first read) must settle the run
// to failed via settleRunFailure, so the run reaches a terminal state the
// operator can act on instead of wedging in running. Call 1 of ListStepAttempts
// is startNew's synchronous writeRunTrace; call 2 is the launch goroutine's
// advanceAgentStep. After the settle, Wait returns and the ledger reports the
// run failed.
func TestStartSettlesRunFailedWhenControllerErrors(t *testing.T) {
	repoRoot, _ := coverageDeliveryRepo(t)
	coverageWriteFile(t, filepath.Join(repoRoot, ".mivia", "workflows", "two-step.toml"), coverageTwoStepTOML)

	repo := workflowledger.NewMemoryRepository()
	wrapped := &failOnceListStepAttemptsRepo{Repository: repo, failOn: 2}
	engine := &Engine{
		WorkspaceRoot: repoRoot,
		Repo:          wrapped,
		NewRunner: func() controller.AgentStepRunner {
			return &StaticStepRunner{Output: json.RawMessage(`{"ok":true}`)}
		},
		NewRunID: func() string { return "wfr-settle-wire" },
	}
	started, err := engine.Start(context.Background(), workflowledger.StartRequest{
		Workflow: "two-step", Inputs: map[string]any{"task": "x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := engine.Wait(waitCtx, started.RunID); err != nil {
		t.Fatal(err)
	}
	fresh, err := repo.GetRun(context.Background(), started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run status after controller error = %q, want failed (settleRunFailure must settle it)", fresh.Status)
	}
	if _, _, ok, err := repo.GetRunClaim(context.Background(), started.RunID); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("run was left claimed after settleRunFailure")
	}
}
