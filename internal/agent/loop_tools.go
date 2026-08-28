package agent

import (
	"encoding/json"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// dispatchTasksToolName mirrors cliorchestrate.ToolDispatchTasks. Not
// imported: internal/cliorchestrate depends on internal/subagents, which
// depends on this package, so importing it here would cycle. Keep this
// literal in sync with that constant.
const dispatchTasksToolName = "dispatch_tasks"

// identifiedToolCalls gives an ID to every call the provider left without one.
//
// A tool result is bound to its call by id and by nothing else, so an
// unidentified call cannot be answered: it was recorded with an empty ID, which
// provider.ValidateToolPairing rejects outright, and its result carried an
// empty tool_call_id, which is the orphan case again. The ID is ours to author
// because the assistant message the provider sees is ours to author - the model
// never sent one to contradict.
//
// The value is random rather than a counter. IDs must not repeat across the
// WHOLE history (ValidateToolPairing rejects a reused ID), and history outlives
// any one Loop: a per-turn counter would re-issue its first ID on the next
// turn, against messages the session carried forward.
func identifiedToolCalls(calls []provider.ToolCall) []provider.ToolCall {
	identified := make([]provider.ToolCall, 0, len(calls))
	for _, call := range calls {
		if strings.TrimSpace(call.ID) == "" {
			call.ID = "call_" + runtime.NewSessionID()
		}
		identified = append(identified, call)
	}
	return identified
}

func toolEndDetail(r toolExecResult) string {
	// A duplicate never re-ran, so its failure signal is judged against the
	// ORIGINAL recorded body the dedup cache served, not against the
	// suppression notice that replaced it: a run_command duplicate reports its
	// non-zero child exit in the recorded header with err==nil, and reading the
	// notice (which carries no status) would silently downgrade a failed
	// duplicate to completed.
	if r.duplicate {
		if r.err != nil || toolResultBodyFailed(r.toolCall.Function.Name, r.originalBody) {
			return "failed (duplicate)"
		}
		return "completed (duplicate)"
	}
	// Failed takes precedence over truncation (skeptic: both can be set).
	if r.err != nil || toolResultBodyFailed(r.toolCall.Function.Name, r.result) {
		if r.truncated {
			return "failed (truncated)"
		}
		return "failed"
	}
	if r.truncated {
		return "completed (truncated)"
	}
	return "completed"
}

// toolResultBodyFailed detects failure signals inside tool result text when
// Execute returned err=nil - only run_command does that, reporting a non-zero
// child exit in its result header while the call itself succeeded.
//
// The check is scoped by tool name because every other tool returns content
// verbatim: file text opening with "Error handling…" or a grep hit quoting
// "exit=1" is data, not a status, and scanning it reported healthy calls as
// failed. Bodies the loop synthesizes ("error: …") always carry a non-nil err,
// so scoping here loses no failure signal.
func toolResultBodyFailed(name, body string) bool {
	if body == "" {
		return false
	}
	switch name {
	case tools.RunCommandToolName:
		return runCommandBodyFailed(body)
	case dispatchTasksToolName:
		return dispatchTasksBodyFailed(body)
	default:
		return false
	}
}

// runCommandBodyFailed reads run_command's own exit-status header.
// Header shape: "command: …\ncwd: …\nexit=<status>\n". exit=0 is success;
// any other status (1, 127, timeout, canceled, error) is failure.
func runCommandBodyFailed(body string) bool {
	for _, line := range strings.Split(body, "\n") {
		status, ok := strings.CutPrefix(strings.TrimSpace(line), "exit=")
		if !ok {
			continue
		}
		return status != "0"
	}
	return false
}

// dispatchTasksBodyFailed detects the whole-batch-rejection envelope
// dispatch_tasks.Execute answers with a nil Go error on purpose
// (internal/cliorchestrate/dispatch.go: a pre-flight wait/schema
// rejection, an expired caller context, or a coordinator/spawn failure
// that never dispatched a single task) - so the caller keeps the
// run_id/hint fields a hard Go error would discard. Every one of those
// shapes is a JSON OBJECT carrying a top-level "error" and no "tasks"
// key. A per-task result array (wait="run", tasks actually ran, some
// maybe individually failed) is a different top-level shape (a JSON
// array) and never matches; the async payload always carries "tasks"
// even when it also carries a run-level "run_error", so it is left
// alone here too - only the wait="run" envelope this bug was reported
// against is covered.
func dispatchTasksBodyFailed(body string) bool {
	trimmed := strings.TrimSpace(body)
	if !strings.HasPrefix(trimmed, "{") {
		return false
	}
	var probe struct {
		Error string          `json:"error"`
		Tasks json.RawMessage `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(trimmed), &probe); err != nil {
		return false
	}
	return probe.Error != "" && probe.Tasks == nil
}
