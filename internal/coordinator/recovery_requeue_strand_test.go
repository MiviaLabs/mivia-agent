package coordinator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// TestResumeReQueuesStrandedRetryPendingTask is the DC-1/DC-4 regression test:
// a task stranded at retry_pending (a crash or transient storage error between
// requeueForResume's two CASes left failed -> retry_pending durable but
// retry_pending -> queued never written) must be re-driven on resume.
// markInterruptedTasks only sees Running/CancelRequested and startReady's
// retry_pending -> running CAS is invalid, so requeuePersistedFailures is the
// only path that can unstick it; it must re-queue the task and the DAG must
// execute it to completion.
func TestResumeReQueuesStrandedRetryPendingTask(t *testing.T) {
	c, repo := resumeLifecycleFixture(t, func(ctx context.Context, repo ledger.LedgerRepository) {
		_ = repo.CreateTask(ctx, lifecycleTask("t1", string(ledger.TaskStatusRetryPending)))
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
	if len(tasks) != 1 || tasks[0].Status != string(ledger.TaskStatusCompleted) {
		t.Fatalf("stranded retry_pending task ended %q; resume must re-queue it via requeuePersistedFailures and re-execute it", tasks[0].Status)
	}
}

// requeueBlockingRepo fails every retry_pending -> queued CAS while armed,
// simulating the transient storage error (or crash) between requeueForResume's
// two CASes that strands a task at retry_pending. Disarming it simulates the
// storage recovering before the next resume.
type requeueBlockingRepo struct {
	*ledger.MemoryLedgerRepository
	blockRequeue atomic.Bool
}

func (r *requeueBlockingRepo) CompareAndSetTaskStatus(ctx context.Context, runID, taskID string, expectedVersion uint64, newStatus string) error {
	if newStatus == string(ledger.TaskStatusQueued) {
		if snap, err := r.MemoryLedgerRepository.GetTask(ctx, runID, taskID); err == nil && snap.Status == string(ledger.TaskStatusRetryPending) {
			if r.blockRequeue.Load() {
				return errors.New("simulated transient storage failure on retry_pending -> queued")
			}
		}
	}
	return r.MemoryLedgerRepository.CompareAndSetTaskStatus(ctx, runID, taskID, expectedVersion, newStatus)
}

// TestResumeSurfacesRequeueCASFailureAndNextResumeRecovers is the DC-9/DC-4
// regression test: requeueForResume's second CAS (retry_pending -> queued)
// must not silently discard its result (DC-3/DC-9). When it fails, the failure
// must be logged so the stranded state is visible, and the NEXT resume must
// still recover the task via requeuePersistedFailures' retry_pending re-queue.
func TestResumeSurfacesRequeueCASFailureAndNextResumeRecovers(t *testing.T) {
	ctx := context.Background()
	repo := &requeueBlockingRepo{MemoryLedgerRepository: ledger.NewMemoryLedgerRepository()}
	if err := repo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: "r", Status: ledger.RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateTask(ctx, lifecycleTask("t1", string(ledger.TaskStatusQueued))); err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetTaskStatus(ctx, "r", "t1", 1, string(ledger.TaskStatusRunning)); err != nil {
		t.Fatal(err)
	}

	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "worker", staticHandler{out: json.RawMessage(`{"ok":true}`)})

	// Capture the standard logger so the second-CAS failure is assertable
	// (DC-9: a discarded CAS result hides the stranded state). Package tests
	// run sequentially, so swapping the global log output is safe here.
	var buf bytes.Buffer
	prevWriter, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() { log.SetOutput(prevWriter); log.SetFlags(prevFlags) }()

	repo.blockRequeue.Store(true)
	c := New(repo, subagents.New(d, subagents.Policy{Workers: 1})).(*coordinator)
	h, err := c.ResumeInterruptedRun(ctx, "r")
	if err != nil {
		t.Fatalf("first resume: %v", err)
	}
	// The stranded task surfaces as a failure in this run; Join only waits for
	// the run to settle and the execution claim to be released.
	if _, err := c.Join(ctx, h); err != nil {
		t.Fatalf("first resume join: %v", err)
	}

	snap, err := repo.GetTask(ctx, "r", "t1")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != string(ledger.TaskStatusRetryPending) {
		t.Fatalf("after the failing re-queue CAS the task = %q, want retry_pending (stranded)", snap.Status)
	}
	if got := buf.String(); !strings.Contains(got, `requeue task "t1"`) || !strings.Contains(got, "retry_pending") {
		t.Fatalf("failing second CAS was not surfaced in the log; got:\n%s", got)
	}

	// The storage recovers. A fresh coordinator resumes the same run (the old
	// handle is retained for handleRetention); requeuePersistedFailures must
	// re-drive the stranded retry_pending task and the DAG must complete it.
	repo.blockRequeue.Store(false)
	c2 := New(repo, subagents.New(d, subagents.Policy{Workers: 1})).(*coordinator)
	h2, err := c2.ResumeInterruptedRun(ctx, "r")
	if err != nil {
		t.Fatalf("second resume: %v", err)
	}
	res2, err := c2.Join(ctx, h2)
	if err != nil {
		t.Fatalf("second resume join: %v", err)
	}
	if res2.Err != nil {
		t.Fatalf("second resume run error: %v", res2.Err)
	}
	snap, err = repo.GetTask(ctx, "r", "t1")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != string(ledger.TaskStatusCompleted) {
		t.Fatalf("stranded retry_pending task ended %q after the second resume; want completed", snap.Status)
	}
}
