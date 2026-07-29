package coordinator

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

func resumeLifecycleFixture(t *testing.T, seed func(ctx context.Context, repo ledger.LedgerRepository)) (*coordinator, ledger.LedgerRepository) {
	t.Helper()
	repo := ledger.NewMemoryLedgerRepository()
	ctx := context.Background()
	if err := repo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: "r", Status: ledger.RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	seed(ctx, repo)
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "worker", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	// Production wiring: coordinator.New, no WithRetryPolicy.
	return New(repo, subagents.New(d, subagents.Policy{Workers: 1, Partial: true})).(*coordinator), repo
}

func lifecycleTask(id, status string, deps ...string) ledger.TaskSnapshot {
	return ledger.TaskSnapshot{
		RunID: "r", TaskID: id, Status: status, Version: 1,
		HandlerName: "worker", Input: json.RawMessage(`{"p":1}`), DependsOn: deps,
	}
}

// Resume must re-execute interrupted work under the PRODUCTION wiring, which
// configures no retry policy (New sets NoRetry; WithRetryPolicy has no caller).
// Previously resume drove the task to terminal failed and stopped there, so
// calling resume destroyed the run rather than resuming it.
func TestResumeReExecutesInterruptedTaskWithDefaultPolicy(t *testing.T) {
	c, repo := resumeLifecycleFixture(t, func(ctx context.Context, repo ledger.LedgerRepository) {
		_ = repo.CreateTask(ctx, lifecycleTask("t1", string(ledger.TaskStatusQueued)))
		_ = repo.CompareAndSetTaskStatus(ctx, "r", "t1", 1, string(ledger.TaskStatusRunning))
	})
	ctx := context.Background()
	h, err := c.ResumeInterruptedRun(ctx, "r")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if _, err := c.Join(ctx, h); err != nil {
		t.Fatalf("join: %v", err)
	}
	tasks, _ := repo.ListTasks(ctx, "r")
	t.Logf("t1 final status = %q", tasks[0].Status)
	if tasks[0].Status != string(ledger.TaskStatusCompleted) {
		t.Fatalf("interrupted task ended %q; resume must re-execute it", tasks[0].Status)
	}
}

// A task that already completed must not be re-dispatched. Its transition back
// to running is rejected, the DAG then reports it failed, and collectReady
// drives every dependent to terminal blocked — so resuming a partly-completed
// run destroyed exactly the work that had not started yet.
func TestResumeDoesNotBlockDependentsOfCompletedTasks(t *testing.T) {
	c, repo := resumeLifecycleFixture(t, func(ctx context.Context, repo ledger.LedgerRepository) {
		_ = repo.CreateTask(ctx, lifecycleTask("t1", string(ledger.TaskStatusQueued)))
		_ = repo.CompareAndSetTaskStatus(ctx, "r", "t1", 1, string(ledger.TaskStatusRunning))
		_ = repo.CompareAndSetTaskStatus(ctx, "r", "t1", 2, string(ledger.TaskStatusCompleted))
		_ = repo.CreateTask(ctx, lifecycleTask("t2", string(ledger.TaskStatusQueued), "t1"))
	})
	ctx := context.Background()
	h, err := c.ResumeInterruptedRun(ctx, "r")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	res, err := c.Join(ctx, h)
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	for _, r := range res.Results {
		t.Logf("result %s -> status=%s err=%v", r.TaskID, r.Status, r.Err)
	}
	tasks, _ := repo.ListTasks(ctx, "r")
	for _, ts := range tasks {
		t.Logf("task %s final status = %q", ts.TaskID, ts.Status)
		if ts.TaskID == "t2" && ts.Status != string(ledger.TaskStatusCompleted) {
			t.Errorf("dependent t2 ended %q; a completed dependency must not block it", ts.Status)
		}
	}
}
