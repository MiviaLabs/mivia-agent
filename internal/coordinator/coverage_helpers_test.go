package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

func TestCoverageHelpersResultsFromSnapshots(t *testing.T) {
	results := ResultsFromSnapshots([]ledger.TaskSnapshot{
		{TaskID: "completed", Status: string(ledger.TaskStatusCompleted)},
		{TaskID: "failed", Status: string(ledger.TaskStatusFailed), ErrorRef: "content:error-1"},
		{TaskID: "timed-out", Status: string(ledger.TaskStatusTimedOut)},
	})
	if len(results) != 3 {
		t.Fatalf("result count = %d, want 3", len(results))
	}
	if results[0].Err != nil {
		t.Fatalf("completed result error = %v", results[0].Err)
	}
	if results[0].Provenance.Kind != "recovered" {
		t.Fatalf("completed provenance = %#v", results[0].Provenance)
	}
	if results[1].Err == nil || !strings.Contains(results[1].Err.Error(), "content:error-1") {
		t.Fatalf("failed result error = %v, want error reference", results[1].Err)
	}
	if results[2].Err == nil || !strings.Contains(results[2].Err.Error(), "no error content reference") {
		t.Fatalf("timed-out result error = %v, want missing-reference detail", results[2].Err)
	}
}

func TestCoverageHelpersDoneChannels(t *testing.T) {
	retry := NewRetryState("task", NoRetry)
	select {
	case <-retry.Done():
		t.Fatal("retry Done closed before Exhausted")
	default:
	}
	retry.Exhausted()
	select {
	case <-retry.Done():
	default:
		t.Fatal("retry Done did not close after Exhausted")
	}

	done := make(chan struct{})
	handle := &RunHandle{done: done}
	if handle.Done() != done {
		t.Fatal("RunHandle.Done did not return the handle channel")
	}
}

func TestCoverageHelpersListInterruptedAndCleanup(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	repo := ledger.NewStorageLedgerRepository(store)
	if err := repo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: "queued", DisplayName: "queued", Status: ledger.RunStatusQueued}); err != nil {
		t.Fatalf("CreateRun queued: %v", err)
	}
	if err := repo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: "finished", DisplayName: "finished", Status: ledger.RunStatusCompleted}); err != nil {
		t.Fatalf("CreateRun finished: %v", err)
	}
	if err := repo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: "free", DisplayName: "free", Status: ledger.RunStatusCreated}); err != nil {
		t.Fatalf("CreateRun free: %v", err)
	}
	if err := repo.ClaimRun(ctx, "queued", "other-executor"); err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	coord := New(repo, nil).(*coordinator)
	runs, err := coord.ListInterruptedRuns(ctx)
	if err != nil {
		t.Fatalf("ListInterruptedRuns: %v", err)
	}
	if len(runs) != 2 || runs[0].RunID != "free" || runs[0].HeldByAnotherExecutor || runs[1].RunID != "queued" || !runs[1].HeldByAnotherExecutor {
		t.Fatalf("interrupted runs = %#v", runs)
	}

	memoryRepo := ledger.NewMemoryLedgerRepository()
	cleanup := New(memoryRepo, nil).(*coordinator)
	if err := memoryRepo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: "cleanup", Status: ledger.RunStatusCreated}); err != nil {
		t.Fatalf("CreateRun cleanup: %v", err)
	}
	if err := memoryRepo.ClaimRun(ctx, "cleanup", cleanup.holderID); err != nil {
		t.Fatalf("ClaimRun cleanup: %v", err)
	}
	cleanup.releaseAndDeleteRun(ctx, "cleanup")
	if _, err := memoryRepo.GetRun(ctx, "cleanup"); !errors.Is(err, ledger.ErrNotFound) {
		t.Fatalf("GetRun after cleanup = %v, want ErrNotFound", err)
	}
	noRecoveryRuns, err := New(memoryRepo, nil).ListInterruptedRuns(ctx)
	if err != nil || noRecoveryRuns != nil {
		t.Fatalf("memory recovery listing = %#v, %v; want nil, nil", noRecoveryRuns, err)
	}
}

func TestCoverageHelpersRunDAGExecutesPreparedTask(t *testing.T) {
	ctx := context.Background()
	repo := ledger.NewMemoryLedgerRepository()
	dispatcher := runtime.New(runtime.Policy{})
	if err := dispatcher.Register(runtime.Subagent, "worker", staticHandler{out: json.RawMessage(`{"ok":true}`)}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	coord := New(repo, subagents.New(dispatcher, subagents.Policy{Workers: 1})).(*coordinator)
	const runID = "direct-dag"
	if err := repo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: runID, Status: ledger.RunStatusRunning}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	tasks := []subagents.Task{{ID: "task", Name: "worker"}}
	named, err := coord.createTasks(ctx, runID, tasks, time.Now())
	if err != nil {
		t.Fatalf("createTasks: %v", err)
	}
	handle := coord.newRunHandle(runID, "", map[string]string{"task": named[0].attemptID}, "", false)
	results, err := coord.runDAG(handle, tasks)
	if err != nil {
		t.Fatalf("runDAG: %v", err)
	}
	if len(results) != 1 || results[0].TaskID != "task" || results[0].Err != nil {
		t.Fatalf("runDAG results = %#v", results)
	}
}
