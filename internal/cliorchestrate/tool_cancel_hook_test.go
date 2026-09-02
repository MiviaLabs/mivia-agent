// tool_cancel_hook_test.go proves ToolCancelReadyHook's three independent
// early-return guards (tool_cancel_hook.go) one at a time: no coordinator
// registered for the dispatcher at all, a coordinator registered under the
// wrong type, and a coordinator registered correctly but ctx carrying no
// runtime.TaskIdentity. A positive control proves the happy path (all three
// conditions satisfied) actually registers, so the guard tests are proven
// against real registration behavior rather than an untested code path.
package cliorchestrate

import (
	"context"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// fakeCancelCoordinator is a minimal coordinator.Coordinator test double
// that records RegisterSubagentToolCanceler calls. It embeds a nil
// coordinator.Coordinator so it satisfies the (large) interface without
// implementing every method - any method other than
// RegisterSubagentToolCanceler panics if called, which ToolCancelReadyHook
// never does.
type fakeCancelCoordinator struct {
	coordinator.Coordinator

	mu       sync.Mutex
	calls    int
	runID    string
	taskID   string
	canceler agent.ToolCanceler
}

func (f *fakeCancelCoordinator) RegisterSubagentToolCanceler(runID, taskID string, canceler agent.ToolCanceler) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.runID, f.taskID, f.canceler = runID, taskID, canceler
}

func (f *fakeCancelCoordinator) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// noopCanceler is a valid, non-nil agent.ToolCanceler for hook tests that
// never expect it to actually be invoked.
func noopCanceler(string) bool { return false }

// TestToolCancelReadyHook_NoCoordinatorRegisteredIsNoop proves the hook is
// a clean no-op when d has no entry in the package-level coordinators map
// at all (never initialized, or a bare test dispatcher). Isolates the
// `!ok` leg of the coordinators.Load guard (tool_cancel_hook.go:36): a
// mutant that drops the `!` would instead skip this guard's return and
// fall through to a nil-interface method call, panicking this test.
func TestToolCancelReadyHook_NoCoordinatorRegisteredIsNoop(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	// Deliberately no StoreTestCoordinator call: coordinators.Load(d) misses.

	ctx := runtime.ContextWithTaskIdentity(context.Background(), runtime.TaskIdentity{
		RunID: "run-1", TaskID: "task-1", Agent: "a",
	})
	hook := ToolCancelReadyHook(d)
	hook(ctx, noopCanceler) // must not panic
}

// TestToolCancelReadyHook_WrongTypeInMapIsNoop proves the hook is a clean
// no-op when d's entry in the coordinators map exists but does not hold a
// coordinator.Coordinator (a corrupted or foreign value). Isolates the
// `!ok` leg of the type-assertion guard (tool_cancel_hook.go:40): a mutant
// that drops the `!` would fall through with a nil coord interface and
// panic on the eventual RegisterSubagentToolCanceler call.
func TestToolCancelReadyHook_WrongTypeInMapIsNoop(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	CoordinatorsForTest.Store(d, "not-a-coordinator")
	defer CoordinatorsForTest.Delete(d)

	ctx := runtime.ContextWithTaskIdentity(context.Background(), runtime.TaskIdentity{
		RunID: "run-1", TaskID: "task-1", Agent: "a",
	})
	hook := ToolCancelReadyHook(d)
	hook(ctx, noopCanceler) // must not panic
}

// TestToolCancelReadyHook_NoTaskIdentityIsNoop proves the hook is a clean
// no-op when a real coordinator IS registered but ctx carries no
// runtime.TaskIdentity (a root-session tool call, a skill/oneshot handler,
// or any MultiStepHandler invocation outside the coordinator's subagent
// pool). Isolates the `!ok` leg of the TaskIdentityFrom guard
// (tool_cancel_hook.go:44): a mutant that drops the `!` would instead call
// through to RegisterSubagentToolCanceler with a zero-value TaskIdentity,
// which this test's call-count assertion catches without needing a panic.
func TestToolCancelReadyHook_NoTaskIdentityIsNoop(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	fake := &fakeCancelCoordinator{}
	cleanup := StoreTestCoordinator(d, fake, nil)
	defer cleanup()

	hook := ToolCancelReadyHook(d)
	hook(context.Background(), noopCanceler) // no identity on ctx

	if got := fake.callCount(); got != 0 {
		t.Fatalf("RegisterSubagentToolCanceler called %d times with no task identity on ctx, want 0", got)
	}
}

// TestToolCancelReadyHook_RegistersWhenAllConditionsSatisfied is the
// positive control: a real coordinator registered, and ctx carrying a
// valid TaskIdentity, forwards to RegisterSubagentToolCanceler with the
// identity's RunID/TaskID and the canceler unchanged. Proves the three
// guard tests above are isolating real early-return branches, not just
// asserting against a hook that never does anything.
func TestToolCancelReadyHook_RegistersWhenAllConditionsSatisfied(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	fake := &fakeCancelCoordinator{}
	cleanup := StoreTestCoordinator(d, fake, nil)
	defer cleanup()

	ctx := runtime.ContextWithTaskIdentity(context.Background(), runtime.TaskIdentity{
		RunID: "run-42", TaskID: "task-7", Agent: "a",
	})
	hook := ToolCancelReadyHook(d)
	hook(ctx, noopCanceler)

	if got := fake.callCount(); got != 1 {
		t.Fatalf("RegisterSubagentToolCanceler called %d times, want 1", got)
	}
	if fake.runID != "run-42" || fake.taskID != "task-7" {
		t.Fatalf("RegisterSubagentToolCanceler got runID=%q taskID=%q, want run-42/task-7", fake.runID, fake.taskID)
	}
	if fake.canceler == nil {
		t.Fatal("RegisterSubagentToolCanceler got a nil canceler, want the one passed to the hook")
	}
}
