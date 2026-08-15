package coordinator

import (
	"context"
	"encoding/json"
	"testing"
	"time"

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

// A run interrupted with one task already completed and one mid-flight must
// still report one result per task after resume. finalizeDAG emits only the
// re-executed task set, so the completed task's seeded outcome was dropped
// from Join's Results - the resumed run reported a partial result set even
// though the recovery contract promises one result per task.
func TestResumeReportsOneResultPerTaskIncludingCompleted(t *testing.T) {
	c, repo := resumeLifecycleFixture(t, func(ctx context.Context, repo ledger.LedgerRepository) {
		_ = repo.CreateTask(ctx, lifecycleTask("t1", string(ledger.TaskStatusQueued)))
		_ = repo.CompareAndSetTaskStatus(ctx, "r", "t1", 1, string(ledger.TaskStatusRunning))
		_ = repo.CompareAndSetTaskStatus(ctx, "r", "t1", 2, string(ledger.TaskStatusCompleted))
		_ = repo.CreateTask(ctx, lifecycleTask("t2", string(ledger.TaskStatusQueued)))
		_ = repo.CompareAndSetTaskStatus(ctx, "r", "t2", 1, string(ledger.TaskStatusRunning))
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
	byID := make(map[string]subagents.Result, len(res.Results))
	for _, r := range res.Results {
		if _, dup := byID[r.TaskID]; dup {
			t.Errorf("duplicate result for task %q", r.TaskID)
		}
		byID[r.TaskID] = r
	}
	if len(res.Results) != 2 {
		t.Fatalf("expected 2 results (completed t1 + re-executed t2), got %d: %+v", len(res.Results), res.Results)
	}
	for _, id := range []string{"t1", "t2"} {
		r, ok := byID[id]
		if !ok {
			t.Errorf("missing result for task %q", id)
			continue
		}
		if r.Status != string(ledger.TaskStatusCompleted) {
			t.Errorf("task %q ended %q; expected %q", id, r.Status, ledger.TaskStatusCompleted)
		}
	}
	// Negative path: the seeded task is neither re-executed nor re-recorded.
	// Its ledger status stays completed, its attempt count stays 1, and no
	// task_completed event is appended for it during the resume (its terminal
	// status was already durable).
	t1, err := repo.GetTask(ctx, "r", "t1")
	if err != nil {
		t.Fatalf("get t1: %v", err)
	}
	if t1.Status != string(ledger.TaskStatusCompleted) {
		t.Errorf("seeded task t1 ledger status = %q; must stay completed", t1.Status)
	}
	if len(t1.Attempts) != 1 {
		t.Errorf("seeded task t1 attempt count = %d; must stay 1 (never re-executed)", len(t1.Attempts))
	}
	events, _ := repo.ListEvents(ctx, "r")
	completedT1 := 0
	for _, ev := range events {
		if ev.TaskID == "t1" && ev.Kind == "task_completed" {
			completedT1++
		}
	}
	if completedT1 != 0 {
		t.Errorf("seeded task t1 recorded %d task_completed events during resume; must be 0 (never re-recorded)", completedT1)
	}
}

// C-1 regression: resumeInterruptedRun registered its handle with an empty
// idempotency key, so newRunHandle never started the evictor goroutine and the
// handle stayed in handlesByRun forever (evictHandleAfterTerminal is the only
// site that deletes from handlesByRun). After the run reaches terminal and the
// retention window elapses, HandleForRun must return nil.
func TestResumedRunHandleEvictedAfterTerminal(t *testing.T) {
	c, _ := resumeLifecycleFixture(t, func(ctx context.Context, repo ledger.LedgerRepository) {
		_ = repo.CreateTask(ctx, lifecycleTask("t1", string(ledger.TaskStatusQueued)))
		_ = repo.CompareAndSetTaskStatus(ctx, "r", "t1", 1, string(ledger.TaskStatusRunning))
	})
	c.handleRetention = 10 * time.Millisecond
	ctx := context.Background()
	h, err := c.ResumeInterruptedRun(ctx, "r")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if _, err := c.Join(ctx, h); err != nil {
		t.Fatalf("join: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for c.HandleForRun("r") != nil && time.Now().Before(deadline) {
		timer := time.NewTimer(time.Millisecond)
		<-timer.C
	}
	if got := c.HandleForRun("r"); got != nil {
		t.Fatalf("resumed run handle was never evicted from handlesByRun; HandleForRun = %v", got)
	}
}

// C-1 negative path: a resume whose execution ended without terminalizing the
// run (done closed on a non-terminal run) used to leak its handle in
// handlesByRun, and the stale handle then permanently refused any later
// in-process resume of the same run via the guard in recovery.go. After the
// retention window the stale handle must be evicted so a later resume succeeds.
func TestStaleResumeHandleDoesNotBlockLaterResume(t *testing.T) {
	c, _ := resumeLifecycleFixture(t, func(ctx context.Context, repo ledger.LedgerRepository) {
		_ = repo.CreateTask(ctx, lifecycleTask("t1", string(ledger.TaskStatusQueued)))
		_ = repo.CompareAndSetTaskStatus(ctx, "r", "t1", 1, string(ledger.TaskStatusRunning))
	})
	c.handleRetention = 10 * time.Millisecond
	// Stage the leaked state exactly as the finding describes: an empty-key
	// resume handle whose execution ended (done closed) without the run
	// reaching terminal, so nothing ever evicted it.
	stale := c.newRunHandle("r", "", map[string]string{"t1": "attempt-1"}, "", false)
	close(stale.done)
	deadline := time.Now().Add(time.Second)
	for c.HandleForRun("r") != nil && time.Now().Before(deadline) {
		timer := time.NewTimer(time.Millisecond)
		<-timer.C
	}
	ctx := context.Background()
	h, err := c.ResumeInterruptedRun(ctx, "r")
	if err != nil {
		t.Fatalf("later resume refused by stale handle: %v", err)
	}
	if _, err := c.Join(ctx, h); err != nil {
		t.Fatalf("join: %v", err)
	}
}

// A run interrupted with every task already terminal (completed +
// cancel_requested) resumes to an immediately-available result set. Nothing
// is left to re-execute, so every result comes from the seeded outcomes -
// without the merge, Join returned zero results for a run whose tasks had all
// finished.
func TestResumeReportsSeededResultsWhenAllTasksTerminal(t *testing.T) {
	c, _ := resumeLifecycleFixture(t, func(ctx context.Context, repo ledger.LedgerRepository) {
		_ = repo.CreateTask(ctx, lifecycleTask("t1", string(ledger.TaskStatusQueued)))
		_ = repo.CompareAndSetTaskStatus(ctx, "r", "t1", 1, string(ledger.TaskStatusRunning))
		_ = repo.CompareAndSetTaskStatus(ctx, "r", "t1", 2, string(ledger.TaskStatusCompleted))
		_ = repo.CreateTask(ctx, lifecycleTask("t2", string(ledger.TaskStatusQueued)))
		_ = repo.CompareAndSetTaskStatus(ctx, "r", "t2", 1, string(ledger.TaskStatusRunning))
		_ = repo.CompareAndSetTaskStatus(ctx, "r", "t2", 2, string(ledger.TaskStatusCancelRequested))
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
	byID := make(map[string]subagents.Result, len(res.Results))
	for _, r := range res.Results {
		byID[r.TaskID] = r
	}
	if len(res.Results) != 2 {
		t.Fatalf("expected 2 results (both seeded), got %d: %+v", len(res.Results), res.Results)
	}
	if r, ok := byID["t1"]; !ok {
		t.Error("missing result for task t1")
	} else if r.Status != string(ledger.TaskStatusCompleted) {
		t.Errorf("t1 ended %q; expected completed", r.Status)
	}
	if r, ok := byID["t2"]; !ok {
		t.Error("missing result for task t2")
	} else if r.Status != string(ledger.TaskStatusCanceled) || r.Err == nil {
		t.Errorf("t2 ended status=%q err=%v; expected canceled with a non-nil error", r.Status, r.Err)
	}
}
