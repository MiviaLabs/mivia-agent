package coordinator

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// onTaskStartFixture spawns a real, already-completed run so its RunHandle
// is registered on the coordinator (HandleForRun-reachable) without needing
// a live in-flight task: onTaskStart's own guard logic is what these tests
// exercise directly, not the pool's dispatch machinery.
func onTaskStartFixture(t *testing.T) (*coordinator, *RunHandle) {
	t.Helper()
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "noop", invoker(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		return json.RawMessage(`"ok"`), nil
	}))
	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(repo, p).(*coordinator)

	h, err := c.Spawn(context.Background(), []subagents.Task{{ID: "seed", Name: "noop"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Join(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	return c, h
}

// TestOnTaskStart_NoIdentity proves onTaskStart registers nothing when the
// context carries no stamped task identity at all (ok == false).
func TestOnTaskStart_NoIdentity(t *testing.T) {
	c, h := onTaskStartFixture(t)
	c.onTaskStart(context.Background(), subagents.Task{ID: "tA", Name: "noop"}, func() {})
	if _, _, ok := h.taskCancelFunc("tA"); ok {
		t.Fatal("onTaskStart registered a CancelFunc with no stamped identity in ctx")
	}
}

// TestOnTaskStart_EmptyRunID proves onTaskStart registers nothing when the
// identity's RunID is empty, even with a non-empty TaskID.
func TestOnTaskStart_EmptyRunID(t *testing.T) {
	c, h := onTaskStartFixture(t)
	ctx := runtime.ContextWithTaskIdentity(context.Background(), runtime.TaskIdentity{RunID: "", TaskID: "tB"})
	c.onTaskStart(ctx, subagents.Task{ID: "tB", Name: "noop"}, func() {})
	if _, _, ok := h.taskCancelFunc("tB"); ok {
		t.Fatal("onTaskStart registered a CancelFunc with an empty RunID")
	}
}

// TestOnTaskStart_EmptyTaskID proves onTaskStart registers nothing when the
// identity's TaskID is empty, even with a valid, registered RunID.
func TestOnTaskStart_EmptyTaskID(t *testing.T) {
	c, h := onTaskStartFixture(t)
	ctx := runtime.ContextWithTaskIdentity(context.Background(), runtime.TaskIdentity{RunID: h.runID, TaskID: ""})
	c.onTaskStart(ctx, subagents.Task{ID: "tC", Name: "noop"}, func() {})
	if _, _, ok := h.taskCancelFunc("tC"); ok {
		t.Fatal("onTaskStart registered a CancelFunc with an empty TaskID")
	}
}

// TestOnTaskStart_ValidIdentityRegisters is the control case: a fully
// populated identity for a live handle must register the CancelFunc, so the
// three guard tests above are proven against real registration behavior
// rather than a helper that never registers anything.
func TestOnTaskStart_ValidIdentityRegisters(t *testing.T) {
	c, h := onTaskStartFixture(t)
	ctx := runtime.ContextWithTaskIdentity(context.Background(), runtime.TaskIdentity{RunID: h.runID, TaskID: "tD"})
	c.onTaskStart(ctx, subagents.Task{ID: "tD", Name: "noop"}, func() {})
	if _, _, ok := h.taskCancelFunc("tD"); !ok {
		t.Fatal("onTaskStart did not register a CancelFunc for a valid identity on a live handle")
	}
}
