package controller

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// neverSettlingHandler never returns on its own: it blocks until its context
// is canceled, simulating a child whose coordinator run handle never settles
// (hung pool worker, stuck referral wait, dead executor). Without a
// controller-side join watchdog the coordinator's Join blocks on <-h.done
// forever and the workflow run parks at the current attempt; with the watchdog
// the controller cancels the child and settles the attempt timed_out within
// the bound instead of blocking indefinitely.
type neverSettlingHandler struct {
	mu      sync.Mutex
	invoked int
}

func (h *neverSettlingHandler) Invoke(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
	h.mu.Lock()
	h.invoked++
	h.mu.Unlock()
	<-ctx.Done()
	return nil, ctx.Err()
}

func (h *neverSettlingHandler) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.invoked
}

// TestWatchJoinLivenessStopJoinsInFlightEmit pins the F16 fix: stop() must
// BLOCK until the watchdog goroutine has fully exited, so a heartbeat emit
// already in flight when stop is called can never still be running (and so
// never still be writing to the ledger) after the caller observes stop
// returning. Before the fix, stop() only closed a channel and returned
// immediately, leaving a window where an in-flight emit (mid persistDurable
// Heartbeat ledger write) could complete after joinWithCancellation, and
// therefore Run(), had already returned to its caller.
func TestWatchJoinLivenessStopJoinsInFlightEmit(t *testing.T) {
	ResetStepHeartbeats()
	defer ResetStepHeartbeats()

	const taskID = "task-stop-joins-emit"
	releaseEmit := make(chan struct{})
	emitStarted := make(chan struct{}, 1)
	var emitInFlight, emitFinished atomicBool

	emit := func(ProgressEvent) {
		emitInFlight.set(true)
		select {
		case emitStarted <- struct{}{}:
		default:
		}
		<-releaseEmit
		emitFinished.set(true)
		emitInFlight.set(false)
	}

	joinCtx, joinCancel := context.WithCancel(context.Background())
	defer joinCancel()
	stop := watchJoinLiveness(joinCtx, joinCancel, taskID, 20*time.Millisecond, emit)

	<-emitStarted // the watchdog tick is now blocked inside emit()

	stopReturned := make(chan struct{})
	go func() {
		close(releaseEmit) // let the in-flight emit proceed to completion
		stop()
		close(stopReturned)
	}()

	select {
	case <-stopReturned:
	case <-time.After(3 * time.Second):
		t.Fatal("stop() did not return after the in-flight emit was released")
	}
	if !emitFinished.get() {
		t.Fatal("stop() returned before the in-flight emit finished; want stop to join the goroutine")
	}
	if emitInFlight.get() {
		t.Fatal("emit still marked in-flight after stop() returned")
	}
}

// atomicBool is a tiny mutex-guarded bool for the test above; avoids sync/
// atomic.Bool version constraints and keeps the assertion readable.
type atomicBool struct {
	mu sync.Mutex
	v  bool
}

func (b *atomicBool) set(v bool) {
	b.mu.Lock()
	b.v = v
	b.mu.Unlock()
}

func (b *atomicBool) get() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.v
}

// TestJoinBoundHonorsTaskTimeoutOverWatchdog pins that the 10-minute join
// watchdog is a last-resort bound ONLY when nothing else bounds the join: a
// long task timeout (e.g. one derived from a 24h run deadline) must be
// honored, never truncated by the watchdog. A workflow step with a 24h task
// timeout must be able to run for 24h, not be killed after 10 minutes.
func TestJoinBoundHonorsTaskTimeoutOverWatchdog(t *testing.T) {
	// A 24h task timeout governs over the 10-minute default watchdog.
	if got := joinBound(context.Background(), 24*time.Hour, 10*time.Minute); got != 24*time.Hour {
		t.Fatalf("joinBound = %s, want 24h (task timeout governs, watchdog must not truncate)", got)
	}
	// With no task timeout and no parent deadline the watchdog still applies:
	// a child that never settles must not park the controller forever.
	if got := joinBound(context.Background(), 0, 10*time.Minute); got != 10*time.Minute {
		t.Fatalf("joinBound = %s, want the watchdog when nothing else bounds the join", got)
	}
	// A parent (run) deadline sooner than the task timeout wins.
	parent, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if got := joinBound(parent, 24*time.Hour, 10*time.Minute); got <= 0 || got > 5*time.Minute {
		t.Fatalf("joinBound = %s, want the ~5m parent deadline (<= 5m, > 0)", got)
	}
}

// TestAgentStepJoinWatchdogSettlesNeverSettlingChild pins the controller-side
// join bound: a child that never settles (no task timeout, no parent deadline,
// so coordinator.Join has no bound of its own) must not park the controller
// forever. The runner's injected join watchdog cancels the child and the
// attempt settles timed_out with an error naming the join timeout, so the run
// reaches a terminal status within the bound instead of staying 'running' at
// the current attempt.
func TestAgentStepJoinWatchdogSettlesNeverSettlingChild(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	handler := &neverSettlingHandler{}
	if err := d.Register(runtime.Subagent, "dev", handler); err != nil {
		t.Fatal(err)
	}
	p := subagents.New(d, subagents.Policy{Workers: 1})
	coordRepo := ledger.NewMemoryLedgerRepository()
	coord := coordinator.New(coordRepo, p).WithRetryPolicy(coordinator.NoRetry)

	// The watchdog is the ONLY bound here: no parent deadline (Background)
	// and no per-agent timeout (agentStepRequest derives none without a
	// deadline). The child never settles and never heartbeats, so the
	// liveness-gated watchdog cancels the silent join at the full bound.
	runner := NewCoordinatorRunner(coord)
	runner.JoinWatchdog = 200 * time.Millisecond
	ctrl, repo := newErrorController(t, runner, "wfr-join-watchdog")

	started := time.Now()
	got, err := ctrl.Run(context.Background())
	elapsed := time.Since(started)

	if err == nil {
		t.Fatalf("run succeeded = %+v; want failure for a never-settling child", got)
	}
	if !strings.Contains(err.Error(), "join timed out") {
		t.Fatalf("error = %v, want it to name the join timeout", err)
	}
	if got.Status != workflowledger.RunStatusTimedOut {
		t.Fatalf("status = %q, want timed_out (a join watchdog expiry is a run timeout, not a cancel)", got.Status)
	}
	if elapsed > 15*time.Second {
		t.Fatalf("run settled after %s; want it bounded by the injected watchdog (~200ms), not blocking", elapsed)
	}
	if n := handler.count(); n != 1 {
		t.Fatalf("child invocations = %d, want exactly 1 (no re-dispatch, no retry)", n)
	}
	attempts, err := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].Status != workflowledger.AttemptStatusTimedOut {
		t.Fatalf("attempts = %+v, want exactly one timed_out attempt (wf_attempt_completed must be durable)", attempts)
	}
	if attempts[0].ErrorRef == "" {
		t.Fatal("attempt ErrorRef is empty; want the join-timeout detail persisted")
	}
	body, err := repo.LoadContent(context.Background(), attempts[0].ErrorRef)
	if err != nil {
		t.Fatalf("load error content: %v", err)
	}
	if !strings.Contains(string(body), "join timed out") {
		t.Fatalf("error content %q does not name the join timeout", body)
	}
}

// TestJoinWatchdogDoesNotKillLiveChild pins the liveness gate of the join
// watchdog: a child that emits subagent heartbeats for its task id is live
// and must NEVER be canceled by the watchdog, no matter how long the join
// runs. The watchdog cancels the join only when the child is silent for the
// full bound. Heartbeats are registered for the task id on a ticker shorter
// than the bound; once the heartbeats stop, the same watchdog must cancel
// the now-silent child within the bound.
func TestJoinWatchdogDoesNotKillLiveChild(t *testing.T) {
	ResetStepHeartbeats()
	defer ResetStepHeartbeats()

	const taskID = "task-live-join"
	const watchdog = 600 * time.Millisecond

	d := runtime.New(runtime.Policy{})
	handler := &neverSettlingHandler{}
	if err := d.Register(runtime.Subagent, "dev", handler); err != nil {
		t.Fatal(err)
	}
	p := subagents.New(d, subagents.Policy{Workers: 1})
	coordRepo := ledger.NewMemoryLedgerRepository()
	coord := coordinator.New(coordRepo, p).WithRetryPolicy(coordinator.NoRetry)

	h, err := coord.EnsureRun(context.Background(), coordinator.EnsureRunRequest{
		RunID:                coordinator.NewRunID(),
		Tasks:                []subagents.Task{{ID: taskID, Name: "dev", AgentName: "dev"}},
		IdempotencyKey:       "workflow-step/live-join",
		NonInteractiveParent: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	spec := AgentStepRequest{WorkflowRunID: "wfr-live-join", StepID: "one", AttemptNo: 1,
		TaskID: taskID, CoordinatorRunID: h.RunID(), AgentName: "dev"}

	stopHeartbeats := make(chan struct{})
	go func() {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopHeartbeats:
				return
			case <-ticker.C:
				NoteStepHeartbeat(taskID)
			}
		}
	}()

	runner := NewCoordinatorRunner(coord)
	runner.JoinWatchdog = watchdog
	done := make(chan error, 1)
	go func() {
		_, joinErr := runner.joinWithCancellation(context.Background(), spec, h)
		done <- joinErr
	}()

	// The child is live: the join must still be open after several watchdog
	// bounds, because the last heartbeat is always far inside the bound.
	select {
	case joinErr := <-done:
		t.Fatalf("join ended while the child was live: %v; want the watchdog to leave a live child alone", joinErr)
	case <-time.After(1500 * time.Millisecond):
	}

	// The child goes silent: the watchdog must cancel the join within one
	// bound plus one tick.
	close(stopHeartbeats)
	select {
	case joinErr := <-done:
		if joinErr == nil || !strings.Contains(joinErr.Error(), "join timed out") {
			t.Fatalf("join error = %v, want it to name the join timeout", joinErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("join did not end after the child went silent; want a watchdog timeout")
	}
}

// TestJoinWatchdogIgnoresStalePreJoinHeartbeat pins the anchor-bound of the
// liveness reference: a heartbeat entry recorded BEFORE this join started (a
// stale pre-join entry left by a previous execution of the same wft- TaskID
// on a same-process resume) must never be used as the reference. The fresh
// child's first heartbeat arrives well after the join start, so the watchdog
// must NOT cancel the live re-dispatched child at the first tick.
func TestJoinWatchdogIgnoresStalePreJoinHeartbeat(t *testing.T) {
	ResetStepHeartbeats()
	defer ResetStepHeartbeats()

	const taskID = "task-stale-join"
	const watchdog = 600 * time.Millisecond

	// Record a stale pre-join heartbeat long before the join starts, exactly
	// as a previous execution of the same task id would leave behind. The
	// registry's unexported note takes an explicit time, so the entry is aged
	// deterministically instead of sleeping (time.Sleep is banned in tests).
	stepHeartbeats.note(taskID, time.Now().Add(-time.Hour))

	// The fresh child's first heartbeat lands after the first watchdog tick,
	// so the registry still holds only the stale pre-join entry at first tick.
	coord, h, spec, stopHeartbeats := newJoinWatchdogHarness(t, taskID, "workflow-step/stale-join", "wfr-stale-join", 1, 300*time.Millisecond)

	runner := NewCoordinatorRunner(coord)
	runner.JoinWatchdog = watchdog
	done := make(chan error, 1)
	go func() {
		_, joinErr := runner.joinWithCancellation(context.Background(), spec, h)
		done <- joinErr
	}()

	// The first tick fires at ~100 ms (bound/8 floored at 100 ms). The stale
	// entry is older than the bound, so the old code canceled the live child
	// right here; the join must instead stay open because the reference is
	// bounded by the join start, not the stale entry.
	select {
	case joinErr := <-done:
		t.Fatalf("join ended at the first tick with a stale pre-join heartbeat: %v; want the reference bounded by the join start", joinErr)
	case <-time.After(2500 * time.Millisecond):
	}

	// The child goes silent: the watchdog must cancel the join within one
	// bound plus one tick.
	close(stopHeartbeats)
	select {
	case joinErr := <-done:
		if joinErr == nil || !strings.Contains(joinErr.Error(), "join timed out") {
			t.Fatalf("join error = %v, want it to name the join timeout", joinErr)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("join did not end after the child went silent; want a watchdog timeout")
	}
}

// TestJoinWatchdogFixedBoundFallbackForUnknownTask pins the fixed-bound
// fallback of the liveness-gated watchdog: with NO registry entry for the
// task id, the watchdog must cancel the silent child after the full bound,
// exactly like the pre-liveness fixed bound. It must not time out early and
// must not hang.
func TestJoinWatchdogFixedBoundFallbackForUnknownTask(t *testing.T) {
	ResetStepHeartbeats()
	defer ResetStepHeartbeats()

	const taskID = "task-unknown-join"
	const watchdog = 200 * time.Millisecond

	d := runtime.New(runtime.Policy{})
	handler := &neverSettlingHandler{}
	if err := d.Register(runtime.Subagent, "dev", handler); err != nil {
		t.Fatal(err)
	}
	p := subagents.New(d, subagents.Policy{Workers: 1})
	coordRepo := ledger.NewMemoryLedgerRepository()
	coord := coordinator.New(coordRepo, p).WithRetryPolicy(coordinator.NoRetry)

	h, err := coord.EnsureRun(context.Background(), coordinator.EnsureRunRequest{
		RunID:                coordinator.NewRunID(),
		Tasks:                []subagents.Task{{ID: taskID, Name: "dev", AgentName: "dev"}},
		IdempotencyKey:       "workflow-step/unknown-join",
		NonInteractiveParent: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	spec := AgentStepRequest{WorkflowRunID: "wfr-unknown-join", StepID: "one", AttemptNo: 1,
		TaskID: taskID, CoordinatorRunID: h.RunID(), AgentName: "dev"}

	runner := NewCoordinatorRunner(coord)
	runner.JoinWatchdog = watchdog
	started := time.Now()
	_, joinErr := runner.joinWithCancellation(context.Background(), spec, h)
	elapsed := time.Since(started)

	if joinErr == nil || !strings.Contains(joinErr.Error(), "join timed out") {
		t.Fatalf("join error = %v, want it to name the join timeout", joinErr)
	}
	if elapsed < watchdog-50*time.Millisecond {
		t.Fatalf("join ended after %s; want it to hold the full bound (~%s)", elapsed, watchdog)
	}
	if elapsed > 15*time.Second {
		t.Fatalf("join ended after %s; want it bounded by the injected watchdog (~%s)", elapsed, watchdog)
	}
}
