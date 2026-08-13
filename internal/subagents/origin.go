package subagents

import (
	"encoding/json"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
)

// maxTaskDescriptionBytes bounds EventOrigin.TaskDescription the same way
// every other UI-facing preview in this codebase is bounded (see
// eventPreview in internal/cli/tui_events.go) - a consumer attributing a
// subagent card to its task shouldn't have to handle an arbitrarily long
// description any more than a tool call's redacted input/output preview.
const maxTaskDescriptionBytes = 200

// taskDescriptionFromInput derives a bounded description from a task's raw
// Input for EventOrigin.TaskDescription. delegate's Input is always a bare
// JSON string (the task text); dispatch_tasks/spawn_agent's per-task Input
// can be arbitrary JSON shaped by that task's own input schema, so this
// falls back to the raw JSON text when it isn't a bare string - still a
// useful-enough preview, just not natural-language.
func taskDescriptionFromInput(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(input, &s) == nil {
		return boundTaskDescription(s)
	}
	return boundTaskDescription(string(input))
}

func boundTaskDescription(s string) string {
	if len(s) <= maxTaskDescriptionBytes {
		return s
	}
	return s[:maxTaskDescriptionBytes] + "…"
}

// StampEventOrigin decorates onEvent so every event it receives carries the
// given origin. An event that already has an origin keeps it - the stamp
// closest to the producing loop wins, so deeper nesting is never rewritten
// by an outer handler.
//
// A nil onEvent stays nil so callers keep their existing nil checks.
func StampEventOrigin(onEvent func(agent.Event), origin agent.EventOrigin) func(agent.Event) {
	if onEvent == nil {
		return nil
	}
	return func(e agent.Event) {
		if e.Origin.IsZero() {
			e.Origin = origin
		}
		onEvent(e)
	}
}
