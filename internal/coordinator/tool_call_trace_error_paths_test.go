package coordinator

import (
	"context"
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// errGetTaskRepo wraps a real repository and fails GetTask with an
// arbitrary, non-ErrNotFound error - the "broken store" shape distinct
// from every existing tool_call_trace_test.go case (all of which use
// ErrNotFound or ErrContentNotFound, the sentinels this call site treats
// as "no trace" rather than propagating).
type errGetTaskRepo struct {
	ledger.LedgerRepository
	failGetTask     bool
	failLoadContent bool
}

func (r *errGetTaskRepo) GetTask(ctx context.Context, runID, taskID string) (ledger.TaskSnapshot, error) {
	if r.failGetTask {
		return ledger.TaskSnapshot{}, errors.New("store wedged")
	}
	return r.LedgerRepository.GetTask(ctx, runID, taskID)
}

func (r *errGetTaskRepo) LoadContent(ctx context.Context, ref string) ([]byte, error) {
	if r.failLoadContent {
		return nil, errors.New("store wedged")
	}
	return r.LedgerRepository.LoadContent(ctx, ref)
}

// TestLoadTaskToolCalls_GetTaskGenuineErrorSurfaces pins the propagation
// branch distinct from the ErrNotFound-is-empty case: a real store failure
// must not be swallowed as "no trace".
func TestLoadTaskToolCalls_GetTaskGenuineErrorSurfaces(t *testing.T) {
	repo := &errGetTaskRepo{LedgerRepository: ledger.NewMemoryLedgerRepository(), failGetTask: true}
	d := runtime.New(runtime.Policy{})
	c := New(repo, subagents.New(d, subagents.Policy{Workers: 1}))
	if _, err := mustTraceLoader(t, c).LoadTaskToolCalls(context.Background(), "run-1", "t1"); err == nil {
		t.Fatal("LoadTaskToolCalls swallowed a genuine GetTask store failure")
	}
}

// TestLoadTaskToolCalls_LoadContentGenuineErrorSurfaces mirrors the above
// for LoadContent, distinct from TestLoadTaskToolCallsMissingTraceContentIsEmpty
// (ErrContentNotFound).
func TestLoadTaskToolCalls_LoadContentGenuineErrorSurfaces(t *testing.T) {
	inner := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	if err := d.Register(runtime.Subagent, "toolcaller", toolCallEmittingHandler{toolName: "run_command"}); err != nil {
		t.Fatal(err)
	}
	c := New(inner, subagents.New(d, subagents.Policy{Workers: 1}))
	h, err := c.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: "toolcaller"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Join(context.Background(), h); err != nil {
		t.Fatal(err)
	}

	wrapped := &errGetTaskRepo{LedgerRepository: inner, failLoadContent: true}
	c2 := New(wrapped, subagents.New(d, subagents.Policy{Workers: 1}))
	if _, err := mustTraceLoader(t, c2).LoadTaskToolCalls(context.Background(), h.runID, "t1"); err == nil {
		t.Fatal("LoadTaskToolCalls swallowed a genuine LoadContent store failure")
	}
}

// TestOnTaskStart_NilCoordinatorAndUnstampedContextAreNoOps pins onTaskStart's
// two earliest guards directly: a nil receiver and a context carrying no
// coordinator-stamped task identity must both be silent no-ops rather than
// panicking or registering a cancel func nothing will ever look up.
func TestOnTaskStart_NilCoordinatorAndUnstampedContextAreNoOps(t *testing.T) {
	var nilC *Coordinator
	nilC.onTaskStart(context.Background(), subagents.Task{ID: "t1"}, func() {})

	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	c := New(repo, subagents.New(d, subagents.Policy{Workers: 1}))
	c.onTaskStart(context.Background(), subagents.Task{ID: "t1"}, func() {}) // unstamped ctx
}

// TestShouldSkipCanceledTask_NilCoordinatorAndUnstampedContextFailOpen pins
// shouldSkipCanceledTask's fail-open guards: every uncertain case must run
// the task (return false), never silently skip real work.
func TestShouldSkipCanceledTask_NilCoordinatorAndUnstampedContextFailOpen(t *testing.T) {
	var nilC *Coordinator
	if nilC.shouldSkipCanceledTask(context.Background(), subagents.Task{ID: "t1"}) {
		t.Fatal("a nil coordinator must fail open (false), not claim the task is canceled")
	}
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	c := New(repo, subagents.New(d, subagents.Policy{Workers: 1}))
	if c.shouldSkipCanceledTask(context.Background(), subagents.Task{ID: "t1"}) {
		t.Fatal("an unstamped context must fail open (false)")
	}
}

// TestRunHandle_SubagentToolCanceler_NilReceiverIsNoop pins the RunHandle
// method's own nil-receiver guard, distinct from the Coordinator-level nil
// checks tool_cancel_guard_test.go already covers.
func TestRunHandle_SubagentToolCanceler_NilReceiverIsNoop(t *testing.T) {
	var h *RunHandle
	if _, ok := h.subagentToolCanceler("t1"); ok {
		t.Fatal("a nil RunHandle must report no registered canceler, not panic or claim one exists")
	}
}
