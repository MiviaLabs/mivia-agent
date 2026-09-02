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

// registerSubagentTaskRoutes publishes one route per spawned task - the
// coordinator that dispatched it, the run, and the task - to the installed
// sink. Called from BOTH dispatch paths: RunThroughCoordinator
// (dispatch_tasks' default wait="run") and spawnAndWait (spawn_agent, and
// dispatch_tasks' async waits). Both call it immediately after the run
// snapshot is read, the first moment runID and every tasks[i].ID exist
// together.
//
// The registered callID is tasks[i].ID itself: the namespaced
// "<dispatch tool call id>:<raw model task id>" namespacedTaskID builds in
// buildTasks. That is deliberately NOT the raw dispatch_tasks tool-call id,
// because the namespaced form is what the UI keys a subagent row on - both
// live (dispatchTaskIDsAndNames in
// internal/ui/screen/conversation/events.go) and rebuilt from persisted
// history (populateDispatchTasks in
// internal/uiadapter/subagent_reconstruct.go). callID and taskID are
// therefore the same string here: the coordinator's own task id IS the UI's
// row id.
//
// The coordinator travels with each route rather than being installed once
// at TUI-build time. It has to: it does not exist when the TUI builds its
// screen (InitCoordinator creates it lazily, on the first dispatch), and one
// UI route table is shared by every pooled session while a coordinator is
// per session. Publishing it here is both the earliest available moment and
// necessarily early enough - a task cannot be highlighted in the panel, let
// alone cancelled, before the dispatch that runs this.
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
