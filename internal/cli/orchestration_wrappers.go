package cli

// orchestration_wrappers.go re-exports orchestration constants and functions
// from cliorchestrate for callers that import cli. See cliorchestrate for
// the authoritative definitions.

import (
	"github.com/MiviaLabs/mivia-agent/internal/cli/orchestrate"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// HandlerDelegate re-exports orchestrate.HandlerDelegate.
const HandlerDelegate = orchestrate.HandlerDelegate

// ToolDispatchTasks re-exports orchestrate.ToolDispatchTasks.
const ToolDispatchTasks = orchestrate.ToolDispatchTasks

// OrchestrationCoordinator re-exports the cliorchestrate narrow coordinator
// interface so callers that import cli can name it.
type OrchestrationCoordinator = orchestrate.OrchestrationCoordinator

// ActiveCoordinator delegates to orchestrate.ActiveCoordinator.
func ActiveCoordinator() (OrchestrationCoordinator, bool) {
	return orchestrate.ActiveCoordinator()
}

// SetSubagentTaskRouteSink delegates to
// orchestrate.SetSubagentTaskRouteSink. Its parameter is spelled as the
// unnamed func type on purpose: internal/tui/run assigns this function
// itself to adapter.SubagentTaskRouteRegistrar, which requires identical
// function types.
func SetSubagentTaskRouteSink(fn func(coord OrchestrationCoordinator, callID, runID, taskID string)) {
	orchestrate.SetSubagentTaskRouteSink(fn)
}

// SetActiveSessionCaller delegates to orchestrate.SetActiveSessionCaller.
func SetActiveSessionCaller(caller runtime.Caller) {
	orchestrate.SetActiveSessionCaller(caller)
}
