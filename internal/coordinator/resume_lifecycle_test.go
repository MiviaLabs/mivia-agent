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
	return New(repo, subagents.New(d, subagents.Policy{Workers: 1})).(*coordinator), repo
}

func lifecycleTask(id, status string, deps ...string) ledger.TaskSnapshot {
	return ledger.TaskSnapshot{
		RunID: "r", TaskID: id, Status: status, Version: 1,
		HandlerName: "worker", AgentName: "worker", AgentDigest: "test-digest", Input: json.RawMessage(`{"p":1}`), DependsOn: deps,
		Attempts: []ledger.AttemptSnapshot{{
			AttemptID: id + "-attempt-1", TaskID: id, RunID: "r",
			AttemptNum: 1, Status: string(ledger.TaskStatusQueued),
		}},
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
// drives every dependent to terminal blocked - so resuming a partly-completed
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

// A run interrupted mid-cancel: reconcileCancellation had moved the task to
// cancel_requested before the crash. markInterruptedTasks handles that status
// but targeted `failed`, which the transition table forbids
// (cancel_requested → canceled only). The CAS error was swallowed, so the task
// stayed cancel_requested forever: never terminal, so Recover kept reporting
// the run interrupted and every resume was a silent no-op.
func TestResumeFinalizesCancelRequestedTasks(t *testing.T) {
	c, repo := resumeLifecycleFixture(t, func(ctx context.Context, repo ledger.LedgerRepository) {
		_ = repo.CreateTask(ctx, lifecycleTask("t1", string(ledger.TaskStatusQueued)))
		_ = repo.CompareAndSetTaskStatus(ctx, "r", "t1", 1, string(ledger.TaskStatusRunning))
		_ = repo.CompareAndSetTaskStatus(ctx, "r", "t1", 2, string(ledger.TaskStatusCancelRequested))
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
	if got := tasks[0].Status; got != string(ledger.TaskStatusCanceled) {
		t.Fatalf("cancel_requested task ended %q; resume must finalize it to canceled", got)
	}
}

// The interrupted attempt is a record of an execution that happened. Resume
// reused its AttemptID, so the retry's outcome overwrote it in place and the
// ledger ended up showing a single clean attempt - losing all evidence that an
// interruption and a re-execution ever occurred.
func TestResumeAppendsNewAttemptRatherThanOverwriting(t *testing.T) {
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
	attempts := tasks[0].Attempts
	if len(attempts) < 2 {
		t.Fatalf("expected the interrupted attempt plus a resumed one, got %d: %+v", len(attempts), attempts)
	}
	if attempts[0].Status != string(ledger.TaskStatusFailed) {
		t.Errorf("the interrupted attempt was rewritten to %q; its record must stay immutable", attempts[0].Status)
	}
	if attempts[0].AttemptID == attempts[len(attempts)-1].AttemptID {
		t.Error("resumed execution reused the interrupted AttemptID")
	}
}
