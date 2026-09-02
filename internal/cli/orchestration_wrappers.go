package cli

// orchestration_wrappers.go re-exports orchestration constants and functions
// from cliorchestrate for callers that import cli. See cliorchestrate for
// the authoritative definitions.

import (
	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// HandlerDelegate re-exports cliorchestrate.HandlerDelegate.
const HandlerDelegate = cliorchestrate.HandlerDelegate

// ToolDispatchTasks re-exports cliorchestrate.ToolDispatchTasks.
const ToolDispatchTasks = cliorchestrate.ToolDispatchTasks

// ActiveCoordinator delegates to cliorchestrate.ActiveCoordinator.
func ActiveCoordinator() (coordinator.Coordinator, bool) {
	return cliorchestrate.ActiveCoordinator()
}

// SetSubagentTaskRouteSink delegates to
// cliorchestrate.SetSubagentTaskRouteSink. Its parameter is spelled as the
// unnamed func type on purpose: internal/newtui assigns this function
// itself to uiadapter.SubagentTaskRouteRegistrar, which requires identical
// function types.
func SetSubagentTaskRouteSink(fn func(coord coordinator.Coordinator, callID, runID, taskID string)) {
	cliorchestrate.SetSubagentTaskRouteSink(fn)
}

// SetActiveSessionCaller delegates to cliorchestrate.SetActiveSessionCaller.
func SetActiveSessionCaller(caller runtime.Caller) {
	cliorchestrate.SetActiveSessionCaller(caller)
}
