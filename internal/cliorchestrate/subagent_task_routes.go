package cliorchestrate

import (
	"sync/atomic"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// subagentTaskRouteSink holds the process-wide sink SetSubagentTaskRouteSink
// installs: the UI's route table, handed here by
// uiadapter.SubagentTaskRouteRegistrar through cli.SetSubagentTaskRouteSink,
// so nothing in this package imports internal/uiadapter and
// internal/uiadapter never imports internal/cli* (INV-TUI-29,
// docs/design/ui-isolation.md).
//
// An atomic pointer, not a plain var: dispatches publish routes from
// subagent goroutines while the TUI installs the sink on the main
// goroutine. The nil zero value - every process that never runs the TUI, so
// headless one-shot runs and tests - makes registerSubagentTaskRoutes a
// clean no-op rather than a panic.
var subagentTaskRouteSink atomic.Pointer[func(coord coordinator.Coordinator, callID, runID, taskID string)]

// SetSubagentTaskRouteSink installs the sink every later dispatch publishes
// its spawned tasks' coordinator routes into. A nil argument removes the
// sink (stored as a nil pointer, never as a pointer to a nil func, so the
// publish path needs exactly one guard). Safe to call at any time from any
// goroutine; the last call wins.
func SetSubagentTaskRouteSink(fn func(coord coordinator.Coordinator, callID, runID, taskID string)) {
	if fn == nil {
		subagentTaskRouteSink.Store(nil)
		return
	}
	subagentTaskRouteSink.Store(&fn)
}

// registerSubagentTaskRoutes publishes one route per spawned task (coordinator,
// run ID, task ID) to the installed sink.
//
// Invocation points:
//   - RunThroughCoordinator (dispatch_tasks default wait="run")
//   - spawnAndWait (spawn_agent and dispatch_tasks async waits)
//
// Both call immediately after reading the run snapshot, when runID and
// tasks[i].ID are available together.
//
// The registered callID is tasks[i].ID (the namespaced "<dispatch tool call id>:
// <raw model task id>" built in buildTasks), not the raw tool-call ID. UI keys
// subagent rows on this namespaced ID live (dispatchTaskIDsAndNames) and from
// history (populateDispatchTasks); callID and taskID are identical here.
//
// The coordinator travels per route because it is created lazily per session on
// first dispatch (InitCoordinator) and cannot be installed at TUI-build time,
// while the UI route table is shared across pooled sessions.
func registerSubagentTaskRoutes(c coordinator.Coordinator, runID string, tasks []subagents.Task) {
	p := subagentTaskRouteSink.Load()
	if p == nil {
		return
	}
	sink := *p
	for i := range tasks {
		// The sink itself drops empty ids, so a task with no id
		// (impossible through buildTasks, which rejects one) needs no
		// guard here.
		sink(c, tasks[i].ID, runID, tasks[i].ID)
	}
}
