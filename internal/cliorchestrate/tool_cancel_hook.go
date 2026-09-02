package cliorchestrate

import (
	"context"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// ToolCancelReadyHook returns the OnToolCancelReady callback a
// subagents.MultiStepHandler dispatched on d should install (via
// SessionDispatcherOpts.OnToolCancelReady - see internal/clichat).
//
// The returned func resolves the coordinator lazily, at call time, from the
// SAME package-level dispatcher->coordinator map InitCoordinator populates:
// handler registration (which captures this hook) happens BEFORE
// InitCoordinator runs for a fresh session (see internal/clichat/dispatcher.go),
// so resolving eagerly would always miss. By the time a subagent task's
// nested loop actually calls this hook, InitCoordinator has long since run.
//
// It forwards to the coordinator's own registry only when ctx carries a
// runtime.TaskIdentity - i.e. this invocation was dispatched by that
// coordinator's subagent pool (contextForTask stamps it before
// Pool.executeOne calls the dispatcher). A root-session tool call, a
// skill/oneshot handler, or any other MultiStepHandler invocation that
// reaches Invoke outside the coordinator's pool carries no identity and
// this hook is a no-op for it - there is no run/task pair to key a
// cancel-by-ID registry on.
//
// A dispatcher with no registered coordinator at all (never initialized, or
// a bare test dispatcher) also yields a no-op: nothing to register with.
func ToolCancelReadyHook(d *runtime.Dispatcher) func(ctx context.Context, canceler agent.ToolCanceler) {
	return func(ctx context.Context, canceler agent.ToolCanceler) {
		v, ok := coordinators.Load(d)
		if !ok {
			return
		}
		coord, ok := v.(coordinator.Coordinator)
		if !ok {
			return
		}
		id, ok := runtime.TaskIdentityFrom(ctx)
		if !ok {
			return
		}
		coord.RegisterSubagentToolCanceler(id.RunID, id.TaskID, canceler)
	}
}
