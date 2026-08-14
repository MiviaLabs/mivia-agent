package coordinator

// Pins the live panel run-killer: executor A's claim lease expires while a
// panel member's provider call is still in flight; a rejoining executor B
// marks the running task failed (markInterruptedTasks). A's real result then
// arrives and recordTaskResult reads the fresh snapshot (version current,
// status failed) and previously CASed failed -> completed - an edge the
// transition table forbids - so every such rejoin killed the whole workflow
// run with "update task ...: invalid state transition". A late write losing
// to a settled terminal record is a benign lost race: the durable status
// wins and no run error is joined.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

func settledRaceFixture(t *testing.T) (*coordinator, ledger.LedgerRepository) {
	t.Helper()
	repo := ledger.NewMemoryLedgerRepository()
	ctx := context.Background()
	if err := repo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: "r", Status: ledger.RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateTask(ctx, lifecycleTask("t1", string(ledger.TaskStatusQueued))); err != nil {
		t.Fatal(err)
	}
	// queued -> running (executor A dispatches), then running -> failed
	// (executor B's markInterruptedTasks after A's lease expired).
	if err := repo.CompareAndSetTaskStatus(ctx, "r", "t1", 1, string(ledger.TaskStatusRunning)); err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetTaskStatus(ctx, "r", "t1", 2, string(ledger.TaskStatusFailed)); err != nil {
		t.Fatal(err)
	}
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "worker", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	return New(repo, subagents.New(d, subagents.Policy{Workers: 1})).(*coordinator), repo
}

func TestLateResultAgainstSettledTaskJoinsNoError(t *testing.T) {
	c, repo := settledRaceFixture(t)
	ctx := context.Background()
	snap, err := repo.GetTask(ctx, "r", "t1")
	if err != nil {
		t.Fatal(err)
	}
	var runErr error
	// Executor A's late completed result arriving after B settled t1 failed.
	ok := c.tryTaskStatusCAS(ctx, "r", "t1", snap, string(ledger.TaskStatusCompleted), &runErr)
	if ok {
		t.Fatal("tryTaskStatusCAS = true; the settled failed status must win over the late completed write")
	}
	if runErr != nil {
		t.Fatalf("runErr = %v; a late write losing to a settled terminal record must not error the run", runErr)
	}
	after, err := repo.GetTask(ctx, "r", "t1")
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != string(ledger.TaskStatusFailed) {
		t.Fatalf("task status = %q, want the settled failed status untouched", after.Status)
	}
}

func TestStartReadyAdoptsSettledTaskWithoutRunError(t *testing.T) {
	c, repo := settledRaceFixture(t)
	// Panel members run under RunPolicy NoRetry (panel_work_spec.go), so the
	// recovered-retry branch never claims the settled task first.
	h := c.newRunHandle("r", "", map[string]string{"t1": "attempt-1"}, "", false, withRunPolicy(ledger.RunPolicy{NoRetry: true, FailInterrupted: true}))
	pending := map[string]subagents.Task{"t1": {ID: "t1", Name: "worker"}}
	results := map[string]subagents.Result{}
	if err := c.startReady(h, []subagents.Task{{ID: "t1", Name: "worker"}}, pending, results, map[string]time.Time{}, map[string]*RetryState{}); err != nil {
		t.Fatalf("startReady = %v; a task settled by another executor must be adopted, never joined as a run error", err)
	}
	got, ok := results["t1"]
	if !ok || got.Status != string(ledger.TaskStatusFailed) {
		t.Fatalf("results[t1] = %+v (ok=%v), want the settled failed outcome adopted", got, ok)
	}
	if _, stillPending := pending["t1"]; stillPending {
		t.Fatal("t1 still pending; a settled task must leave the dispatch set")
	}
	after, err := repo.GetTask(context.Background(), "r", "t1")
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != string(ledger.TaskStatusFailed) {
		t.Fatalf("task status = %q, want the settled failed status untouched", after.Status)
	}
}
