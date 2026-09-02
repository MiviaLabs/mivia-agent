package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// siblingCancelRun is TestCancelTaskDoesNotAffectSiblings' fixture: a
// "victim" task that blocks until its context is canceled, and a
// "survivor" task that blocks until releaseSurvivor is closed - split out
// of the test itself to keep it under the per-function line budget.
type siblingCancelRun struct {
	repo            ledger.LedgerRepository
	coord           *coordinator
	h               *RunHandle
	releaseSurvivor chan struct{}
}

// spawnSiblingCancelRun spawns the victim/survivor pair and waits for both
// to actually start executing (not merely dispatched) before returning, so
// the caller's CancelTask races nothing at startup.
func spawnSiblingCancelRun(t *testing.T) siblingCancelRun {
	t.Helper()
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})

	victimStarted := make(chan struct{})
	_ = d.Register(runtime.Subagent, "victim", invoker(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		close(victimStarted)
		<-ctx.Done()
		return nil, ctx.Err()
	}))

	survivorStarted := make(chan struct{})
	releaseSurvivor := make(chan struct{})
	_ = d.Register(runtime.Subagent, "survivor", invoker(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		close(survivorStarted)
		select {
		case <-releaseSurvivor:
			return json.RawMessage(`"done"`), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}))

	p := subagents.New(d, subagents.Policy{Workers: 2})
	c := New(repo, p)

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "victim", Name: "victim"},
		{ID: "survivor", Name: "survivor"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	<-victimStarted
	<-survivorStarted

	return siblingCancelRun{repo: repo, coord: c.(*coordinator), h: h, releaseSurvivor: releaseSurvivor}
}

// TestCancelTaskDoesNotAffectSiblings is the central regression test for
// single-task cancellation (slice 2b): canceling ONE of two concurrent tasks
// must (a) transition only that task to canceled, (b) let the other task
// complete normally, and (c) never trigger the run's own completion (h.done)
// via the single-task cancel path — the run keeps going until its other task
// finishes on its own.
func TestCancelTaskDoesNotAffectSiblings(t *testing.T) {
	fx := spawnSiblingCancelRun(t)
	repo, coord, h, releaseSurvivor := fx.repo, fx.coord, fx.h, fx.releaseSurvivor

	cancelCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := coord.CancelTask(cancelCtx, h, "victim"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}

	// (a) the victim task is canceled.
	victimSnap, err := repo.GetTask(context.Background(), h.runID, "victim")
	if err != nil {
		t.Fatal(err)
	}
	if victimSnap.Status != string(ledger.TaskStatusCanceled) {
		t.Fatalf("victim status = %q, want %q", victimSnap.Status, ledger.TaskStatusCanceled)
	}

	// (c) the run itself is not finished by the single-task cancel: the
	// survivor is still running, and h.done must not be closed yet.
	select {
	case <-h.Done():
		t.Fatal("h.done closed by single-task CancelTask; run must keep going")
	default:
	}
	survivorSnap, err := repo.GetTask(context.Background(), h.runID, "survivor")
	if err != nil {
		t.Fatal(err)
	}
	if survivorSnap.Status != string(ledger.TaskStatusRunning) {
		t.Fatalf("survivor status = %q, want %q (must be unaffected by sibling cancel)", survivorSnap.Status, ledger.TaskStatusRunning)
	}

	// (b) let the survivor finish normally; the whole run then completes.
	close(releaseSurvivor)

	result, err := coord.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if got := statusForTaskID(result.Results, "survivor"); got != "completed" {
		t.Fatalf("survivor result status = %q, want completed", got)
	}
	if got := statusForTaskID(result.Results, "victim"); got != "canceled" {
		t.Fatalf("victim result status = %q, want canceled", got)
	}
}

// TestCancelTaskAlreadyTerminalIsNoop proves CancelTask on an already-terminal
// task is a safe no-op: no error, no hang.
func TestCancelTaskAlreadyTerminalIsNoop(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "fast", invoker(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		return json.RawMessage(`"ok"`), nil
	}))
	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(repo, p)

	h, err := c.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: "fast"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Join(context.Background(), h); err != nil {
		t.Fatal(err)
	}

	coord := c.(*coordinator)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := coord.CancelTask(ctx, h, "t1"); err != nil {
		t.Fatalf("CancelTask on terminal task should be a no-op, got error: %v", err)
	}

	snap, err := repo.GetTask(context.Background(), h.runID, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != string(ledger.TaskStatusCompleted) {
		t.Fatalf("t1 status = %q, want completed (unchanged)", snap.Status)
	}
}

// TestCancelTaskUnknownTaskID proves CancelTask on a nonexistent task ID
// returns a clear error rather than panicking or silently succeeding.
func TestCancelTaskUnknownTaskID(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "fast", invoker(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		return json.RawMessage(`"ok"`), nil
	}))
	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(repo, p)

	h, err := c.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: "fast"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Join(context.Background(), h); err != nil {
		t.Fatal(err)
	}

	coord := c.(*coordinator)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err = coord.CancelTask(ctx, h, "does-not-exist")
	if err == nil {
		t.Fatal("expected an error for an unknown task ID, got nil")
	}
}

// TestCancelTaskRecoveredRunRefuses proves CancelTask refuses cleanly on a
// recovered handle (no live in-process execution owner), matching Cancel's
// existing fail-closed treatment of recovered runs rather than silently
// no-oping and lying about having canceled anything.
func TestCancelTaskRecoveredRunRefuses(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	started := make(chan struct{})
	_ = d.Register(runtime.Subagent, "slow", invoker(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(repo, p)

	h, err := c.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: "slow"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	<-started

	// Simulate a recovered handle the way JoinAsRecovered constructs one:
	// same coordinator ownership, recovered=true.
	recovered := &RunHandle{
		runID: h.runID, done: make(chan struct{}), cancelDone: make(chan struct{}),
		owner: h.owner, recovered: true,
	}

	coord := c.(*coordinator)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err = coord.CancelTask(ctx, recovered, "t1")
	if err == nil {
		t.Fatal("expected CancelTask to refuse on a recovered handle, got nil error")
	}

	// Clean up: cancel the real handle so the test process does not leak the
	// still-blocked task goroutine.
	_ = c.Cancel(context.Background(), h)
}

// conflictOnceRepo forces exactly one ledger.ErrConflict on the first
// CompareAndSetTaskStatus call that would transition taskID to toStatus,
// then delegates to the wrapped repository normally thereafter -
// simulating a concurrent CAS conflict (e.g. a racing dispatch or a second
// cancel caller winning the race) that requestSingleTaskCancel's retry loop
// (cancel_task.go:113-114) must survive by re-reading snap and retrying
// instead of giving up.
type conflictOnceRepo struct {
	ledger.LedgerRepository
	taskID   string
	toStatus string

	mu    sync.Mutex
	fired bool
}

func (r *conflictOnceRepo) CompareAndSetTaskStatus(ctx context.Context, runID, taskID string, expectedVersion uint64, newStatus string) error {
	r.mu.Lock()
	if !r.fired && taskID == r.taskID && newStatus == r.toStatus {
		r.fired = true
		r.mu.Unlock()
		return ledger.ErrConflict
	}
	r.mu.Unlock()
	return r.LedgerRepository.CompareAndSetTaskStatus(ctx, runID, taskID, expectedVersion, newStatus)
}

// TestRequestSingleTaskCancelRetriesOnConflict is the CAS-conflict regression
// test for requestSingleTaskCancel's retry loop: a single injected
// ledger.ErrConflict on the queued->cancel_requested CAS must not abort the
// request. The loop must re-read the task's current snapshot and retry,
// landing the task in canceled (via CancelTask's normal dispatched-task
// finalize path) rather than leaving it stuck or erroring out. This kills
// the mutant that removes the `continue` on cancel_task.go:114 (a no-op
// there falls through to the `return false, fmt.Errorf(...)` branch and
// CancelTask would instead observe a hard error).
func TestRequestSingleTaskCancelRetriesOnConflict(t *testing.T) {
	base := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	started := make(chan struct{})
	_ = d.Register(runtime.Subagent, "slow", invoker(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	p := subagents.New(d, subagents.Policy{Workers: 1})
	repo := &conflictOnceRepo{
		LedgerRepository: base,
		taskID:           "t1",
		toStatus:         string(ledger.TaskStatusCancelRequested),
	}
	c := New(repo, p)

	h, err := c.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: "slow"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	<-started

	coord := c.(*coordinator)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := coord.CancelTask(ctx, h, "t1"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}

	repo.mu.Lock()
	fired := repo.fired
	repo.mu.Unlock()
	if !fired {
		t.Fatal("test invalid: the conflict injector never fired, so this proves nothing about the retry path")
	}

	snap, err := base.GetTask(context.Background(), h.runID, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != string(ledger.TaskStatusCanceled) {
		t.Fatalf("task status = %q, want %q (proves the CAS-conflict retry loop actually retried and settled)", snap.Status, ledger.TaskStatusCanceled)
	}
}

// TestCancelTaskNilCancelFuncSkipsInvoke proves the `ok && cancelFn != nil`
// guard on cancel_task.go:56 needs BOTH operands: a task registered
// (ok=true) with a nil CancelFunc must never be invoked. onTaskStart always
// registers a non-nil CancelFunc (task_start.go:34), so this state is
// unreachable through the ordinary dispatch path; this test reaches into
// the in-package RunHandle API directly (this file's package is
// `coordinator`, not `coordinator_test`) to construct it and isolate this
// exact branch. This kills the mutant that flips `&&` to `||`: under `||`,
// ok=true alone would satisfy the condition and CancelTask would call the
// nil cancelFn, panicking - which is exactly what this test would observe
// as a test failure/crash if the mutant were live.
func TestCancelTaskNilCancelFuncSkipsInvoke(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	started := make(chan struct{})
	release := make(chan struct{})
	_ = d.Register(runtime.Subagent, "slow", invoker(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		close(started)
		select {
		case <-release:
			return json.RawMessage(`"done"`), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}))
	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(repo, p)

	h, err := c.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: "slow"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	<-started

	// Overwrite the real registration with ok=true, cancelFn=nil: a state
	// registerTaskCancel's own contract never produces on the shipped path.
	h.registerTaskCancel("t1", nil)

	coord := c.(*coordinator)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := coord.CancelTask(ctx, h, "t1"); err != nil {
		t.Fatalf("CancelTask with a nil registered CancelFunc should not error (nor panic), got: %v", err)
	}

	snap, err := repo.GetTask(context.Background(), h.runID, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != string(ledger.TaskStatusCanceled) {
		t.Fatalf("t1 status = %q, want canceled (finalize must still run when the invoke block is skipped)", snap.Status)
	}

	// Cleanup: the real dispatched task is still blocked (its actual
	// CancelFunc was lost when we overwrote the registration above); cancel
	// the whole run so the process does not leak that goroutine.
	_ = c.Cancel(context.Background(), h)
}

// TestCancelTaskQueuedNeverDispatchedSucceeds proves a task that is still
// queued (never dispatched, no CancelFunc registered) can be canceled
// without error - it isolates the `snap.Status != string(TaskStatusQueued)`
// operand of the three-way membership check on cancel_task.go:98. A single
// worker with two tasks guarantees the second task stays queued while the
// first runs, so this call reads a real, unmodified queued snapshot. This
// kills the mutant that flips this `!=` to `==`: with a queued status, the
// mutated first clause turns true, and (combined with the unmutated
// running/awaiting_input clauses, both also true for a queued status) the
// whole condition wrongly becomes true, making CancelTask return
// "cannot be canceled" for a task that plainly can be.
func TestCancelTaskQueuedNeverDispatchedSucceeds(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	started := make(chan struct{})
	_ = d.Register(runtime.Subagent, "slow", invoker(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(repo, p)

	// wave2 depends on wave1: the DAG (dag.go:startReady) only CASes a task
	// queued->running once it becomes ready, so wave2 stays queued for as
	// long as wave1 (which blocks until its context is canceled) has not
	// completed - mirroring TestCancelDagWithQueuedDependent's fixture.
	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "wave1", Name: "slow"},
		{ID: "wave2", Name: "slow", DependsOn: []string{"wave1"}},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	<-started

	snap, err := repo.GetTask(context.Background(), h.runID, "wave2")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != string(ledger.TaskStatusQueued) {
		t.Fatalf("test invalid: wave2 status = %q, want queued (its dependency wave1 must still be running)", snap.Status)
	}

	coord := c.(*coordinator)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := coord.CancelTask(ctx, h, "wave2"); err != nil {
		t.Fatalf("CancelTask on a queued, never-dispatched task should succeed, got: %v", err)
	}

	wave2Snap, err := repo.GetTask(context.Background(), h.runID, "wave2")
	if err != nil {
		t.Fatal(err)
	}
	if wave2Snap.Status != string(ledger.TaskStatusCanceled) {
		t.Fatalf("wave2 status = %q, want canceled", wave2Snap.Status)
	}

	_ = c.Cancel(context.Background(), h)
}

// TestCancelTaskAwaitingInputSucceeds proves a task in awaiting_input can be
// canceled without error, isolating the
// `snap.Status != string(TaskStatusAwaitingInput)` operand of the same
// three-way check on cancel_task.go:100. Running -> AwaitingInput is a valid
// ledger transition (transition.go:33), so the task is forced there directly
// on the real repo after it starts. This kills the mutant that flips this
// `!=` to `==`: with status awaiting_input, the mutated third clause turns
// true (the unmutated queued/running clauses are already true for
// awaiting_input), wrongly failing the whole condition to "cannot be
// canceled".
func TestCancelTaskAwaitingInputSucceeds(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	started := make(chan struct{})
	_ = d.Register(runtime.Subagent, "slow", invoker(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(repo, p)

	h, err := c.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: "slow"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	<-started

	runningSnap, err := repo.GetTask(context.Background(), h.runID, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetTaskStatus(context.Background(), h.runID, "t1", runningSnap.Version, string(ledger.TaskStatusAwaitingInput)); err != nil {
		t.Fatalf("test setup: force awaiting_input: %v", err)
	}

	coord := c.(*coordinator)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := coord.CancelTask(ctx, h, "t1"); err != nil {
		t.Fatalf("CancelTask on an awaiting_input task should succeed, got: %v", err)
	}

	snap, err := repo.GetTask(context.Background(), h.runID, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != string(ledger.TaskStatusCanceled) {
		t.Fatalf("t1 status = %q, want canceled", snap.Status)
	}
}

// TestFinalizeSingleTaskCancelUnexpectedStatusBailsOut proves
// finalizeSingleTaskCancel bails out cleanly (no error, no status change)
// when it observes a non-terminal status that is neither cancel_requested
// nor queued - the "should not happen" branch documented at
// cancel_task.go:144-148. Only reachable callers ever hand it
// cancel_requested/queued/terminal, so this test drives the unexported
// method directly (in-package) with a task still "running" to isolate the
// guard itself. This kills a mutant on either `!=` on cancel_task.go:144:
// flipping either one to `==` makes the bail-out condition false for a
// running status, letting the loop fall through to attempt (and succeed at)
// a running->canceled CAS it should never attempt on this path.
func TestFinalizeSingleTaskCancelUnexpectedStatusBailsOut(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	started := make(chan struct{})
	_ = d.Register(runtime.Subagent, "slow", invoker(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(repo, p)

	h, err := c.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: "slow"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	<-started

	coord := c.(*coordinator)
	if err := coord.finalizeSingleTaskCancel(h, "t1"); err != nil {
		t.Fatalf("finalizeSingleTaskCancel on an unexpected non-terminal status should bail out cleanly, got: %v", err)
	}

	snap, err := repo.GetTask(context.Background(), h.runID, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != string(ledger.TaskStatusRunning) {
		t.Fatalf("status = %q, want unchanged running (finalize must bail out, not transition an unexpected status)", snap.Status)
	}

	_ = c.Cancel(context.Background(), h)
}

// TestFinalizeSingleTaskCancelRetriesOnConflict is the CAS-conflict
// regression test for finalizeSingleTaskCancel's OWN retry loop (distinct
// from requestSingleTaskCancel's, already covered by
// TestRequestSingleTaskCancelRetriesOnConflict): a single injected
// ledger.ErrConflict on the cancel_requested->canceled CAS must not abort
// the finalize, and must instead re-read and retry until it settles. This
// kills the mutant that flips cancel_task.go:154's `!=` to `==`: under `==`,
// a genuine ErrConflict would immediately return an error instead of
// retrying.
//
// A second, still-running "survivor" sibling task is kept alive throughout
// (spawnSiblingCancelRun's own pattern): with only one task in the run,
// canceling it also finishes the whole run, and the background DAG-completion
// path (record_results.go:recordTaskResult -> tryTaskStatusCAS) independently
// attempts the very same cancel_requested->canceled CAS, racing (and
// nondeterministically stealing) this test's single-shot conflict injection.
// Keeping a sibling running defers that whole-run finalize until this test
// releases it, so the ONLY caller of this CAS is finalizeSingleTaskCancel.
func TestFinalizeSingleTaskCancelRetriesOnConflict(t *testing.T) {
	base := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	victimStarted := make(chan struct{})
	_ = d.Register(runtime.Subagent, "victim", invoker(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		close(victimStarted)
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	survivorStarted := make(chan struct{})
	releaseSurvivor := make(chan struct{})
	_ = d.Register(runtime.Subagent, "survivor", invoker(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		close(survivorStarted)
		select {
		case <-releaseSurvivor:
			return json.RawMessage(`"done"`), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}))
	p := subagents.New(d, subagents.Policy{Workers: 2})
	repo := &conflictOnceRepo{
		LedgerRepository: base,
		taskID:           "victim",
		toStatus:         string(ledger.TaskStatusCanceled),
	}
	c := New(repo, p)

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "victim", Name: "victim"},
		{ID: "survivor", Name: "survivor"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	<-victimStarted
	<-survivorStarted

	coord := c.(*coordinator)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := coord.CancelTask(ctx, h, "victim"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}

	repo.mu.Lock()
	fired := repo.fired
	repo.mu.Unlock()
	if !fired {
		t.Fatal("test invalid: the conflict injector never fired, so this proves nothing about finalize's retry path")
	}

	snap, err := base.GetTask(context.Background(), h.runID, "victim")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != string(ledger.TaskStatusCanceled) {
		t.Fatalf("victim status = %q, want %q (proves finalize's own CAS-conflict retry loop actually retried and settled)", snap.Status, ledger.TaskStatusCanceled)
	}

	close(releaseSurvivor)
	if _, err := c.Join(context.Background(), h); err != nil {
		t.Fatal(err)
	}
}

// finalizeErrorOnceRepo forces a fixed non-conflict error on the first
// CompareAndSetTaskStatus call that would transition taskID to toStatus,
// then delegates to the wrapped repository normally thereafter - simulating
// a genuine (non-ErrConflict) ledger failure on finalize's own CAS, distinct
// from conflictOnceRepo's ledger.ErrConflict injection.
type finalizeErrorOnceRepo struct {
	ledger.LedgerRepository
	taskID   string
	toStatus string
	err      error

	mu    sync.Mutex
	fired bool
}

func (r *finalizeErrorOnceRepo) CompareAndSetTaskStatus(ctx context.Context, runID, taskID string, expectedVersion uint64, newStatus string) error {
	r.mu.Lock()
	if !r.fired && taskID == r.taskID && newStatus == r.toStatus {
		r.fired = true
		r.mu.Unlock()
		return r.err
	}
	r.mu.Unlock()
	return r.LedgerRepository.CompareAndSetTaskStatus(ctx, runID, taskID, expectedVersion, newStatus)
}

// TestFinalizeSingleTaskCancelNonConflictErrorSurfaces proves a genuine
// (non-ErrConflict) error from finalize's own CAS surfaces immediately as a
// real error, rather than being swallowed into the retry loop. This kills
// the reverse direction of the cancel_task.go:154 mutant: under the `==`
// mutant, a non-conflict error would skip the `return fmt.Errorf(...)`
// branch and fall into the retry loop instead, eventually surfacing (after
// taskCancelWaitBudget) as a "timed out reconciling concurrent updates"
// error rather than the actual injected failure - so this test asserts both
// that the original error is wrapped and that no timeout wording appears.
//
// As in TestFinalizeSingleTaskCancelRetriesOnConflict, a still-running
// "survivor" sibling is kept alive so the whole run does not finish and race
// the background DAG-completion finalize (record_results.go) against this
// single-shot injection on the very same CAS.
func TestFinalizeSingleTaskCancelNonConflictErrorSurfaces(t *testing.T) {
	base := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	victimStarted := make(chan struct{})
	_ = d.Register(runtime.Subagent, "victim", invoker(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		close(victimStarted)
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	survivorStarted := make(chan struct{})
	releaseSurvivor := make(chan struct{})
	_ = d.Register(runtime.Subagent, "survivor", invoker(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		close(survivorStarted)
		select {
		case <-releaseSurvivor:
			return json.RawMessage(`"done"`), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}))
	p := subagents.New(d, subagents.Policy{Workers: 2})
	boom := errors.New("boom: storage unavailable")
	repo := &finalizeErrorOnceRepo{
		LedgerRepository: base,
		taskID:           "victim",
		toStatus:         string(ledger.TaskStatusCanceled),
		err:              boom,
	}
	c := New(repo, p)

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "victim", Name: "victim"},
		{ID: "survivor", Name: "survivor"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	<-victimStarted
	<-survivorStarted

	coord := c.(*coordinator)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = coord.CancelTask(ctx, h, "victim")
	if err == nil {
		t.Fatal("expected a genuine non-conflict finalize error to surface, got nil")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want it to wrap the injected non-conflict error %v", err, boom)
	}
	if strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error = %v; a genuine non-conflict error must surface immediately, not via the retry-timeout path", err)
	}

	close(releaseSurvivor)
	if _, err := c.Join(context.Background(), h); err != nil {
		t.Fatal(err)
	}
}
