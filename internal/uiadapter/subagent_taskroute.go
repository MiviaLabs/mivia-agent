package uiadapter

import (
	"context"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
)

// This file holds the task-route half of SubagentThreads: how a registered
// callID resolves back to the coordinator that dispatched it, so a UI action
// can cancel ONE dispatched task or ONE in-flight call inside it. Split from
// subagent.go, which carries the thread registry and the transcript
// conversation itself; this half touches the coordinator, that half never
// does. The route timeout (subagentTaskTimeout) lives here too, beside the
// two cancels that spend it; subagent_resolve.go spends it on ref resolution.

// subagentTaskTimeout bounds any single I/O call this package makes into
// the coordinator or a content-ref resolver on behalf of a UI action:
// canceling a task, canceling a tool call, or (subagent_resolve.go)
// resolving a persisted tool-call trace reference. One constant, reused by
// every caller, so a UI action never hangs indefinitely on a stalled
// backend and every caller times out after the same interval.
const subagentTaskTimeout = 30 * time.Second

// subagentTaskRoute is the coordinator identity backing one registered
// callID: the coordinator that dispatched it, the run it belongs to, and
// its own task ID within that run - everything CancelSubagentTask needs to
// reach SubagentTaskCoordinator.CancelTask.
//
// The coordinator is per-route, not one field on the registry, because ONE
// SubagentThreads is shared by every pooled session (see SessionPool) while
// a coordinator is created per *runtime.Dispatcher, so per session
// (internal/cliorchestrate.InitCoordinator). A single registry-wide
// coordinator would bind every session's routes to whichever session
// dispatched last, and a lookup for another session's run would then miss
// and surface as the misleading "run is no longer active". Carrying it here
// costs nothing: the registering caller dispatched the task and therefore
// knows exactly which coordinator owns it.
type subagentTaskRoute struct {
	coord  SubagentTaskCoordinator
	runID  string
	taskID string
}

// SubagentTaskCoordinator is this package's consumer-side view of a
// coordinator: only the three members CancelSubagentTask and
// CancelSubagentToolCall need to resolve a registered callID to a live
// run/task and stop it. The full coordinator carries far more; this package
// depends on the subset, not the fat interface.
type SubagentTaskCoordinator interface {
	HandleForRun(runID string) *coordinator.RunHandle
	CancelTask(ctx context.Context, h *coordinator.RunHandle, taskID string) error
	CancelSubagentToolCall(ctx context.Context, h *coordinator.RunHandle, taskID, callID string) (bool, error)
}

// Compile-time check that the real coordinator satisfies this subset.
var _ SubagentTaskCoordinator = (*coordinator.Coordinator)(nil)

// RegisterTaskRoute records, for callID, the coordinator + run + task triple
// the cancel entry points resolve against. A nil coord is recorded as-is
// rather than rejected: resolveTaskRoute turns it into a clear "no
// coordinator wired" error, which is more diagnosable than a route that
// silently never existed.
func (s *SubagentThreads) RegisterTaskRoute(coord SubagentTaskCoordinator, callID, runID, taskID string) {
	if callID == "" || runID == "" || taskID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.routes == nil {
		s.routes = map[string]subagentTaskRoute{}
	}
	s.routes[callID] = subagentTaskRoute{coord: coord, runID: runID, taskID: taskID}
}

// CancelSubagentTask stops the coordinator's execution of the ONE dispatched
// task backing callID, leaving its sibling tasks and the parent run
// untouched. See ports.SubagentThreads.CancelSubagentTask's doc comment for
// how this differs from TurnHandle.Cancel()/ActiveTurn().Cancel() (which
// only detach a UI listener from the live event stream).
func (s *SubagentThreads) CancelSubagentTask(callID string) (bool, error) {
	coord, h, taskID, err := s.resolveTaskRoute(callID)
	if err != nil {
		return false, err
	}
	if h == nil {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), subagentTaskTimeout)
	defer cancel()
	if err := coord.CancelTask(ctx, h, taskID); err != nil {
		return false, err
	}
	return true, nil
}

// CancelSubagentToolCall cancels ONE in-flight tool call within the ONE
// dispatched task backing callID, leaving the task itself, its siblings,
// and the parent run untouched. See ports.SubagentThreads.CancelSubagentToolCall's
// doc comment for the ok/error split; the route resolution here is
// identical to CancelSubagentTask's, deliberately not duplicated into a
// third copy - see resolveTaskRoute.
func (s *SubagentThreads) CancelSubagentToolCall(callID, toolCallID string) (bool, error) {
	coord, h, taskID, err := s.resolveTaskRoute(callID)
	if err != nil {
		return false, err
	}
	if h == nil {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), subagentTaskTimeout)
	defer cancel()
	return coord.CancelSubagentToolCall(ctx, h, taskID, toolCallID)
}

// resolveTaskRoute resolves callID down to the coordinator that dispatched
// it, its RunHandle, and its task ID - the shared first half of both
// CancelSubagentTask and CancelSubagentToolCall. A nil handle with a nil
// error means "no route registered for this callID" (a safe no-op for the
// caller); a non-nil error means a route WAS found but the coordinator
// itself could not serve it.
func (s *SubagentThreads) resolveTaskRoute(callID string) (SubagentTaskCoordinator, *coordinator.RunHandle, string, error) {
	s.mu.Lock()
	route, ok := s.routes[callID]
	s.mu.Unlock()
	if !ok {
		return nil, nil, "", nil
	}
	if route.coord == nil {
		return nil, nil, "", fmt.Errorf("uiadapter: no coordinator wired to reach subagent task %q", callID)
	}
	h := route.coord.HandleForRun(route.runID)
	if h == nil {
		return nil, nil, "", fmt.Errorf("uiadapter: run %q for subagent task %q is no longer active", route.runID, callID)
	}
	return route.coord, h, route.taskID, nil
}
